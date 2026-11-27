// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli is the hotseat command line. The root holds bus, chat, run, and web.
// The bus runs detached: bus start and bus stop manage that process, and
// bus status asks an address whether it can serve.
// chat holds the verbs on one chat, each naming the chat as its first argument.
// chat attend waits as owner and prints ask, error, and done.
// chat status reads one chat and prints its state, findings, and stop.
// run shells out to start for one chat. web serves the page for a running bus.
// Every chat verb and bus status exits by outcome through one table in exit.go.
// The bus process does not launch agents and it does not open connections to clients.
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
	"github.com/p3bot/hotseat/internal/web"
)

// Version is set at build time with -X.
var Version = "dev"

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
		Use:   "hotseat",
		Short: "Chat bus where AI agents work through one task together",
		Long: `hotseat keeps a durable local chat bus and drives AI agents through a task
on it. Start the bus with hotseat bus start, then drive a task with
hotseat run new.

` + referenceLine + "README.md",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return errors.New("a command is required")
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	// cobra lists any command named help, so the replacement has no name and the --help flag is the only help surface.
	root.SetHelpCommand(&cobra.Command{Hidden: true})
	root.AddCommand(newBusCmd(), newChatCmd(), newRunCmd(), newWebCmd())
	return root
}

// referenceLine opens the last line of every help page. A repository path follows it.
const referenceLine = "Reference: https://github.com/p3bot/hotseat/blob/main/"

func newBusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bus",
		Short:         "Run the detached chat bus",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat bus start
  hotseat bus stop
  hotseat bus status`,
		Long: `Start, stop, and check the chat bus. The bus runs detached from the
terminal. Its store directory holds ` + store.FileName + ` and ` + logName + `, and defaults
to $XDG_DATA_HOME/hotseat.

` + referenceLine + "docs/bus.md",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("choose start, stop, or status")
		},
	}
	cmd.AddCommand(newBusStartCmd(), newBusStopCmd(), newBusStatusCmd(), newBusServeCmd())
	return cmd
}

type busFlags struct {
	store   string
	maxBody int
	debug   bool
}

func addBusFlags(cmd *cobra.Command, f *busFlags) {
	cmd.Flags().StringVar(&f.store, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
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
  hotseat bus start --store /var/lib/hotseat --address 127.0.0.1:4727 --max-body 524288
  hotseat bus start --store /var/lib/hotseat --address 192.0.2.10:4727 --token-file /run/hotseat/token`,
		Long: `Start the bus detached from the terminal and print its pid, listen address,
store directory, and page URL. The page listens on 127.0.0.1 at the bus port
plus one, ` + web.URL(web.DefaultListen) + ` by default. When the bus for that store is
already running, start prints it and changes nothing.

A loopback --address needs no token. Any other address requires --token-file.

` + referenceLine + "docs/bus.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			address, err := resolveAddress(cmd)
			if err != nil {
				return err
			}
			tokenFile, _, err := resolveTokenFile(cmd)
			if err != nil {
				return err
			}
			return startDaemon(cmd.Context(), cmd.OutOrStdout(), f.store, address, tokenFile, f.maxBody, f.debug)
		},
	}
	addBusFlags(cmd, &f)
	addAddress(cmd)
	addTokenFile(cmd)
	return cmd
}

func newBusStopCmd() *cobra.Command {
	var storeDir string
	cmd := &cobra.Command{
		Use:           "stop",
		Short:         "Stop the bus",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Long: `Stop the bus and wait for it to exit. Already stopped is success.

With --store, stop only the bus for that directory. Without it, stop the bus
for the default store and any other hotseat bus on ` + bus.DefaultListen + `. A
process on that address that is not a hotseat bus is left running, and stop
exits 1.

` + referenceLine + "docs/bus.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("store") {
				return stopBus(cmd.Context(), storeDir)
			}
			return stopBare(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&storeDir, "store", "", "directory for the SQLite database (default $XDG_DATA_HOME/hotseat)")
	return cmd
}

func newBusStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Ask an address whether the bus can serve",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Long: `Ask the bus at --address whether it can serve, and print health as pass,
fail, or connection_failure. A bus on this machine also prints its pid and
store directory. pass exits 0, and fail and connection_failure exit 4.

` + referenceLine + "docs/bus.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			address, err := resolveAddress(cmd)
			if err != nil {
				return err
			}
			return statusBus(cmd.Context(), cmd.OutOrStdout(), address)
		},
	}
	addAddress(cmd)
	return cmd
}

// newBusServeCmd is the detached child bus start execs. It takes the resolved --listen and reads no variable.
func newBusServeCmd() *cobra.Command {
	var f busFlags
	var listen, tokenFile string
	cmd := &cobra.Command{
		Use:           "serve",
		Hidden:        true,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serveDetached(cmd.Context(), f.store, listen, tokenFile, f.maxBody, f.debug)
		},
	}
	addBusFlags(cmd, &f)
	cmd.Flags().StringVar(&listen, "listen", bus.DefaultListen, "listen address")
	cmd.Flags().StringVar(&tokenFile, flagTokenFile, "", "file holding the capability token")
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
