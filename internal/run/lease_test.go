// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// busClock is a bus clock a test moves by hand. It starts at the real time.
type busClock struct {
	mu  sync.Mutex
	now time.Time
}

func newBusClock() *busClock { return &busClock{now: time.Now()} }

func (c *busClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *busClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// leaseHeld reports whether conv has a live lease.
func (h *harness) leaseHeld(conv string) bool {
	h.t.Helper()
	res := h.leaseAs(conv, bus.LeaseGet, "")
	if res.Held == nil {
		h.t.Fatalf("get %s: %+v", conv, res)
	}
	return *res.Held
}

// awaitRenewed waits until conv's lease expires one time to live after the bus clock's now.
func (h *harness) awaitRenewed(conv string, clock *busClock) {
	h.t.Helper()
	want := clock.read().Add(bus.LeaseTTL).UTC().Format(time.RFC3339Nano)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if res := h.leaseAs(conv, bus.LeaseGet, ""); res.Expires == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("lease on %s was not renewed", conv)
}

// awaitClosed waits until conv is closed.
func (h *harness) awaitClosed(conv string) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := h.conv(conv); c.Status == "closed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("%s did not close", conv)
}

func (h *harness) noRecords(conv string) {
	h.t.Helper()
	r := h.recordsOf(conv)
	if len(r.sets)+len(r.groupings)+len(r.settles)+len(r.fixes)+len(r.halts) != 0 {
		h.t.Fatalf("records %+v", r)
	}
}

func TestSecondRunAgainstALiveConductorWaitsOnceThenIsRefusedAndActsOnNothing(t *testing.T) {
	h := newHarness(t)
	h.keptConversation("alpha", "bravo")
	if res := h.leaseAs("kept", bus.LeaseAcquire, "live"); res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	var waits []time.Duration
	h.pace.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		// The live conductor renews while the second run waits.
		h.leaseAs("kept", bus.LeaseRenew, "live")
		return nil
	}
	before := len(h.messages("kept"))
	stdout, _, err := h.runBounded(h.continueKept(agentsOf("alpha", "bravo")))
	if err == nil || !strings.Contains(err.Error(), bus.ReasonConductorHeld) {
		t.Fatalf("err %v", err)
	}
	if stdout != "outcome: error\n" {
		t.Fatalf("stdout %q", stdout)
	}
	if len(waits) != 1 || waits[0] <= 0 || waits[0] > bus.LeaseTTL {
		t.Fatalf("waits %v", waits)
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
	if got := len(h.messages("kept")); got != before {
		t.Fatalf("messages %d, want %d", got, before)
	}
	h.noRecords("kept")
	if rows := h.do("members", client.MembersRequest{Conversation: "kept"}); rows.Members == nil || len(*rows.Members) != 0 {
		t.Fatalf("members %+v", rows.Members)
	}
	if res := h.leaseAs("kept", bus.LeaseRenew, "live"); res.Outcome != bus.OutcomeOK {
		t.Fatalf("the refused run disturbed the live conductor: %s", res.Reason)
	}
}

func TestSignalDuringTheHeldLeaseWaitStopsTheRunWithNothingDone(t *testing.T) {
	h := newHarness(t)
	h.keptConversation("alpha", "bravo")
	if res := h.leaseAs("kept", bus.LeaseAcquire, "live"); res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	waiting := make(chan struct{})
	h.pace.sleep = func(ctx context.Context, d time.Duration) error {
		close(waiting)
		return sleepCtx(ctx, d)
	}
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.goRun(cctx, h.continueKept(agentsOf("alpha", "bravo")))
	select {
	case <-waiting:
	case res := <-ch:
		t.Fatalf("run returned before the wait: %v\n%s", res.err, res.stdout)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not wait on the held lease")
	}
	cancel()
	res := h.awaitRun(ch, 5*time.Second)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("err %v", res.err)
	}
	if res.stdout != "outcome: error\n" {
		t.Fatalf("stdout %q", res.stdout)
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
	h.noRecords("kept")
}

func TestRunAcquiresOnceADeadConductorsLeaseRunsOut(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	h.keptConversation("alpha", "bravo")
	h.publish("kept", "alpha", kindDone, "finished", nil)
	if res := h.leaseAs("kept", bus.LeaseAcquire, "killed-run"); res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	var waits []time.Duration
	h.pace.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		clock.advance(d)
		return nil
	}
	stdout, stderr, err := h.runBounded(h.continueKept(agentsOf("alpha", "bravo")))
	if err != nil {
		t.Fatal(err, stderr)
	}
	if len(waits) != 1 || waits[0] != bus.LeaseTTL {
		t.Fatalf("waits %v", waits)
	}
	if !strings.HasSuffix(stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", stdout)
	}
	if conv, _ := h.conv("kept"); conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
	if h.leaseHeld("kept") {
		t.Fatal("the run left its lease")
	}
}

