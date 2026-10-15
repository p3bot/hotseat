// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// statusWriter prints key: value lines.
// A value that contains a newline is a byte count and then those bytes.
type statusWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (s *statusWriter) line(key, value string) {
	s.record([2]string{key, value})
}

// record writes one event under one lock so its lines stay adjacent.
func (s *statusWriter) record(fields ...[2]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, field := range fields {
		s.write(field[0], field[1])
		if s.err != nil {
			return
		}
	}
}

func (s *statusWriter) write(key, value string) {
	if s.err != nil || s.w == nil {
		return
	}
	if strings.Contains(value, "\n") {
		if _, err := fmt.Fprintf(s.w, "%s <<%d\n", key, len(value)); err != nil {
			s.err = err
			return
		}
		if _, err := io.WriteString(s.w, value); err != nil {
			s.err = err
			return
		}
		_, s.err = io.WriteString(s.w, "\n")
		return
	}
	if value == "" {
		_, s.err = fmt.Fprintf(s.w, "%s:\n", key)
		return
	}
	_, s.err = fmt.Fprintf(s.w, "%s: %s\n", key, value)
}

func (s *statusWriter) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

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
