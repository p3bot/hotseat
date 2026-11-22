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

const statusLong = `Print what the bus says about one conversation as one text result. status
calls a bus that is already running and makes only read calls, in order:
lease get, list, members, records, and read. Records come before the
transcript, so the transcript holds every message a record names. It writes
nothing, launches nothing, and does not start the bus.

Lines, in order: outcome, conversation, task, ticket, and seat, one roster
line per name, state, then since and expires while state is running, then
member and member_status per member row as stored, newest, findings,
settled, fixed, and dropped, then discussing while state is running and a
finding is being worked on.

state is running while a conductor holds the conversation's lease, else
closed once the conversation is closed, else idle. It is a fact, not a
judgement of how a run ended. expires is for a reader to compare.

newest is the highest seq of any kind. findings counts the kept findings
and dropped the duplicates an aggregate dropped. settled counts kept
findings with a settle, dismissals included, and fixed the settled changes
that were fixed. Findings are worked one at a time in seq order, and
discussing is the first kept finding whose discussion has started and that
has no settle, or settled on a change with no fix. It is not printed when a
halt names that finding, since a halt ends every discussion. Each count is 0
for a task with no findings.

When the conversation has a stop record, done names the done that stopped
the run, then unsettled and unfixed lines and run_outcome (done, deadlock,
or unsettled) follow from the same rule as run's stop result, read from
before that done. status never decides which done stopped a run. When a
stopped by error is newer than that done, or there is no stop record, the
newest one prints as stop and stop_seq.

A read that answers other than ok prints its outcome and reason and nothing
else, and exits non-zero. A missing conversation is outcome refused with
reason conversation not found, and an unreachable bus is outcome
connection_failure. Otherwise status exits zero whatever the state or the
run outcome.

--token-file reads the shared capability token for a bus that is not on
loopback. Omit it for a loopback bus.`

func newStatusCmd() *cobra.Command {
	var address, conv string
	cmd := newClientCmd("status", "Show what is happening with one conversation", `  hotseat status --conversation job
  hotseat status --address 127.0.0.1:4727 --conversation job`, func(cmd *cobra.Command) error {
		token, err := tokenFromFlag(cmd)
		if err != nil {
			return err
		}
		st, err := run.ReadStatus(cmd.Context(), address, token, conv)
		if res, ok := run.FailedCall(err); ok {
			if err := writeResult(cmd.OutOrStdout(), res); err != nil {
				return err
			}
			return reportedFailure{}
		}
		if err != nil {
			return err
		}
		return writeStatus(cmd.OutOrStdout(), st)
	})
	cmd.Long = statusLong
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&conv, "conversation", "", "conversation name")
	markRequired(cmd, "conversation")
	return cmd
}

// writeStatus prints st in the documented order. The binding omits empty fields as list does.
func writeStatus(w io.Writer, st run.Status) error {
	lw := lineWriter{w: w}
	lw.plain("outcome", bus.OutcomeOK)
	lw.plain("conversation", st.Binding.Name)
	for _, field := range [][2]string{{"task", st.Binding.Task}, {"ticket", st.Binding.Ticket}, {"seat", st.Binding.Seat}} {
		if field[1] != "" {
			lw.plain(field[0], field[1])
		}
	}
	for _, name := range st.Binding.Roster {
		lw.plain("roster", name)
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
		lw.integer("done", st.Stop.Done)
		for _, seq := range st.Stop.Unsettled {
			lw.integer("unsettled", seq)
		}
		for _, seq := range st.Stop.Unfixed {
			lw.integer("unfixed", seq)
		}
		lw.plain("run_outcome", st.Stop.Outcome)
	}
	if st.StopError != nil {
		lw.plain("stop", st.StopError.Class)
		lw.integer("stop_seq", st.StopError.Seq)
	}
	return lw.err
}