func TestCreatedRunHoldsTheLeaseUntilItExits(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	if !h.leaseHeld(name) {
		t.Fatal("a driving run holds no lease")
	}
	if res := h.leaseAs(name, bus.LeaseAcquire, "second"); res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonConductorHeld {
		t.Fatalf("second acquire %+v", res)
	}
	h.publish(name, "alpha", kindDone, "finished", nil)
	h.release("alpha")
	res := h.awaitRun(ch, 10*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if h.leaseHeld(name) {
		t.Fatal("the run left its lease")
	}
}

func TestRunStopsOnceItsLeaseIsTakenAndItsLaterWritesAreRefused(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	clock.advance(bus.LeaseTTL)
	if res := h.leaseAs(name, bus.LeaseAcquire, "next-run"); res.Outcome != bus.OutcomeOK {
		t.Fatalf("acquire after expiry %+v", res)
	}
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), errLeaseLost.Error()) {
		t.Fatalf("err %v", res.err)
	}
	if !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
	// The stopped run killed alpha, and its timed-out write was refused.
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
	}
	if r := h.leaseAs(name, bus.LeaseRenew, "next-run"); r.Outcome != bus.OutcomeOK {
		t.Fatalf("the stopped run disturbed the next conductor: %s", r.Reason)
	}
}

func TestRefusedConductorWriteStopsARunThatMissedItsRenewals(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	h.pace.renew = time.Hour
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.awaitStatus(name, "bravo", statusExited)
	clock.advance(bus.LeaseTTL)
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), errLeaseLost.Error()+": "+bus.ReasonLeaseWrite) {
		t.Fatalf("err %v", res.err)
	}
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
	}
}

func TestDoneSenderExitingAfterTheTimeToLiveStillEndsDoneAndReleases(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	cfg := h.base()
	cfg.TicketSet = false
	h.pace.grace = 10 * time.Second
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.publish(name, "alpha", kindDone, "finished", nil)
	h.awaitClosed(name)
	for range 3 {
		clock.advance(bus.LeaseTTL / 2)
		h.awaitRenewed(name, clock)
	}
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
	if got := h.memberStatus(name, "alpha"); got != statusExited {
		t.Fatalf("alpha %s", got)
	}
	if h.leaseHeld(name) {
		t.Fatal("the run left its lease")
	}
}

func TestLeaseLostDuringTheDoneGraceEndsWithErrorNotDone(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	h.pace.renew = time.Hour
	h.pace.grace = 10 * time.Second
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.publish(name, "alpha", kindDone, "finished", nil)
	h.awaitClosed(name)
	clock.advance(bus.LeaseTTL)
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), errLeaseLost.Error()+": "+bus.ReasonLeaseWrite) {
		t.Fatalf("err %v", res.err)
	}
	if !strings.HasSuffix(res.stdout, "outcome: error\n") || strings.Contains(res.stdout, "outcome: done") {
		t.Fatalf("stdout %s", res.stdout)
	}
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
	}
}

func TestHolderIsSentOnEveryConductorWriteAndStaysOffPromptsOutputAndTranscript(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	holders := map[string]bool{}
	unfenced := map[string]int{}
	addr := h.proxy(func(_ http.ResponseWriter, path string, body []byte) bool {
		var req struct {
			Holder  *string `json:"holder"`
			Status  *string `json:"status"`
			Session *string `json:"session"`
			Lease   string  `json:"lease"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if req.Holder != nil {
			holders[*req.Holder] = true
		}
		fenced := path == bus.PathRecord || path == bus.PathPublish ||
			(path == bus.PathMember && req.Status != nil) || (path == bus.PathSession && req.Session != nil)
		if fenced && req.Holder == nil {
			unfenced[path]++
		}
		return false
	})
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Address = addr
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.publish(name, "alpha", kindDone, "finished", nil)
	res := h.awaitRun(ch, 10*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(unfenced) != 0 {
		t.Fatalf("conductor writes without a holder: %v", unfenced)
	}
	if len(holders) != 1 {
		t.Fatalf("holders %v", holders)
	}
	var holder string
	for holder = range holders {
	}
	if raw, err := hex.DecodeString(holder); err != nil || len(raw) != 16 || strings.ToLower(holder) != holder {
		t.Fatalf("holder %q is not 32 lowercase hex characters", holder)
	}
	places := []string{res.stdout, res.stderr}
	for _, c := range h.calls() {
		places = append(places, c.Stdin, strings.Join(c.Argv, " "))
	}
	for _, m := range h.messages(name) {
		places = append(places, m.Body, m.TxID)
	}
	for _, text := range places {
		if strings.Contains(text, holder) {
			t.Fatalf("holder leaked into %q", text)
		}
	}
}
