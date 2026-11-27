// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// testHolder is the conductor the tests' fenced writes run as.
const testHolder = "conductor-1"

// conduct acquires chat's lease for testHolder so the fenced writes that follow are accepted.
func conduct(t *testing.T, base, chat string) {
	t.Helper()
	if res := leaseCall(t, base, chat, LeaseAcquire, testHolder); res.Outcome != OutcomeOK {
		t.Fatalf("acquire %s: %+v", chat, res)
	}
}

func leaseCall(t *testing.T, base, chat, op, holder string) wire {
	t.Helper()
	body := map[string]any{"chat": chat, "lease": op}
	if holder != "" {
		body["holder"] = holder
	}
	return mustPost(t, base, PathLease, body)
}

// fakeClock is a bus clock a test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLeaseRefusesAMissingOrUnknownSelectorAndAMissingOrMalformedHolder(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	cases := []struct {
		name   string
		body   map[string]any
		reason string
	}{
		{"chat missing", map[string]any{"lease": "get"}, ReasonChatRequired},
		{"chat bad", map[string]any{"chat": "bad name", "lease": "get"}, ReasonBadChat},
		{"lease missing", map[string]any{"chat": "job", "holder": "h"}, ReasonLeaseRequired},
		{"lease null", map[string]any{"chat": "job", "lease": nil, "holder": "h"}, ReasonLeaseRequired},
		{"lease unknown", map[string]any{"chat": "job", "lease": "steal", "holder": "h"}, ReasonLeaseBad},
		{"acquire holder missing", map[string]any{"chat": "job", "lease": "acquire"}, ReasonHolderRequired},
		{"renew holder missing", map[string]any{"chat": "job", "lease": "renew"}, ReasonHolderRequired},
		{"release holder missing", map[string]any{"chat": "job", "lease": "release"}, ReasonHolderRequired},
		{"holder empty", map[string]any{"chat": "job", "lease": "acquire", "holder": ""}, ReasonHolderBad},
		{"holder bad", map[string]any{"chat": "job", "lease": "acquire", "holder": "a b"}, ReasonHolderBad},
		{"holder long", map[string]any{"chat": "job", "lease": "acquire", "holder": strings.Repeat("a", MaxNameLen+1)}, ReasonHolderBad},
		{"missing chat", map[string]any{"chat": "nope", "lease": "acquire", "holder": "h"}, ReasonNotFound},
		{"missing chat get", map[string]any{"chat": "nope", "lease": "get"}, ReasonNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustPost(t, base, PathLease, tc.body)
			if res.Outcome != OutcomeRefused || res.Reason != tc.reason {
				t.Fatalf("got %s %q, want refused %q", res.Outcome, res.Reason, tc.reason)
			}
		})
	}
	if got := leaseCall(t, base, "job", LeaseGet, ""); got.Held == nil || *got.Held {
		t.Fatalf("a refused call left a lease: %+v", got)
	}
}

func TestAcquireSucceedsWhenFreeOrRetriedByTheSameHolderAndReturnsTheTTL(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	create(t, base, "job")
	first := leaseCall(t, base, "job", LeaseAcquire, "h1")
	if first.Outcome != OutcomeOK || first.TTL == nil || *first.TTL != 20 || first.Held != nil || first.Remaining != nil {
		t.Fatalf("first = %+v", first)
	}
	since := leaseCall(t, base, "job", LeaseGet, "").Since
	clock.advance(5 * time.Second)
	again := leaseCall(t, base, "job", LeaseAcquire, "h1")
	if again.Outcome != OutcomeOK || again.TTL == nil || *again.TTL != 20 {
		t.Fatalf("retry = %+v", again)
	}
	got := leaseCall(t, base, "job", LeaseGet, "")
	if got.Since != since || got.Expires != clock.read().Add(LeaseTTL).Format(time.RFC3339Nano) {
		t.Fatalf("after retry since %s expires %s, want since %s", got.Since, got.Expires, since)
	}
}

