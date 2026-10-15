// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
)

func TestConversationCodeUsesTkAlphabets(t *testing.T) {
	if LetterAlphabet != "abcdefghjkmnpqrstuvwxyz" {
		t.Fatalf("letter alphabet %q", LetterAlphabet)
	}
	if ShortIDAlphabet != "abcdefghjkmnpqrstuvwxyz23456789" {
		t.Fatalf("short alphabet %q", ShortIDAlphabet)
	}
	code, err := mintCode(bytes.NewReader([]byte{0, 1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 4 {
		t.Fatalf("code %q", code)
	}
	if !strings.ContainsRune(LetterAlphabet, rune(code[0])) {
		t.Fatalf("first %q", code)
	}
	for i := 1; i < len(code); i++ {
		if !strings.ContainsRune(ShortIDAlphabet, rune(code[i])) {
			t.Fatalf("code %q", code)
		}
	}
}

func TestTaskSlugDropsCategoryAndHotseat(t *testing.T) {
	slug, err := taskSlug("tasks:hotseat/review/ticket")
	if err != nil || slug != "review-ticket" {
		t.Fatalf("slug %q %v", slug, err)
	}
	design, err := taskSlug("tasks:design")
	if err != nil || design != "design" {
		t.Fatalf("design %q %v", design, err)
	}
	if got := bareConversation(slug, "foo-x234"); got != "review-ticket-foo-x234" {
		t.Fatalf("bare %s", got)
	}
	if got := bareConversation("design", ""); got != "design" {
		t.Fatalf("bare design %s", got)
	}
}

func TestNameOver64NamesTheLimit(t *testing.T) {
	err := checkName(strings.Repeat("a", bus.MaxNameLen+1))
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatalf("err %v", err)
	}
}

func TestRosterRejectsUppercaseAndDuplicate(t *testing.T) {
	if _, err := parseAgents("Alpha=hotseat/grok:m"); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatal(err)
	}
	if _, err := parseAgents("alpha=hotseat/grok:m alpha=hotseat/claude:m"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal(err)
	}
	roster, err := parseAgents("bravo=hotseat/claude:m alpha=hotseat/grok:n")
	if err != nil {
		t.Fatal(err)
	}
	if rosterNames(roster)[0] != "bravo" || defaultSeat(roster) != "alpha" {
		t.Fatalf("%+v seat %s", roster, defaultSeat(roster))
	}
}

func TestLocatorIgnoresTrailingNewlineAndRejectsBlankLine(t *testing.T) {
	keys, err := locatorKeys("session_id\n")
	if err != nil || len(keys) != 1 || keys[0] != "session_id" {
		t.Fatalf("keys %q %v", keys, err)
	}
	if _, err := locatorKeys("sessionId\n\n"); err == nil {
		t.Fatal("blank line accepted")
	}
	if _, err := locatorKeys(""); err == nil {
		t.Fatal("empty accepted")
	}
	id, ok := sessionID([]byte(`{"result":{"0":"abc"}}`), []string{"result", "0"})
	if !ok || id != "abc" {
		t.Fatalf("id %q %v", id, ok)
	}
	if _, ok := sessionID([]byte(`{"sessionId":1}`), []string{"sessionId"}); ok {
		t.Fatal("number accepted")
	}
}

func TestInstructionTrailerOrder(t *testing.T) {
	body := instructionBody("Review\n", "alpha", "foo-x234", []string{"tasks:hotseat/review/extra"}, "focus")
	want := "Review\n\nseat: alpha\nticket: foo-x234\nprompts:\n- tasks:hotseat/review/extra\ncustom:\nfocus\n"
	if body != want {
		t.Fatalf("body %q", body)
	}
	if len(body) > bus.DefaultMaxBody {
		t.Fatal("fixture exceeds the bus maximum")
	}
}

func TestDefaults(t *testing.T) {
	if DefaultMaxTurns != 500 {
		t.Fatalf("turns %d", DefaultMaxTurns)
	}
	if DefaultProcessLimit != 8*time.Hour {
		t.Fatalf("limit %s", DefaultProcessLimit)
	}
	if (Config{}).maxTurns() != 500 || (Config{}).timeout() != 8*time.Hour {
		t.Fatal("unset defaults")
	}
	if (Config{Interactive: true, Timeout: time.Second, TimeoutSet: true}).timeout() != 0 {
		t.Fatal("interactive kept a process limit")
	}
}

func TestZeroTimeoutIsRejected(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		_, err := newEngine(context.Background(), Config{
			Dir:        t.TempDir(),
			Timeout:    d,
			TimeoutSet: true,
		}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "positive") {
			t.Fatalf("timeout %s err %v", d, err)
		}
	}
}
