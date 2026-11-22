// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
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
	out, _, err := h.runBounded(h.continueKept(agentsOf("alpha", "bravo")))
	if err == nil || !strings.Contains(err.Error(), bus.ReasonConductorHeld) {
		t.Fatalf("err %v", err)
	}
	if !out.onlyUnheld() {
		t.Fatalf("out %q", out)
	}
	if len(waits) != 1 || waits[0] <= 0 || waits[0] > bus.LeaseTTL {
		t.Fatalf("waits %v", waits)
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
	// Nothing is said before the lease is held, a stop error included.
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
		t.Fatalf("run returned before the wait: %v\n%s", res.err, res.out)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not wait on the held lease")
	}
	cancel()
	res := h.awaitRun(ch, 5*time.Second)
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("err %v", res.err)
	}
	if !res.out.onlyUnheld() {
		t.Fatalf("out %q", res.out)
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
	out, stderr, err := h.runBounded(h.continueKept(agentsOf("alpha", "bravo")))
	if err != nil {
		t.Fatal(err, stderr)
	}
	if len(waits) != 1 || waits[0] != bus.LeaseTTL {
		t.Fatalf("waits %v", waits)
	}
	if out.outcome() != "done" {
		t.Fatalf("out %s", out)
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

// memberPID is name's stored pid. The test fails when there is none.
func (h *harness) memberPID(conv, name string) int {
	h.t.Helper()
	row := h.member(conv, name)
	if row.PID == nil {
		h.t.Fatalf("%s has no pid", name)
	}
	return int(*row.PID)
}

// refusedRenewTap returns a bus address for a run, and a channel closed once that run has been answered with a refused renew.
func (h *harness) refusedRenewTap() (string, <-chan struct{}) {
	h.t.Helper()
	refused := make(chan struct{})
	var once sync.Once
	addr := h.proxy(func(w http.ResponseWriter, path string, body []byte) bool {
		if path != bus.PathLease || !bytes.Contains(body, []byte(`"lease":"`+bus.LeaseRenew+`"`)) {
			return false
		}
		resp, err := http.Post("http://"+h.addr+path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		if bytes.Contains(raw, []byte(`"outcome":"`+bus.OutcomeRefused+`"`)) {
			once.Do(func() { close(refused) })
		}
		return true
	})
	return addr, refused
}

// waitTap returns a bus address for a run, and a channel closed at that run's first wait call.
// A continuation waits only after it has settled or adopted every process left from an earlier run.
func (h *harness) waitTap() (string, <-chan struct{}) {
	h.t.Helper()
	waiting := make(chan struct{})
	var once sync.Once
	addr := h.proxy(func(_ http.ResponseWriter, path string, _ []byte) bool {
		if path == bus.PathWait {
			once.Do(func() { close(waiting) })
		}
		return false
	})
	return addr, waiting
}

// await fails the test unless ch closes within 8 seconds.
func (h *harness) await(ch <-chan struct{}, what string) {
	h.t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		h.t.Fatalf("%s did not happen", what)
	}
}

// continueVia is cfg continuing conversation name with the base roster and seat, through the bus at addr.
func (h *harness) continueVia(name, addr string) Config {
	cfg := h.base()
	cfg.Conversation = name
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.Address = addr
	return cfg
}

func TestRunThatLosesItsLeaseLeavesItsMemberRunningAndTheNextHolderRecordsIt(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	h.plan.Launch = map[string]runFile{"bravo": {Register: true}}
	cfg := h.base()
	cfg.TicketSet = false
	firstAddr, refused := h.refusedRenewTap()
	cfg.Address = firstAddr
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	first := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.awaitStatus(name, "bravo", statusExited)
	pid := h.memberPID(name, "alpha")
	clock.advance(bus.LeaseTTL)
	h.await(refused, "the first run's refused renew")
	nextAddr, adopted := h.waitTap()
	nctx, stopNext := ctx()
	defer stopNext()
	next := h.goRun(nctx, h.continueVia(name, nextAddr))
	h.await(adopted, "the next run's adoption")
	if !alive(pid) {
		t.Fatal("the run that lost its lease killed alpha")
	}
	h.stillRunning(first)
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
	}
	h.release("alpha")
	res := h.awaitRun(first, 8*time.Second)
	if !errors.Is(res.err, errLeaseLost) {
		t.Fatalf("err %v", res.err)
	}
	if res.out.outcome() != "error" {
		t.Fatalf("out %s", res.out)
	}
	if strings.Contains(res.stderr, "lease release") {
		t.Fatalf("the stale run released a lease it no longer held\n%s", res.stderr)
	}
	h.wantStopError(name, stopLease, 0)
	// A boot launch stored no session id, so the adopting run cannot credit a clean exit.
	h.awaitStatus(name, "alpha", statusTimedOut)
	if countBody(h.messages(name), classProcessExit) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
	stopNext()
	h.awaitRun(next, 8*time.Second)
}

func TestRelaunchedMemberOutlivesTwoLostLeasesAndTheThirdHolderRecordsItsExit(t *testing.T) {
	clock := newBusClock()
	// A bus clock an hour behind the host matches no host start time against a stored launch time.
	clock.advance(-time.Hour)
	h := newHarnessClock(t, clock.read)
	h.plan.Launch = map[string]runFile{"alpha": {Steps: []stepFile{
		on(promptTurn, withRe(says(kindSay, "bravo"), "$re")),
	}}}
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	first := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	boot := h.memberPID(name, "alpha")
	h.hold("alpha")
	h.publish(name, "bravo", kindTurn, "mode: parallel\nreview", []string{"alpha"})
	h.awaitStatus(name, "alpha", statusRunning)
	pid := h.memberPID(name, "alpha")
	if pid == boot {
		t.Fatal("the turn did not relaunch alpha")
	}
	clock.advance(bus.LeaseTTL)
	secondAddr, adopted := h.waitTap()
	second := h.goRun(cctx, h.continueVia(name, secondAddr))
	h.await(adopted, "the second run's adoption")
	clock.advance(bus.LeaseTTL)
	res := h.awaitRun(second, 8*time.Second)
	if !errors.Is(res.err, errLeaseLost) {
		t.Fatalf("second run err %v", res.err)
	}
	if !alive(pid) {
		t.Fatal("a run that lost its lease killed the member it adopted")
	}
	// The member it left running has not exited, so that run writes no exit for it.
	if !strings.Contains(res.stderr, eventLine("adopt", "member", "alpha")) || strings.Contains(res.stderr, " event=exit member=alpha ") {
		t.Fatalf("second run stderr\n%s", res.stderr)
	}
	h.stillRunning(first)
	if got := h.memberStatus(name, "alpha"); got != statusRunning {
		t.Fatalf("alpha %s", got)
	}
	thirdAddr, adoptedAgain := h.waitTap()
	third := h.goRun(cctx, h.continueVia(name, thirdAddr))
	h.await(adoptedAgain, "the third run's adoption")
	h.release("alpha")
	if res := h.awaitRun(first, 8*time.Second); !errors.Is(res.err, errLeaseLost) {
		t.Fatalf("first run err %v", res.err)
	}
	h.awaitStatus(name, "alpha", statusExited)
	if n := countBody(h.messages(name), classProcessExit); n != 0 {
		t.Fatalf("an adopted relaunch was recorded as a process exit\n%+v", h.messages(name))
	}
	h.publish(name, "alpha", kindDone, "finished", nil)
	got := h.awaitRun(third, 8*time.Second)
	if got.err != nil {
		t.Fatal(got.err, got.stderr)
	}
	if got.out.outcome() != "done" {
		t.Fatalf("out %s", got.out)
	}
	if n := countBody(h.messages(name), classProcessExit); n != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
	if !strings.Contains(got.stderr, eventLine("adopt", "member", "alpha")) || !strings.Contains(got.stderr, " event=exit member=alpha duration=") {
		t.Fatalf("third run stderr\n%s", got.stderr)
	}
}

// A run killed with SIGKILL leaves the row of a later launch: a pid, its start token, and a stored session.
func TestContinuationAdoptsARelaunchAKilledRunLeftAndRecordsItExited(t *testing.T) {
	// A bus clock an hour behind the host matches no host start time against a stored launch time.
	h := newHarnessClock(t, func() time.Time { return time.Now().Add(-time.Hour) })
	h.keptConversation("alpha", "bravo")
	h.setMember("kept", "alpha", statusLaunched, nil)
	h.setMember("kept", "alpha", statusExited, nil)
	h.setSession("kept", "alpha", "sess-alpha")
	h.setMember("kept", "bravo", statusLaunched, nil)
	h.setMember("kept", "bravo", statusExited, nil)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	reaped := make(chan struct{})
	go func() {
		_, _ = child.Process.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-reaped
	})
	live := int64(pid)
	if row := h.setMember("kept", "alpha", statusRunning, &live); row.Member == nil || row.Member.Start == "" {
		t.Skipf("no start token on this platform: %+v", row)
	}
	addr, adopted := h.waitTap()
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.continueVia("kept", addr))
	h.await(adopted, "the continuation's adoption")
	if !alive(pid) {
		t.Fatal("the continuation ended the process it adopted")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-reaped
	h.awaitStatus("kept", "alpha", statusExited)
	if n := countBody(h.messages("kept"), classProcessExit); n != 0 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
	h.publish("kept", "alpha", kindDone, "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
	// An adopted process is not a child, so its exit has no code.
	if !strings.Contains(res.stderr, eventLine("adopt", "member", "alpha")) || !strings.Contains(res.stderr, " event=exit member=alpha duration=") {
		t.Fatalf("stderr\n%s", res.stderr)
	}
}

func TestSignalWhileARunWaitsOnALostLeaseKillsItsLaunchedMembers(t *testing.T) {
	clock := newBusClock()
	h := newHarnessClock(t, clock.read)
	cfg := h.base()
	cfg.TicketSet = false
	addr, refused := h.refusedRenewTap()
	cfg.Address = addr
	h.hold("alpha")
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	pid := h.memberPID(name, "alpha")
	clock.advance(bus.LeaseTTL)
	if res := h.leaseAs(name, bus.LeaseAcquire, "next-run"); res.Outcome != bus.OutcomeOK {
		t.Fatalf("acquire after expiry %+v", res)
	}
	h.await(refused, "the run's refused renew")
	h.stillRunning(ch)
	if !alive(pid) {
		t.Fatal("alpha died before the signal")
	}
	h.awaitBody(name, stopLease)
	cancel()
	h.awaitRun(ch, 8*time.Second)
	h.wantStopError(name, stopLease, 0)
	if !waitDead(context.Background(), pid, 2*time.Second) {
		t.Fatal("the signal left alpha running")
	}
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
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
	h.wantStopError(name, stopLease, 0)
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
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
	if got := h.memberStatus(name, "alpha"); got != statusExited {
		t.Fatalf("alpha %s", got)
	}
	if h.leaseHeld(name) {
		t.Fatal("the run left its lease")
	}
	h.wantNoStopError(name)
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
	done := h.publish(name, "alpha", kindDone, "finished", nil)
	h.awaitClosed(name)
	clock.advance(bus.LeaseTTL)
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), errLeaseLost.Error()+": "+bus.ReasonLeaseWrite) {
		t.Fatalf("err %v", res.err)
	}
	// The halt after the close keeps the recorded done and its stop lines, then says the stop.
	if !res.out.stopIs(done.Seq, nil, nil) || !res.out.endedBy(stopLease) {
		t.Fatalf("out %s", res.out)
	}
	if got := h.memberStatus(name, "alpha"); got != statusLaunched {
		t.Fatalf("alpha %s", got)
	}
	h.wantStopError(name, stopLease, 0)
	st := h.status(name)
	if !reflect.DeepEqual(st.Stop, res.out.stop()) || st.Stop.Outcome != outcomeDone || st.StopError == nil || st.StopError.Class != stopLease {
		t.Fatalf("status stop %+v stop error %+v", st.Stop, st.StopError)
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
	places := []string{res.out.String(), res.stderr}
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