func TestAcquireAgainstAnotherLiveHolderIsRefusedWithRemainingRoundedUp(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	create(t, base, "job")
	leaseCall(t, base, "job", LeaseAcquire, "h1")
	clock.advance(7*time.Second + time.Millisecond)
	res := leaseCall(t, base, "job", LeaseAcquire, "h2")
	if res.Outcome != OutcomeRefused || res.Reason != ReasonConductorHeld || res.Remaining == nil || *res.Remaining != 13 || res.TTL != nil {
		t.Fatalf("second = %+v", res)
	}
	if res := leaseCall(t, base, "job", LeaseRenew, "h1"); res.Outcome != OutcomeOK {
		t.Fatalf("the refusal disturbed the holder: %+v", res)
	}
}

func TestAcquireSucceedsOnceTheOtherHoldersLeaseExpires(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	create(t, base, "job")
	leaseCall(t, base, "job", LeaseAcquire, "dead")
	clock.advance(LeaseTTL - time.Nanosecond)
	if res := leaseCall(t, base, "job", LeaseAcquire, "next"); res.Outcome != OutcomeRefused || *res.Remaining != 1 {
		t.Fatalf("before expiry = %+v", res)
	}
	clock.advance(time.Nanosecond)
	if res := leaseCall(t, base, "job", LeaseAcquire, "next"); res.Outcome != OutcomeOK {
		t.Fatalf("at expiry = %+v", res)
	}
	if res := leaseCall(t, base, "job", LeaseRenew, "dead"); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseNotHeld {
		t.Fatalf("expired holder renewed: %+v", res)
	}
}

func TestRenewExtendsOnlyThisHoldersLiveLeaseAndNeverCreatesOne(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	create(t, base, "job")
	if res := leaseCall(t, base, "job", LeaseRenew, "h1"); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseNotHeld || res.TTL != nil {
		t.Fatalf("renew with no lease = %+v", res)
	}
	if got := leaseCall(t, base, "job", LeaseGet, ""); *got.Held {
		t.Fatal("renew created a lease")
	}
	leaseCall(t, base, "job", LeaseAcquire, "h1")
	if res := leaseCall(t, base, "job", LeaseRenew, "h2"); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseNotHeld {
		t.Fatalf("renew by another holder = %+v", res)
	}
	for range 3 {
		clock.advance(15 * time.Second)
		if res := leaseCall(t, base, "job", LeaseRenew, "h1"); res.Outcome != OutcomeOK || res.TTL == nil || *res.TTL != 20 {
			t.Fatalf("renew = %+v", res)
		}
	}
	clock.advance(LeaseTTL)
	if res := leaseCall(t, base, "job", LeaseRenew, "h1"); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseNotHeld {
		t.Fatalf("renew after expiry = %+v", res)
	}
	if res := leaseCall(t, base, "job", LeaseRenew, "h1"); res.Outcome != OutcomeRefused {
		t.Fatalf("a refused renew re-created the lease: %+v", res)
	}
}

func TestReleaseRemovesOnlyThisHoldersLease(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	if res := leaseCall(t, base, "job", LeaseRelease, "h1"); res.Outcome != OutcomeOK {
		t.Fatalf("release with no lease = %+v", res)
	}
	leaseCall(t, base, "job", LeaseAcquire, "h1")
	if res := leaseCall(t, base, "job", LeaseRelease, "h2"); res.Outcome != OutcomeOK {
		t.Fatalf("release by another holder = %+v", res)
	}
	if got := leaseCall(t, base, "job", LeaseGet, ""); !*got.Held {
		t.Fatal("another holder's release removed the lease")
	}
	if res := leaseCall(t, base, "job", LeaseRelease, "h1"); res.Outcome != OutcomeOK || res.TTL != nil {
		t.Fatalf("release = %+v", res)
	}
	if got := leaseCall(t, base, "job", LeaseGet, ""); *got.Held {
		t.Fatal("release left the lease")
	}
	if res := leaseCall(t, base, "job", LeaseAcquire, "h2"); res.Outcome != OutcomeOK {
		t.Fatalf("acquire after release = %+v", res)
	}
}

