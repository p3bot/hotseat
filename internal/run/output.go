// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// lockedWriter serialises writes. Member processes share one stderr.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// newEventLog writes one engine event per line to w: a local wall-clock time, the event, and its fields.
// A nil w discards every event.
func newEventLog(w io.Writer) *slog.Logger {
	if w == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) > 0 {
			return a
		}
		switch a.Key {
		case slog.LevelKey:
			return slog.Attr{}
		case slog.MessageKey:
			a.Key = "event"
		}
		return a
	}}))
}

// prefixLines writes each line of text to w with the tool name in front, in one write so the lines stay adjacent.
func prefixLines(w io.Writer, tool string, text []byte) error {
	if len(text) == 0 {
		return nil
	}
	var b bytes.Buffer
	for line := range strings.Lines(string(text)) {
		b.WriteString(tool)
		b.WriteString(": ")
		b.WriteString(strings.TrimSuffix(line, "\n"))
		b.WriteByte('\n')
	}
	_, err := w.Write(b.Bytes())
	return err
}
