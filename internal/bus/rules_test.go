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
	return store.Message{Seq: seq, From: from, To: to, Body: body, Key: body}
}

func TestDecideWait(t *testing.T) {
	msgs := []store.Message{
		msg(1, "alice", nil, "note"),
		msg(2, "alice", []string{"carol"}, "side"),
		msg(3, "alice", []string{"bob"}, "ping"),
		msg(4, "bob", []string{"bob"}, "self"),
		msg(5, "alice", []string{"all"}, "broadcast"),
	}

	ok, pending := decideWait(store.StatusOpen, msgs, "bob", true)
	if pending || ok.Outcome != OutcomeOK || ok.MatchSeq != 3 || len(ok.Messages) != 3 {
		t.Fatalf("named span: pending=%v outcome=%s match=%d len=%d", pending, ok.Outcome, ok.MatchSeq, len(ok.Messages))
	}
	if ok.Messages[0].Seq != 1 || ok.Messages[2].Body != "ping" {
		t.Fatalf("span = %+v", ok.Messages)
	}

	rest, pending := decideWait(store.StatusOpen, msgs[3:], "bob", true)
	if pending || rest.MatchSeq != 5 || len(rest.Messages) != 2 {
		t.Fatalf("later named: pending=%v match=%d len=%d", pending, rest.MatchSeq, len(rest.Messages))
	}

	one, pending := decideWait(store.StatusOpen, msgs, "", false)
	if pending || one.MatchSeq != 1 || len(one.Messages) != 1 {
		t.Fatalf("unnamed: pending=%v match=%d len=%d", pending, one.MatchSeq, len(one.Messages))
	}

	none, pending := decideWait(store.StatusOpen, nil, "bob", true)
	if !pending {
		t.Fatal("open named with no match should block")
	}
	_ = none

	closedTail, pending := decideWait(store.StatusClosed, msgs[:2], "bob", true)
	if pending || closedTail.Outcome != OutcomeClosed || len(closedTail.Messages) != 2 || closedTail.MatchSeq != 0 {
		t.Fatalf("closed named tail: %+v pending=%v", closedTail, pending)
	}

	closedEmpty, pending := decideWait(store.StatusClosed, nil, "", false)
	if pending || closedEmpty.Outcome != OutcomeClosed || len(closedEmpty.Messages) != 0 {
		t.Fatalf("closed unnamed: %+v", closedEmpty)
	}

	// A stored match after close is ok, not closed.
	matched, pending := decideWait(store.StatusClosed, msgs[:3], "bob", true)
	if pending || matched.Outcome != OutcomeOK || matched.MatchSeq != 3 {
		t.Fatalf("match before close: %+v", matched)
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
	got := selectRead(msgs, "bob", true, 10)
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 4 {
		t.Fatalf("named read = %+v", got)
	}
	limited := selectRead(msgs, "", false, 2)
	if len(limited) != 2 || limited[0].Seq != 1 || limited[1].Seq != 2 {
		t.Fatalf("limited read = %+v", limited)
	}
}
