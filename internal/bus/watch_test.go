// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

// blockSignal returns OnBlock options and the channel each park sends on.
func blockSignal() (Options, chan struct{}) {
	blocked := make(chan struct{}, 16)
	return Options{OnBlock: func(string, string) { blocked <- struct{}{} }}, blocked
}

func awaitBlock(t *testing.T, blocked <-chan struct{}) {
	t.Helper()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not block")
	}
}

// noBlock fails when the watch parks again, which means it was woken.
func noBlock(t *testing.T, blocked <-chan struct{}) {
	t.Helper()
	select {
	case <-blocked:
		t.Fatal("watch was woken")
	case <-time.After(100 * time.Millisecond):
	}
}

func watchAsync(t *testing.T, base string, body map[string]any) <-chan wire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	out := make(chan wire, 1)
	go func() {
		res, _, _, err := post(ctx, base, PathWatch, body)
		if err != nil {
			res.Outcome = err.Error()
		}
		out <- res
	}()
	return out
}

func receive(t *testing.T, ch <-chan wire) wire {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not return")
		return wire{}
	}
}

func currentRev(t *testing.T, base string) uint64 {
	t.Helper()
	res := mustPost(t, base, PathWatch, map[string]any{})
	if res.Outcome != OutcomeOK || res.Rev == nil {
		t.Fatalf("rev watch = %+v", res)
	}
	return *res.Rev
}

func TestWatchWithoutRevReturnsAtOnceWithTheCounter(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res, raw := mustRaw(t, base, PathWatch, map[string]any{"deadline": "5s"})
	if res.Outcome != OutcomeOK || res.Rev == nil {
		t.Fatalf("empty bus = %s", raw)
	}
	if res.More == nil || *res.More || messageLen(res) != 0 {
		t.Fatalf("rev-only watch returned messages: %s", raw)
	}
	start := *res.Rev
	create(t, base, "job")
	if got := currentRev(t, base); got != start+1 {
		t.Fatalf("rev after create = %d, want %d", got, start+1)
	}
}

func TestWatchWakesOnPublishToAnyConversation(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "alpha")
	create(t, base, "beta")
	publish(t, base, "beta", "alice", nil, "first", "1")
	rev := currentRev(t, base)

	got := watchAsync(t, base, map[string]any{"cursor": 1, "limit": 10, "rev": rev})
	awaitBlock(t, blocked)
	publish(t, base, "beta", "alice", nil, "second", "2")
	res := receive(t, got)
	if res.Outcome != OutcomeOK || messageLen(res) != 1 {
		t.Fatalf("watch = %+v", res)
	}
	msg := (*res.Messages)[0]
	if msg.Seq != 2 || msg.Conversation != "beta" || msg.Body != "second" {
		t.Fatalf("message = %+v", msg)
	}
	if res.Rev == nil || *res.Rev != rev {
		t.Fatalf("same-conversation publish changed rev: %v", res.Rev)
	}
}

func TestWatchWakesOnCreateThatInsertedAndCloseThatChangedStatus(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "job")
	rev := currentRev(t, base)

	got := watchAsync(t, base, map[string]any{"rev": rev})
	awaitBlock(t, blocked)
	create(t, base, "job")
	noBlock(t, blocked)
	create(t, base, "other")
	res := receive(t, got)
	if res.Outcome != OutcomeOK || res.Rev == nil || *res.Rev != rev+1 {
		t.Fatalf("create watch = %+v", res)
	}

	rev = *res.Rev
	got = watchAsync(t, base, map[string]any{"rev": rev})
	awaitBlock(t, blocked)
	mustPost(t, base, PathClose, map[string]any{"conversation": "job"})
	res = receive(t, got)
	if res.Outcome != OutcomeOK || res.Rev == nil || *res.Rev != rev+1 {
		t.Fatalf("close watch = %+v", res)
	}
}

