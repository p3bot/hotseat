// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/p3bot/hotseat/internal/bus"
)

const (
	modeParallel = "parallel"
	modeSerial   = "serial"
)

type agent struct {
	name   string
	module string
	model  string
}

func parseAgents(raw string) ([]agent, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil, errors.New("a roster is required")
	}
	out := make([]agent, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		name, rest, ok := strings.Cut(field, "=")
		module, model, both := strings.Cut(rest, ":")
		if !ok || !both || name == "" || module == "" || model == "" {
			return nil, fmt.Errorf("roster entry %q must be name=module:model", field)
		}
		if name != strings.ToLower(name) {
			return nil, fmt.Errorf("roster name %s must be lowercase", name)
		}
		if !validName(name) {
			return nil, fmt.Errorf("roster name %s must be 1 to %d characters from ASCII letters, digits, '.', '_', and '-'", name, bus.MaxNameLen)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("duplicate roster name %s", name)
		}
		seen[name] = struct{}{}
		out = append(out, agent{name: name, module: module, model: model})
	}
	return out, nil
}

func validName(s string) bool {
	if len(s) < 1 || len(s) > bus.MaxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

func defaultSeat(roster []agent) string {
	seat := roster[0].name
	for _, a := range roster[1:] {
		if a.name < seat {
			seat = a.name
		}
	}
	return seat
}

func rosterNames(roster []agent) []string {
	names := make([]string, len(roster))
	for i, a := range roster {
		names[i] = a.name
	}
	return names
}

func nameIn(roster []agent, name string) bool {
	for _, a := range roster {
		if a.name == name {
			return true
		}
	}
	return false
}

// taskSlug is the conversation stem of a start id line.
// A leading tasks: category is dropped, then one leading hotseat path segment.
func taskSlug(idLine string) (string, error) {
	s := strings.TrimSpace(idLine)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return "", errors.New("task id must be one line")
	}
	s = strings.TrimPrefix(s, "tasks:")
	switch {
	case s == "hotseat":
		s = ""
	case strings.HasPrefix(s, "hotseat/"):
		s = strings.TrimPrefix(s, "hotseat/")
	}
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		return "", errors.New("task id has no conversation name")
	}
	return s, nil
}

// bareConversation leads with the ticket id so one work item sorts together.
func bareConversation(slug, ticket string) string {
	if ticket == "" {
		return slug
	}
	return ticket + "-" + slug
}

func nameLimit(name string) error {
	return fmt.Errorf("conversation name %q exceeds the %d character limit", name, bus.MaxNameLen)
}

func checkName(name string) error {
	if len(name) < 1 || len(name) > bus.MaxNameLen {
		return nameLimit(name)
	}
	if !validName(name) {
		return fmt.Errorf("conversation name %q must be 1 to %d characters from ASCII letters, digits, '.', '_', and '-'", name, bus.MaxNameLen)
	}
	return nil
}

func mintCode(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var buf [conversationCode]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return "", err
	}
	out := make([]byte, conversationCode)
	out[0] = LetterAlphabet[int(buf[0])%len(LetterAlphabet)]
	for i := 1; i < conversationCode; i++ {
		out[i] = ShortIDAlphabet[int(buf[i])%len(ShortIDAlphabet)]
	}
	return string(out), nil
}

func instructionBody(text, seat, ticket string, prompts []string, custom string) string {
	var b strings.Builder
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString("seat: ")
	b.WriteString(seat)
	b.WriteByte('\n')
	if ticket != "" {
		b.WriteString("ticket: ")
		b.WriteString(ticket)
		b.WriteByte('\n')
	}
	if len(prompts) > 0 {
		b.WriteString("prompts:\n")
		for _, p := range prompts {
			b.WriteString("- ")
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	if custom != "" {
		b.WriteString("custom:\n")
		b.WriteString(custom)
		if !strings.HasSuffix(custom, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func parseMode(body string) (mode, phase string, ok bool) {
	line, rest, found := strings.Cut(body, "\n")
	if !found {
		line = body
		rest = ""
	}
	switch strings.TrimSpace(line) {
	case "mode: " + modeParallel:
		return modeParallel, rest, true
	case "mode: " + modeSerial:
		return modeSerial, rest, true
	default:
		return "", "", false
	}
}

// locatorKeys reads one object key per line.
// The final newline ends the last key. An empty line is a missing locator.
// A line of digits is a key, not an array index.
func locatorKeys(out string) ([]string, error) {
	if out == "" {
		return nil, errors.New("missing session id")
	}
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil, errors.New("missing session id")
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if line == "" {
			return nil, errors.New("missing session id")
		}
	}
	return lines, nil
}

func sessionID(raw []byte, keys []string) (string, bool) {
	if len(keys) == 0 || len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	for _, key := range keys {
		obj, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		v, ok = obj[key]
		if !ok {
			return "", false
		}
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func splitTicket(id string) (string, error) {
	scope, short, ok := strings.Cut(id, "-")
	if !ok || scope == "" || short == "" || strings.Contains(short, "-") {
		return "", fmt.Errorf("ticket %s is not a scope id", id)
	}
	return scope, nil
}

func scopeRoots(out string) (map[string]string, error) {
	roots := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		col := strings.Split(line, "\t")
		if len(col) < 3 || col[0] == "" {
			return nil, errors.New("tk scope list is not name, ticket directory, code-root, mode")
		}
		roots[col[0]] = col[2]
	}
	return roots, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
