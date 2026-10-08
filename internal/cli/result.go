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
)

// writeResult prints the call. Every client command uses this writer.
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
	if res.Conversation != nil {
		lw.header("conversation")
		lw.conversation(*res.Conversation)
	}
	if res.Conversations != nil {
		lw.header("conversations")
		for _, conv := range *res.Conversations {
			lw.conversation(conv)
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

func (lw *lineWriter) conversation(conv client.Conversation) {
	lw.plain("name", conv.Name)
	lw.plain("status", conv.Status)
}

func (lw *lineWriter) message(msg client.Message) {
	lw.header("message")
	lw.integer("seq", msg.Seq)
	lw.plain("time", msg.Time)
	lw.plain("from", msg.From)
	for _, name := range msg.To {
		lw.plain("to", name)
	}
	lw.plain("txid", msg.TxID)
	lw.framed("body", msg.Body)
}
