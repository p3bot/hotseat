// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"testing"

	"github.com/p3bot/hotseat/internal/store"
)

func msg(seq int64, from string, to []string, body string) store.Message {
	return store.Message{Seq: seq, From: from, To: to, Body: body, TxID: body}
}

func TestDecideWait(t *testing.T) {
	msgs := []store.Message{
		msg(1, "alice", nil, "note"),
		msg(2, "alice", []string{"carol"}, "side"),
		msg(3, "alice", []string{"bob"}, "ping"),
		msg(4, "bob", []string{"bob"}, "self"),
		msg(5, "alice", []string{"all"}, "broadcast"),
	}

	ok, pending := decideWait(msgs, "bob", true, nil, false)
	if pending || ok.Outcome != OutcomeOK || ok.MatchSeq != 3 || len(ok.Messages) != 3 {
		t.Fatalf("named span: pending=%v outcome=%s match=%d len=%d", pending, ok.Outcome, ok.MatchSeq, len(ok.Messages))
	}
	if ok.Messages[0].Seq != 1 || ok.Messages[2].Body != "ping" {
		t.Fatalf("span = %+v", ok.Messages)
	}

	rest, pending := decideWait(msgs[3:], "bob", true, nil, false)
	if pending || rest.MatchSeq != 5 || len(rest.Messages) != 2 {
		t.Fatalf("later named: pending=%v match=%d len=%d", pending, rest.MatchSeq, len(rest.Messages))
	}

	one, pending := decideWait(msgs, "", false, nil, false)
	if pending || one.MatchSeq != 1 || len(one.Messages) != 1 {
		t.Fatalf("unnamed: pending=%v match=%d len=%d", pending, one.MatchSeq, len(one.Messages))
	}

	none, pending := decideWait(nil, "bob", true, nil, false)
	if !pending {
		t.Fatal("open named with no match should block")
	}
	_ = none

	staying, pending := decideWait(msgs[:2], "bob", true, nil, false)
	if !pending || staying.Outcome != "" {
		t.Fatalf("no match stays pending: %+v pending=%v", staying, pending)
	}

	nothing, pending := decideWait(nil, "", false, nil, false)
	if !pending || nothing.Outcome != "" {
		t.Fatalf("unnamed with nothing stored stays pending: %+v", nothing)
	}

	matched, pending := decideWait(msgs[:3], "bob", true, nil, false)
	if pending || matched.Outcome != OutcomeOK || matched.MatchSeq != 3 {
		t.Fatalf("stored match: %+v", matched)
	}

	say := msg(1, "alice", []string{"bob"}, "earlier")
	say.Kind = "say"
	turn := msg(2, "alice", []string{"bob"}, "phase")
	turn.Kind = "turn"
	filtered, pending := decideWait([]store.Message{say, turn}, "", false, []string{"turn"}, true)
	if pending || filtered.Outcome != OutcomeOK || filtered.MatchSeq != 2 || len(filtered.Messages) != 1 || filtered.Messages[0].Kind != "turn" {
		t.Fatalf("unnamed kinds: pending=%v %+v", pending, filtered)
	}
}

func TestParseTo(t *testing.T) {
	names, err := parseTo([]byte(`["b","a"]`))
	if err != nil || len(names) != 2 || names[0] != "b" || names[1] != "a" {
		t.Fatalf("order: %v %v", names, err)
	}
	if _, err := parseTo([]byte(`["all","bob"]`)); err == nil {
		t.Fatal("all combined with a name was accepted")
	}
	if _, err := parseTo([]byte(`"all"`)); err == nil {
		t.Fatal("string all was accepted")
	}
	empty, err := parseTo([]byte(`[]`))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
	if _, err := parseTo(nil); err == nil {
		t.Fatal("missing to was accepted")
	}
}

func TestSelectRead(t *testing.T) {
	msgs := []store.Message{
		msg(1, "alice", nil, "note"),
		msg(2, "alice", []string{"bob"}, "ping"),
		msg(3, "bob", []string{"bob"}, "self"),
		msg(4, "alice", []string{"all"}, "broadcast"),
	}
	got := selectRead(msgs, "bob", true, nil, false, 10)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 4 {
		t.Fatalf("named read = %+v", got)
	}
	limited := selectRead(msgs, "", false, nil, false, 2)
	if len(limited) != 2 || limited[0].Seq != 1 || limited[1].Seq != 2 {
		t.Fatalf("limited read = %+v", limited)
	}
}
