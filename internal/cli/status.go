// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/run"
)

func newStatusCmd() *cobra.Command {
	cmd := newChatVerb("status <name>", "Show what is happening with one chat", `Print one chat's setup, state, member statuses, finding counts, and stop,
from bus reads alone. status writes nothing. state is running while a run
holds the chat, closed once the chat is closed, and idle otherwise.

status exits 0 whatever the state. A failed read prints its outcome and
reason and exits by that outcome, so a missing chat exits 2.`, `  hotseat chat status job
  hotseat chat --address 127.0.0.1:4727 status job`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		address, err := resolveAddress(cmd)
		if err != nil {
			return err
		}
		token, err := resolveToken(cmd)
		if err != nil {
			return err
		}
		st, err := run.ReadStatus(cmd.Context(), address, token, args[0])
		if res, ok := run.FailedCall(err); ok {
			if err := writeResult(cmd.OutOrStdout(), res); err != nil {
				return err
			}
			return outcomeExit(res.Outcome)
		}
		if err != nil {
			return err
		}
		return writeStatus(cmd.OutOrStdout(), st)
	})
	return cmd
}

// writeStatus prints st in the documented order. The binding omits empty fields as list does.
func writeStatus(w io.Writer, st run.Status) error {
	lw := lineWriter{w: w}
	lw.plain("outcome", bus.OutcomeOK)
	lw.plain("chat", st.Binding.Name)
	for _, field := range [][2]string{{"task", st.Binding.Task}, {"ticket", st.Binding.Ticket}, {"seat", st.Binding.Seat}} {
		if field[1] != "" {
			lw.plain(field[0], field[1])
		}
	}
	for _, r := range st.Binding.Roster {
		lw.plain("roster", r.String())
	}
	if st.Binding.Permission != "" {
		lw.plain("permission", st.Binding.Permission)
	}
	lw.plain("state", st.State)
	if st.State == run.StateRunning {
		lw.plain("since", st.Since)
		lw.plain("expires", st.Expires)
	}
	for _, m := range st.Members {
		lw.plain("member", m.Name)
		lw.plain("member_status", m.Status)
	}
	lw.integer("newest", st.Newest)
	lw.integer("findings", int64(st.Findings))
	lw.integer("settled", int64(st.Settled))
	lw.integer("fixed", int64(st.Fixed))
	lw.integer("dropped", int64(st.Dropped))
	if st.Discussing != nil {
		lw.integer("discussing", *st.Discussing)
	}
	if st.Stop != nil {
		lw.stopLines(st.Stop)
		lw.plain("run_outcome", st.Stop.Outcome)
	}
	if st.StopError != nil {
		lw.plain("stop", st.StopError.Class)
		lw.integer("stop_seq", st.StopError.Seq)
	}
	return lw.err
}