func TestWatchIgnoresIdempotentPublishAndRepeatedClose(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "job")
	publish(t, base, "job", "alice", nil, "one", "1")
	mustPost(t, base, PathClose, map[string]any{"conversation": "job"})
	rev := currentRev(t, base)

	got := watchAsync(t, base, map[string]any{"cursor": 1, "limit": 10, "rev": rev, "deadline": "1s"})
	awaitBlock(t, blocked)
	if again := publish(t, base, "job", "alice", nil, "one", "1"); again.AlreadyStored == nil || !*again.AlreadyStored {
		t.Fatalf("retry = %+v", again)
	}
	noBlock(t, blocked)
	mustPost(t, base, PathClose, map[string]any{"conversation": "job"})
	noBlock(t, blocked)
	if res := receive(t, got); res.Outcome != OutcomeTimeout {
		t.Fatalf("watch = %+v", res)
	}
}

func TestFilteredWatchStaysBlockedAcrossAnotherConversation(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "alpha")
	create(t, base, "beta")
	publish(t, base, "alpha", "alice", nil, "a1", "1")
	publish(t, base, "beta", "alice", nil, "b1", "2")
	rev := currentRev(t, base)

	got := watchAsync(t, base, map[string]any{"conversation": "alpha", "cursor": 1, "limit": 10, "rev": rev})
	awaitBlock(t, blocked)
	publish(t, base, "beta", "alice", nil, "b2", "3")
	// Woken by the store-wide set, found nothing in alpha, and parked again.
	awaitBlock(t, blocked)
	publish(t, base, "alpha", "alice", nil, "a2", "4")
	res := receive(t, got)
	if res.Outcome != OutcomeOK || messageLen(res) != 1 {
		t.Fatalf("watch = %+v", res)
	}
	if msg := (*res.Messages)[0]; msg.Conversation != "alpha" || msg.Body != "a2" {
		t.Fatalf("message = %+v", msg)
	}
	if res.Rev == nil || *res.Rev != rev+1 {
		t.Fatalf("rev = %v", res.Rev)
	}
}

func TestWatchWithoutCursorWakesOnlyOnRevChange(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "job")
	publish(t, base, "job", "alice", nil, "one", "1")
	rev := currentRev(t, base)

	got := watchAsync(t, base, map[string]any{"rev": rev})
	awaitBlock(t, blocked)
	publish(t, base, "job", "alice", nil, "two", "2")
	awaitBlock(t, blocked)
	create(t, base, "other")
	res := receive(t, got)
	if res.Outcome != OutcomeOK || messageLen(res) != 0 || res.Rev == nil || *res.Rev != rev+1 {
		t.Fatalf("watch = %+v", res)
	}
}

func TestRevBumpsOnlyWhenTheNewestConversationChangesAndRestartLeavesHeldRev(t *testing.T) {
	base, dir, st, stop := startServer(t, Options{})
	start := currentRev(t, base)
	steps := []struct {
		do   func()
		want uint64
	}{
		{func() { create(t, base, "alpha") }, 1},
		{func() { publish(t, base, "alpha", "alice", nil, "a1", "1") }, 2},
		{func() { publish(t, base, "alpha", "alice", nil, "a2", "2") }, 2},
		{func() { create(t, base, "beta") }, 3},
		{func() { publish(t, base, "beta", "alice", nil, "b1", "3") }, 4},
		{func() { publish(t, base, "beta", "alice", nil, "b2", "4") }, 4},
		{func() { publish(t, base, "alpha", "alice", nil, "a3", "5") }, 5},
	}
	for i, step := range steps {
		step.do()
		if got := currentRev(t, base) - start; got != step.want {
			t.Fatalf("step %d rev moved by %d, want %d", i, got, step.want)
		}
	}
	held := start + 5

	stop()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st2.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	base, _ = serveStore(t, st2, Options{Logger: slog.New(slog.DiscardHandler)})
	// A rev held from the old process must not block on the new one.
	again := mustPost(t, base, PathWatch, map[string]any{"rev": held, "deadline": "0s"})
	if again.Outcome != OutcomeOK || again.Rev == nil || *again.Rev == held {
		t.Fatalf("held rev after restart = %+v", again)
	}
	restarted := *again.Rev
	publish(t, base, "alpha", "alice", nil, "a4", "6")
	if got := currentRev(t, base); got != restarted {
		t.Fatalf("publish to the stored newest conversation moved rev from %d to %d", restarted, got)
	}
	publish(t, base, "beta", "alice", nil, "b3", "7")
	if got := currentRev(t, base); got != restarted+1 {
		t.Fatalf("publish to another conversation rev = %d, want %d", got, restarted+1)
	}
}

