// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/run"
)

// writeResult prints the call. Every chat verb uses this writer.
// A field on one line is "key: value", including a value that contains "<<".
// A value that holds a newline is a byte count and then those bytes, and a
// message body always is. The count is what keeps an empty body, a body of
// more than one line, and a line that looks like a field inside the value.
// The newline after a counted value is a separator, not part of the value.
func writeResult(w io.Writer, res client.Result) error {
	lw := lineWriter{w: w}
	lw.plain("outcome", res.Outcome)
	if res.Reason != "" {
		lw.plain("reason", res.Reason)
	}
	lw.boolean("already_stored", res.AlreadyStored)
	lw.boolean("already_existed", res.AlreadyExisted)
	if res.MatchSeq != nil {
		lw.integer("match_seq", *res.MatchSeq)
	}
	lw.boolean("more", res.More)
	if res.Chat != nil {
		lw.header("chat")
		lw.chat(*res.Chat)
	}
	if res.Chats != nil {
		lw.header("chats")
		for _, c := range *res.Chats {
			lw.chat(c)
		}
	}
	if res.Message != nil {
		lw.message(*res.Message)
	}
	if res.Messages != nil {
		lw.header("messages")
		for _, msg := range *res.Messages {
			lw.message(msg)
		}
	}
	if res.Member != nil {
		lw.member(*res.Member)
	}
	if res.Members != nil {
		lw.header("members")
		for _, m := range *res.Members {
			lw.member(m)
		}
	}
	return lw.err
}

type lineWriter struct {
	w   io.Writer
	err error
}

func (lw *lineWriter) plain(key, value string) {
	if lw.err != nil {
		return
	}
	if strings.Contains(value, "\n") {
		lw.framed(key, value)
		return
	}
	if value == "" {
		_, lw.err = fmt.Fprintf(lw.w, "%s:\n", key)
		return
	}
	_, lw.err = fmt.Fprintf(lw.w, "%s: %s\n", key, value)
}

func (lw *lineWriter) framed(key, value string) {
	if lw.err != nil {
		return
	}
	if _, err := fmt.Fprintf(lw.w, "%s <<%d\n", key, len(value)); err != nil {
		lw.err = err
		return
	}
	if _, err := io.WriteString(lw.w, value); err != nil {
		lw.err = err
		return
	}
	_, lw.err = io.WriteString(lw.w, "\n")
}

func (lw *lineWriter) header(key string) {
	if lw.err != nil {
		return
	}
	_, lw.err = fmt.Fprintf(lw.w, "%s:\n", key)
}

func (lw *lineWriter) boolean(key string, v *bool) {
	if v == nil {
		return
	}
	word := "false"
	if *v {
		word = "true"
	}
	lw.plain(key, word)
}

func (lw *lineWriter) integer(key string, n int64) {
	lw.plain(key, strconv.FormatInt(n, 10))
}

// stopLines prints the done a stop record names and that done's unsettled and unfixed findings, as run and status both do.
func (lw *lineWriter) stopLines(s *run.StopResult) {
	lw.integer("done", s.Done)
	for _, seq := range s.Unsettled {
		lw.integer("unsettled", seq)
	}
	for _, seq := range s.Unfixed {
		lw.integer("unfixed", seq)
	}
}

func (lw *lineWriter) chat(c client.Chat) {
	lw.plain("name", c.Name)
	// A caller that only has the name must not print a blank status.
	if c.Status != "" {
		lw.plain("status", c.Status)
	}
	if c.Task != "" {
		lw.plain("task", c.Task)
	}
	if c.Ticket != "" {
		lw.plain("ticket", c.Ticket)
	}
	if c.Seat != "" {
		lw.plain("seat", c.Seat)
	}
	if c.Directory != "" {
		lw.plain("directory", c.Directory)
	}
	for _, prompt := range c.Prompts {
		lw.plain("prompts", prompt)
	}
	if c.Custom != "" {
		lw.plain("custom", c.Custom)
	}
	for _, r := range c.Roster {
		lw.plain("roster", r.String())
	}
	if c.Permission != "" {
		lw.plain("permission", c.Permission)
	}
}

func (lw *lineWriter) message(msg client.Message) {
	lw.header("message")
	lw.integer("seq", msg.Seq)
	lw.plain("time", msg.Time)
	lw.plain("from", msg.From)
	lw.plain("kind", msg.Kind)
	if msg.Re != nil {
		lw.integer("re", *msg.Re)
	}
	for _, name := range msg.To {
		lw.plain("to", name)
	}
	lw.plain("txid", msg.TxID)
	lw.framed("body", msg.Body)
}

// member prints the public row. A pid or start token is never printed.
func (lw *lineWriter) member(m client.Member) {
	lw.header("member")
	lw.plain("name", m.Name)
	lw.plain("status", m.Status)
	lw.plain("launched", m.Launched)
	lw.plain("joined", m.Joined)
}
