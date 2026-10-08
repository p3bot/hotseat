// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/web"
)

func newWebCmd() *cobra.Command {
	var busAddr, listenAddr string
	cmd := &cobra.Command{
		Use:           "web",
		Short:         "Serve a page to list, read, and publish",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat web
  hotseat web --address 127.0.0.1:4727 --listen 127.0.0.1:4728
  hotseat web --address 192.0.2.10:4727 --token-file /run/hotseat/token`,
		Long: `Serve a page that lists conversations, reads a transcript, and publishes.

The page calls a bus that is already running. It does not open the database.
The default bus address is ` + bus.DefaultListen + `. The default page address is ` + web.DefaultListen + `.
--token-file reads the capability token for a bus that is not on loopback.
The token is not a flag and the page does not receive it. Omit the flag for a loopback bus.
The page listens on loopback. Any other listen address is refused.
The page answers for the address it listens on. On loopback, localhost with that port is the same address.
The person at the page supplies from, to, the body, and the cursor.
An empty transaction id is filled in the page when Publish is submitted.
The server does not store the cursor or the transaction id and does not invent one.
An ok publish clears the field. Any other result leaves the posted id there.
The process runs until it is signalled.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := tokenFromFlag(cmd)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
			return runWeb(cmd.Context(), busAddr, listenAddr, token, log)
		},
	}
	addAddress(cmd, &busAddr)
	cmd.Flags().StringVar(&listenAddr, "listen", web.DefaultListen, "page address")
	addTokenFile(cmd)
	return cmd
}

// runWeb listens until ctx is cancelled. token is already loaded.
// An empty token omits the header. The bus decides whether the call is allowed.
// The page process does not open a store.
// The bus address is checked before the socket exists.
// Serve closes a socket that is not loopback.
func runWeb(ctx context.Context, busAddr, listenAddr, token string, log *slog.Logger) error {
	if _, _, err := net.SplitHostPort(busAddr); err != nil {
		return fmt.Errorf("bus address: %w", err)
	}
	if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	return web.Serve(ctx, ln, busAddr, token, log)
}
