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
	"io"
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
	return execute(ctx, args, nil, nil)
}

// execute runs the command tree. Nil writers use the process standard streams.
func execute(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return executeIO(ctx, args, nil, stdout, stderr)
}

func executeIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := newRoot()
	root.SetArgs(args)
	if stdout != nil {
		root.SetOut(stdout)
	}
	if stderr != nil {
		root.SetErr(stderr)
	}
	if stdin != nil {
		root.SetIn(stdin)
	}
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
			return errors.New("a command is required")
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newBusCmd())
	root.AddCommand(newWebCmd())
	addClientCommands(root)
	return root
}

func newBusCmd() *cobra.Command {
	var storeDir string
	var listenAddr string
	var tokenFile string
	var maxBody int
	var debug bool

	cmd := &cobra.Command{
		Use:           "bus",
		Short:         "Host conversations on one address",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat bus
  hotseat bus --store /var/lib/hotseat
  hotseat bus --store /var/lib/hotseat --listen 127.0.0.1:4727 --max-body 524288
  hotseat bus --store /var/lib/hotseat --listen 192.0.2.10:4727 --token-file /run/hotseat/token`,
		Long: `Listen for create, publish, read, wait, close, and list.

The database is ` + store.FileName + ` inside the store directory. When --store
is omitted the directory is $XDG_DATA_HOME/hotseat. An unset, empty, or relative
$XDG_DATA_HOME uses $HOME/.local/share/hotseat. A relative home directory is
refused and nothing is created. The default address is loopback
and requires no token. Any other address requires --token-file. A hostname is
resolved once, and the process listens on one address from that lookup. A
missing or empty token file does not listen and nothing is opened. A failed
bind opens nothing. The token is not logged. Clients send JSON to POST
/v1/<operation>. The process runs until it is signalled.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBus(cmd.Context(), storeDir, listenAddr, tokenFile, maxBody, newLogger(debug))
		},
	}
	cmd.Flags().StringVar(&storeDir, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
	cmd.Flags().StringVar(&listenAddr, "listen", bus.DefaultListen, "listen address")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the capability token; the token is not a flag")
	cmd.Flags().IntVar(&maxBody, "max-body", bus.DefaultMaxBody, "maximum message body size in bytes")
	cmd.Flags().BoolVar(&debug, "debug", false, "log wait and store detail to stderr")
	return cmd
}

// defaultStoreDir is the XDG data directory for this application.
// An unset, empty, or relative XDG_DATA_HOME is ignored.
// A relative home directory is refused. The directory is not created here.
func defaultStoreDir() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "hotseat"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("store directory: home directory is not absolute")
	}
	return filepath.Join(home, ".local", "share", "hotseat"), nil
}

func newLogger(debug bool) *slog.Logger {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// runBus binds the listener, opens the store, and serves until ctx is cancelled.
// The store is created only after the socket is bound and accepted. A refused
// address, a missing token, or a failed bind leaves no database.
func runBus(ctx context.Context, storeDir, addr, tokenFile string, maxBody int, log *slog.Logger) (err error) {
	if storeDir != "" {
		storeDir = filepath.Clean(storeDir)
	}
	if maxBody < 1 {
		return errors.New("max body must be a positive number of bytes")
	}
	bind, class, err := bus.ResolveListen(addr)
	if err != nil {
		return err
	}
	token, err := loadToken(class, tokenFile)
	if err != nil {
		return err
	}
	if storeDir == "" {
		storeDir, err = defaultStoreDir()
		if err != nil {
			return err
		}
	}
	if log == nil {
		log = newLogger(false)
	}

	ln, err := net.Listen(bus.ListenNetwork(bind), bind)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", bind, err)
	}
	if token == "" && !bus.AddrLoopback(ln.Addr()) {
		err = errors.New("non-loopback listen address requires a token file")
		if cerr := ln.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return err
	}

	st, err := store.Open(storeDir)
	if err != nil {
		if cerr := ln.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return err
	}
	defer func() {
		cerr := st.Close()
		if err == nil && cerr != nil {
			err = cerr
		}
	}()

	log.Info("listening", "addr", ln.Addr().String(), "store", st.Path(), "token_required", token != "")
	err = bus.Serve(ctx, ln, st, bus.Options{
		MaxBody: maxBody,
		Logger:  log,
		Token:   token,
	})
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		log.Info("stopped", "addr", ln.Addr().String())
		return nil
	}
	return err
}

// loadToken reads the capability file when a path is set.
// Only a non-loopback address keeps the secret. Loopback stays open with no token.
// Any other class fails closed. The bound socket is checked again before serving.
func loadToken(class bus.ListenClass, path string) (string, error) {
	switch class {
	case bus.ListenLoopback, bus.ListenRemote:
	default:
		return "", errors.New("listen address is not classified")
	}
	if path != "" {
		token, err := bus.ReadTokenFile(path)
		if err != nil {
			return "", err
		}
		if class == bus.ListenRemote {
			return token, nil
		}
		return "", nil
	}
	if class == bus.ListenRemote {
		return "", errors.New("non-loopback listen address requires a token file")
	}
	return "", nil
}