func TestGetReportsHeldSinceAndExpiresAndNeverTheHolder(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	create(t, base, "job")
	free, raw := mustRaw(t, base, PathLease, map[string]any{"chat": "job", "lease": "get", "holder": "not a name"})
	if free.Outcome != OutcomeOK || free.Held == nil || *free.Held || free.Since != "" || free.Expires != "" {
		t.Fatalf("free = %s", raw)
	}
	at := clock.read()
	leaseCall(t, base, "job", LeaseAcquire, "secret-holder")
	clock.advance(3 * time.Second)
	leaseCall(t, base, "job", LeaseRenew, "secret-holder")
	held, raw := mustRaw(t, base, PathLease, map[string]any{"chat": "job", "lease": "get"})
	if held.Outcome != OutcomeOK || !*held.Held || held.Since != at.Format(time.RFC3339Nano) || held.Expires != at.Add(3*time.Second+LeaseTTL).Format(time.RFC3339Nano) || held.TTL != nil {
		t.Fatalf("held = %s", raw)
	}
	if strings.Contains(string(raw), "secret-holder") {
		t.Fatalf("get returned the holder: %s", raw)
	}
}

func TestLeaseWorksOnAClosedChatAndLeavesRevAndWakesNoWatch(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "job")
	mustPost(t, base, PathClose, map[string]any{"chat": "job"})
	rev := currentRev(t, base)
	got := watchAsync(t, base, map[string]any{"cursor": 0, "limit": 10, "rev": rev, "deadline": "1s"})
	awaitBlock(t, blocked)
	for _, op := range []string{LeaseAcquire, LeaseRenew, LeaseGet, LeaseRelease} {
		if res := leaseCall(t, base, "job", op, testHolder); res.Outcome != OutcomeOK {
			t.Fatalf("%s on a closed chat = %+v", op, res)
		}
		noBlock(t, blocked)
	}
	if res := receive(t, got); res.Outcome != OutcomeTimeout {
		t.Fatalf("watch = %+v", res)
	}
	if now := currentRev(t, base); now != rev {
		t.Fatalf("rev = %d, want %d", now, rev)
	}
}

func TestLeaseLogHoldsTheCallAndOutcomeAndNoHolder(t *testing.T) {
	var logs lockedLog
	base, _, _, _ := startServer(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	create(t, base, "job")
	const holder = "holder-secret-77"
	leaseCall(t, base, "job", LeaseAcquire, holder)
	leaseCall(t, base, "job", LeaseAcquire, "holder-secret-88")
	mustPost(t, base, PathRecord, record("job", "halt", map[string]any{"finding": 1}))
	var lines []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "op=lease") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "lease=acquire") || !strings.Contains(lines[0], "outcome=ok") || !strings.Contains(lines[1], "outcome=refused") || !strings.Contains(lines[1], "chat=job") {
		t.Fatalf("lease log lines = %q", lines)
	}
	if strings.Contains(logs.String(), "holder-secret") || strings.Contains(logs.String(), testHolder) {
		t.Fatalf("log holds a holder:\n%s", logs.String())
	}
}

func TestLeaseRouteFollowsTheTokenRules(t *testing.T) {
	const token = "hstk-0a1b2c3d4e5f"
	base, _, _, _ := startServer(t, Options{Token: token})
	body := []byte(`{"chat":"job","lease":"get"}`)
	res, _ := authPost(t, base, PathLease, "", body, []string{token})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonTokenRequired {
		t.Fatalf("without token = %+v", res)
	}
	res, _ = authPost(t, base, PathLease, "Bearer "+token, body, []string{token})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonNotFound {
		t.Fatalf("with token = %+v", res)
	}
}

// fencedWrites are the conductor writes, each without a holder field.
func fencedWrites(finding int64) map[string]map[string]any {
	return map[string]map[string]any{
		PathRecord:  {"chat": "job", "record": "halt", "finding": finding},
		PathMember:  {"chat": "job", "name": "alpha", "status": "running"},
		PathSession: {"chat": "job", "name": "alpha", "session": "sess-1"},
	}
}

func withHolder(body map[string]any, holder any) map[string]any {
	out := map[string]any{"holder": holder}
	for k, v := range body {
		out[k] = v
	}
	return out
}

