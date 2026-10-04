// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is the hotseat command line.
// The bus subcommand listens until the process is signalled. It does not
// launch agents and it does not open connections to clients.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/store"
)

// Version is set at build time with -X.
var Version = "dev"

// ExitFailure is the process status for a startup or runtime error.
const ExitFailure = 1

var caughtSignal atomic.Int32

// Execute runs the hotseat command tree until it returns or a signal arrives.
func Execute() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		sig, ok := <-sigCh
		if !ok {
			return
		}
		if s, ok := sig.(syscall.Signal); ok {
			caughtSignal.Store(int32(s))
		}
		cancel()
	}()
	return ExecuteArgs(ctx, os.Args[1:])
}

// ExecuteArgs runs the command tree with args. args does not include the program name.
func ExecuteArgs(ctx context.Context, args []string) error {
	root := newRoot()
	root.SetArgs(args)
	if ctx == nil {
		ctx = context.Background()
	}
	return root.ExecuteContext(ctx)
}

// SignalExitCode returns 128+signum when Execute was interrupted, otherwise 0.
// main consults it before the error path so a cancelled run exits 130 or 143
// rather than status 1 from context.Canceled.
func SignalExitCode() int {
	if s := caughtSignal.Load(); s != 0 {
		return 128 + int(s)
	}
	return 0
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "hotseat",
		Short:         "Durable conversation bus",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newBusCmd())
	return root
}

func newBusCmd() *cobra.Command {
	var storeDir string
	var listenAddr string
	var maxBody int
	var debug bool

	cmd := &cobra.Command{
		Use:           "bus",
		Short:         "Host conversations on a loopback address",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat bus --store /var/lib/hotseat
  hotseat bus --store /var/lib/hotseat --listen 127.0.0.1:4727 --max-body 524288`,
		Long: `Listen for create, publish, read, wait, close, and list.

The store directory is required. The database is ` + store.FileName + ` inside that
directory. A non-loopback address is refused and nothing is opened.
Clients send JSON to POST /v1/<operation>. The process runs until it is signalled.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBus(cmd.Context(), storeDir, listenAddr, maxBody, newLogger(debug))
		},
	}
	cmd.Flags().StringVar(&storeDir, "store", "", "directory for the SQLite database (required)")
	cmd.Flags().StringVar(&listenAddr, "listen", bus.DefaultListen, "loopback listen address")
	cmd.Flags().IntVar(&maxBody, "max-body", bus.DefaultMaxBody, "maximum message body size in bytes")
	cmd.Flags().BoolVar(&debug, "debug", false, "log wait and store detail to stderr")
	if err := cmd.MarkFlagRequired("store"); err != nil {
		panic(err)
	}
	return cmd
}

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// runBus validates configuration, opens the store, and serves until ctx is cancelled.
// The listen address is checked before the store exists, so a refused address
// does not create a database.
func runBus(ctx context.Context, storeDir, addr string, maxBody int, log *slog.Logger) (err error) {
	if storeDir == "" {
		return errors.New("store directory is required")
	}
	storeDir = filepath.Clean(storeDir)
	if maxBody < 1 {
		return errors.New("max body must be a positive number of bytes")
	}
	if err := bus.ValidateListen(addr); err != nil {
		return err
	}
	if log == nil {
		log = newLogger(false)
	}

	st, err := store.Open(storeDir)
	if err != nil {
		return err
	}
	defer func() {
		cerr := st.Close()
		if err == nil && cerr != nil {
			err = cerr
		}
	}()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	log.Info("listening", "addr", ln.Addr().String(), "store", st.Path())
	err = bus.Serve(ctx, ln, st, bus.Options{
		MaxBody: maxBody,
		Logger:  log,
	})
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		log.Info("stopped", "addr", ln.Addr().String())
		return nil
	}
	return err
}
