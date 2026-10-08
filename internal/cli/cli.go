// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is the hotseat command line.
// The bus runs detached: start, stop, and status manage that process.
// It does not launch agents and it does not open connections to clients.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	cmd := &cobra.Command{
		Use:           "bus",
		Short:         "Run the detached conversation bus",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat bus start
  hotseat bus stop
  hotseat bus status`,
		Long: `Start, stop, and check the conversation bus. The bus process is detached
from the terminal. Its log is ` + logName + ` in the store directory.

The database is ` + store.FileName + ` inside the store directory. When --store
is omitted the directory is $XDG_DATA_HOME/hotseat. An unset, empty, or relative
$XDG_DATA_HOME uses $HOME/.local/share/hotseat. A relative home directory is
refused and nothing is created. hotseat bus start leaves the bus running until
hotseat bus stop.`,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("choose start, stop, or status")
		},
	}
	cmd.AddCommand(newBusStartCmd(), newBusStopCmd(), newBusStatusCmd(), newBusServeCmd())
	return cmd
}

type busFlags struct {
	store     string
	listen    string
	tokenFile string
	maxBody   int
	debug     bool
}

func addServeFlags(cmd *cobra.Command, f *busFlags) {
	cmd.Flags().StringVar(&f.store, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
	cmd.Flags().StringVar(&f.listen, "listen", bus.DefaultListen, "listen address")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "", "file holding the capability token; the token is not a flag")
	cmd.Flags().IntVar(&f.maxBody, "max-body", bus.DefaultMaxBody, "maximum message body size in bytes")
	cmd.Flags().BoolVar(&f.debug, "debug", false, "log at debug level to "+logName)
}

func newBusStartCmd() *cobra.Command {
	var f busFlags
	cmd := &cobra.Command{
		Use:           "start",
		Short:         "Start the bus in the background",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat bus start
  hotseat bus start --store /var/lib/hotseat
  hotseat bus start --store /var/lib/hotseat --listen 127.0.0.1:4727 --max-body 524288
  hotseat bus start --store /var/lib/hotseat --listen 192.0.2.10:4727 --token-file /run/hotseat/token`,
		Long: `Listen for create, publish, read, wait, close, and list. The process runs
in a new session with no controlling terminal. Stdin is discarded. Stdout and
stderr append to ` + logName + ` in the store directory. start returns after the
listener is bound and the database is open, and prints the pid, the listen
address, and the store path. A second start, while that bus is running, prints
the running process and does not change it.

The database is ` + store.FileName + ` inside the store directory. When --store
is omitted the directory is $XDG_DATA_HOME/hotseat. An unset, empty, or relative
$XDG_DATA_HOME uses $HOME/.local/share/hotseat. A relative home directory is
refused and nothing is created. The default address is loopback and requires no
token. Any other address requires --token-file. A hostname is resolved once,
and the process listens on one address from that lookup. A missing or empty
token file does not listen and nothing is opened. A failed bind opens nothing.
The token is not logged. Clients send JSON to POST /<operation>.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return startDaemon(cmd.Context(), cmd.OutOrStdout(), f.store, f.listen, f.tokenFile, f.maxBody, f.debug)
		},
	}
	addServeFlags(cmd, &f)
	return cmd
}

func newBusStopCmd() *cobra.Command {
	var storeDir string
	cmd := &cobra.Command{
		Use:           "stop",
		Short:         "Stop the bus for a store",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Long: `Send SIGTERM to the bus recorded for this store and return after that
process has exited and ` + store.LockName + ` is released. Already stopped is
success. stop does not signal any other process.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return stopBus(cmd.Context(), storeDir)
		},
	}
	cmd.Flags().StringVar(&storeDir, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
	return cmd
}

func newBusStatusCmd() *cobra.Command {
	var storeDir string
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Report whether the bus is running",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Long: `Print the pid, the listen address, and the store path when the bus is
running. When it is not running, exit 1 and report that.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return statusBus(cmd.OutOrStdout(), storeDir)
		},
	}
	cmd.Flags().StringVar(&storeDir, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
	return cmd
}

func newBusServeCmd() *cobra.Command {
	var f busFlags
	cmd := &cobra.Command{
		Use:           "serve",
		Hidden:        true,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveDetached(cmd.Context(), f.store, f.listen, f.tokenFile, f.maxBody, f.debug)
		},
	}
	addServeFlags(cmd, &f)
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
	ln, st, token, err := openBus(storeDir, addr, tokenFile, maxBody)
	if err != nil {
		return err
	}
	defer func() {
		cerr := st.Close()
		if err == nil && cerr != nil {
			err = cerr
		}
	}()
	return serveListener(ctx, ln, st, token, maxBody, log)
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