// fencedJob creates job with roster alpha and a launched alpha, and returns a finding the halt record can name.
func fencedJob(t *testing.T, base string) int64 {
	t.Helper()
	mustPost(t, base, PathCreate, map[string]any{"name": "job", "seat": "alpha", "roster": rosterNames("alpha")})
	conduct(t, base, "job")
	mustPost(t, base, PathMember, map[string]any{"chat": "job", "name": "alpha", "status": "launched", "holder": testHolder})
	put := kindSender(t, base, "job")
	turn, finding := put("turn"), put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{finding}}))
	leaseCall(t, base, "job", LeaseRelease, testHolder)
	return finding
}

func TestFencedWriteWithoutAHolderOrWithAMalformedOneIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	finding := fencedJob(t, base)
	conduct(t, base, "job")
	for path, body := range fencedWrites(finding) {
		if res := mustPost(t, base, path, body); res.Outcome != OutcomeRefused || res.Reason != ReasonHolderRequired {
			t.Fatalf("%s without holder = %+v", path, res)
		}
		if res := mustPost(t, base, path, withHolder(body, nil)); res.Outcome != OutcomeRefused || res.Reason != ReasonHolderRequired {
			t.Fatalf("%s with null holder = %+v", path, res)
		}
		for _, bad := range []string{"", "a b", strings.Repeat("h", MaxNameLen+1)} {
			if res := mustPost(t, base, path, withHolder(body, bad)); res.Outcome != OutcomeRefused || res.Reason != ReasonHolderBad {
				t.Fatalf("%s with holder %q = %+v", path, bad, res)
			}
		}
	}
	send := map[string]any{"chat": "job", "from": "run", "to": []string{}, "body": "b", "txid": "x", "kind": "say", "holder": ""}
	if res := mustPost(t, base, PathSend, send); res.Outcome != OutcomeRefused || res.Reason != ReasonHolderBad {
		t.Fatalf("send with empty holder = %+v", res)
	}
	assertUnchanged(t, base)
}

func TestFencedWriteByAHolderWithoutTheLiveLeaseIsRefusedAndStoresNothing(t *testing.T) {
	clock := newFakeClock()
	base, _, _, _ := startServer(t, Options{Clock: clock.read})
	finding := fencedJob(t, base)
	send := map[string]any{"chat": "job", "from": "run", "to": []string{}, "body": "b", "txid": "x", "kind": "say"}
	writes := fencedWrites(finding)
	writes[PathSend] = send
	refuse := func(when, holder string) {
		t.Helper()
		for path, body := range writes {
			if res := mustPost(t, base, path, withHolder(body, holder)); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseWrite {
				t.Fatalf("%s %s = %+v", path, when, res)
			}
		}
		assertUnchanged(t, base)
	}
	refuse("with no lease", testHolder)
	leaseCall(t, base, "job", LeaseAcquire, "other")
	refuse("under another holder", testHolder)
	leaseCall(t, base, "job", LeaseRelease, "other")
	conduct(t, base, "job")
	clock.advance(LeaseTTL)
	refuse("after the lease expired", testHolder)
	conduct(t, base, "job")
	for path, body := range writes {
		if res := mustPost(t, base, path, withHolder(body, testHolder)); res.Outcome != OutcomeOK {
			t.Fatalf("%s by the holder = %+v", path, res)
		}
	}
}

func TestRecordReplayByAStaleHolderIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	set := record("job", "set", map[string]any{"turn": turn, "findings": []int64{}})
	mustRecord(t, base, set)
	leaseCall(t, base, "job", LeaseRelease, testHolder)
	leaseCall(t, base, "job", LeaseAcquire, "next")
	assertRefused(t, mustPost(t, base, PathRecord, set), ReasonLeaseWrite)
	stale := map[string]any{"chat": "job", "from": "run", "to": []string{}, "body": "b", "txid": "replayed", "kind": "say"}
	if res := mustPost(t, base, PathSend, stale); res.Outcome != OutcomeOK {
		t.Fatalf("send = %+v", res)
	}
	if res := mustPost(t, base, PathSend, withHolder(stale, testHolder)); res.Outcome != OutcomeRefused || res.Reason != ReasonLeaseWrite {
		t.Fatalf("send replay by a stale holder = %+v", res)
	}
}

