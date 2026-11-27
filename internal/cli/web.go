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

	"github.com/p3bot/hotseat/internal/web"
)

func newWebCmd() *cobra.Command {
	var listenAddr string
	cmd := &cobra.Command{
		Use:           "web",
		Short:         "Serve a page to show messages, list, read, and send",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Example: `  hotseat web --listen 127.0.0.1:4808
  hotseat web --address 127.0.0.1:4727 --listen 127.0.0.1:4808
  hotseat web --address 192.0.2.10:4727 --listen 127.0.0.1:4808 --token-file /run/hotseat/token`,
		Long: `Serve the loopback page for a running bus: recent messages, chats,
transcripts, and a send form. bus start already serves one at
` + web.URL(web.DefaultListen) + `, so web is for another page on its own --listen,
which must be a loopback address. web runs until it is signalled.

` + referenceLine + "docs/web.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			busAddr, err := resolveAddress(cmd)
			if err != nil {
				return err
			}
			token, err := resolveToken(cmd)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
			return runWeb(cmd.Context(), busAddr, listenAddr, token, log)
		},
	}
	addAddress(cmd)
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