func TestWatchReportsMorePastTheLimit(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "alpha")
	create(t, base, "beta")
	publish(t, base, "alpha", "alice", nil, "a1", "1")
	publish(t, base, "beta", "alice", nil, "b1", "2")
	publish(t, base, "alpha", "alice", nil, "a2", "3")

	first := mustPost(t, base, PathWatch, map[string]any{"cursor": 0, "limit": 2})
	if first.Outcome != OutcomeOK || messageLen(first) != 2 || first.More == nil || !*first.More {
		t.Fatalf("first = %+v", first)
	}
	if (*first.Messages)[0].Seq != 1 || (*first.Messages)[1].Seq != 2 {
		t.Fatalf("not oldest first: %+v", *first.Messages)
	}
	rest := mustPost(t, base, PathWatch, map[string]any{"cursor": 2, "limit": 2})
	if messageLen(rest) != 1 || rest.More == nil || *rest.More {
		t.Fatalf("rest = %+v", rest)
	}
	ghost := mustPost(t, base, PathWatch, map[string]any{"conversation": "ghost", "cursor": 0, "limit": 2})
	if ghost.Outcome != OutcomeOK || messageLen(ghost) != 0 {
		t.Fatalf("missing conversation = %+v", ghost)
	}
}

func TestWatchDeadlineTimesOutAndBusStopEndsWatch(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, stop := startServer(t, opt)
	create(t, base, "job")
	rev := currentRev(t, base)

	timed, raw := mustRaw(t, base, PathWatch, map[string]any{"cursor": 0, "limit": 5, "rev": rev, "deadline": "100ms"})
	if timed.Outcome != OutcomeTimeout || timed.Messages != nil || timed.Rev != nil {
		t.Fatalf("timeout body = %s", raw)
	}
	awaitBlock(t, blocked)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type reply struct {
		raw []byte
		err error
	}
	got := make(chan reply, 1)
	go func() {
		_, _, raw, err := post(ctx, base, PathWatch, map[string]any{"rev": rev})
		got <- reply{raw, err}
	}()
	awaitBlock(t, blocked)
	go stop()
	select {
	case r := <-got:
		if r.err == nil || len(r.raw) != 0 {
			t.Fatalf("watch at stop answered %q err=%v, want a dropped connection", r.raw, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not return at stop")
	}
}

func TestWatchRefusesBrokenRules(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	cases := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"cursor": -1, "limit": 1}, ReasonCursorRange},
		{map[string]any{"cursor": 0}, ReasonLimitRequired},
		{map[string]any{"cursor": 0, "limit": 0}, ReasonLimitRange},
		{map[string]any{"limit": -2}, ReasonLimitRange},
		{map[string]any{"conversation": "bad name"}, ReasonBadConversation},
		{map[string]any{"conversation": ""}, ReasonConversationRequired},
		{map[string]any{"deadline": "-1s"}, ReasonDeadline},
	}
	for _, c := range cases {
		res := mustPost(t, base, PathWatch, c.body)
		if res.Outcome != OutcomeRefused || res.Reason != c.want {
			t.Fatalf("%v = %+v, want %s", c.body, res, c.want)
		}
	}
}