func TestStopRecordIsFencedByTheLease(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	done := kindSender(t, base, "job")("done")
	leaseCall(t, base, "job", LeaseRelease, testHolder)
	stop := record("job", "stop", map[string]any{"done": done})
	delete(stop, "holder")
	assertRefused(t, mustPost(t, base, PathRecord, stop), ReasonHolderRequired)
	assertRefused(t, mustPost(t, base, PathRecord, withHolder(stop, testHolder)), ReasonLeaseWrite)
	leaseCall(t, base, "job", LeaseAcquire, "other")
	assertRefused(t, mustPost(t, base, PathRecord, withHolder(stop, testHolder)), ReasonLeaseWrite)
	if recs, _ := readRecords(t, base, "job"); len(*recs.Stops) != 0 {
		t.Fatalf("stops = %+v", *recs.Stops)
	}
	leaseCall(t, base, "job", LeaseRelease, "other")
	conduct(t, base, "job")
	mustRecord(t, base, withHolder(stop, testHolder))
}

func TestFencedWriteOnAMissingChatIsNotFound(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	for path, body := range fencedWrites(1) {
		body["chat"] = "nope"
		if res := mustPost(t, base, path, withHolder(body, testHolder)); res.Outcome != OutcomeRefused || res.Reason != ReasonNotFound {
			t.Fatalf("%s = %+v", path, res)
		}
	}
}

func TestSendWithoutAHolderIsNotFenced(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	leaseCall(t, base, "job", LeaseAcquire, "other")
	if res := send(t, base, "job", "alice", nil, "hi", "k1"); res.Outcome != OutcomeOK {
		t.Fatalf("send = %+v", res)
	}
	null := map[string]any{"chat": "job", "from": "alice", "to": []string{}, "body": "b", "txid": "k2", "kind": "say", "holder": nil}
	if res := mustPost(t, base, PathSend, null); res.Outcome != OutcomeOK {
		t.Fatalf("send with null holder = %+v", res)
	}
}

func TestReadsJoinAndCloseIgnoreTheLease(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	fencedJob(t, base)
	leaseCall(t, base, "job", LeaseAcquire, "other")
	for _, call := range []struct {
		path string
		body map[string]any
	}{
		{PathMember, map[string]any{"chat": "job", "name": "alpha", "holder": "not a name"}},
		{PathSession, map[string]any{"chat": "job", "name": "alpha", "holder": "stale"}},
		{PathSession, map[string]any{"chat": "job"}},
		{PathMembers, map[string]any{"chat": "job"}},
		{PathRecords, map[string]any{"chat": "job"}},
		{PathJoin, map[string]any{"chat": "job", "name": "alpha"}},
		{PathClose, map[string]any{"chat": "job"}},
	} {
		if res := mustPost(t, base, call.path, call.body); res.Outcome != OutcomeOK {
			t.Fatalf("%s %v = %+v", call.path, call.body, res)
		}
	}
}

// assertUnchanged checks that fencedJob's rows and transcript were not written by a refused call.
func assertUnchanged(t *testing.T, base string) {
	t.Helper()
	m := mustPost(t, base, PathMember, map[string]any{"chat": "job", "name": "alpha"})
	if m.Member == nil || m.Member.Status != "launched" {
		t.Fatalf("member = %+v", m.Member)
	}
	s := mustPost(t, base, PathSession, map[string]any{"chat": "job", "name": "alpha"})
	if s.Session == nil || s.Session.Session != "" {
		t.Fatalf("session = %+v", s.Session)
	}
	recs, _ := readRecords(t, base, "job")
	if len(*recs.Halts) != 0 {
		t.Fatalf("halts = %+v", *recs.Halts)
	}
	read := mustPost(t, base, PathRead, map[string]any{"chat": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 2 {
		t.Fatalf("transcript = %+v", read.Messages)
	}
}
