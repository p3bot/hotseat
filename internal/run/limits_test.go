// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// awaitBody waits until a message in the chat name contains part.
func (h *harness) awaitBody(name, part string) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if countBody(h.messages(name), part) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no %q in %+v", part, h.messages(name))
}

// stillRunning fails when the run has returned.
func (h *harness) stillRunning(ch <-chan runResult) {
	h.t.Helper()
	select {
	case res := <-ch:
		h.t.Fatalf("run returned early: %v\n%s", res.err, res.out)
	default:
	}
}

// keptChat creates kept with roster, each on its test module, permission auto, and seat alpha, and sends its instructions.
func (h *harness) keptChat(roster ...string) {
	h.t.Helper()
	h.keptChatAs(seeded(roster...), DefaultPermission)
}

// keptChatAs creates kept with seat alpha and the stored roster and permission as given, as chat create or an old
// writer could, and sends its instructions.
func (h *harness) keptChatAs(roster []client.RosterEntry, permission string) {
	h.t.Helper()
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: roster, Permission: permission,
	})
	if res.Outcome != bus.OutcomeOK {
		h.t.Fatal(res.Reason)
	}
	h.send("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", client.RosterNames(roster))
}

func (h *harness) continueKept() runArgs {
	return h.continuing("kept")
}

// runBounded is run with a deadline, so a run that does not stop fails instead of hanging the suite.
func (h *harness) runBounded(cfg runArgs) (*recorder, string, error) {
	h.t.Helper()
	cctx, cancel := ctx()
	defer cancel()
	return h.run(cctx, cfg)
}

func (h *harness) launchCount() int {
	n := 0
	for _, c := range h.calls() {
		if c.Kind == "launch" {
			n++
		}
	}
	return n
}

func TestRosterBelowTaskMinimumCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1"
	out, _, err := h.runBounded(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires 2") {
		t.Fatalf("err %v", err)
	}
	if !out.silent() {
		t.Fatalf("out %s", out)
	}
	if h.onlyExists() {
		t.Fatal("chat created")
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
}

func TestMinimumThatIsNotAPositiveIntegerCreatesNothing(t *testing.T) {
	for _, out := range []string{"0\n", "two\n", "-2\n", "+2\n", "2\n3\n"} {
		t.Run(strings.TrimSpace(out), func(t *testing.T) {
			h := newHarness(t)
			h.plan.MinMembers = out
			out, _, err := h.runBounded(h.base())
			if err == nil || !strings.Contains(err.Error(), "not a positive integer") {
				t.Fatalf("err %v", err)
			}
			if !out.silent() || h.onlyExists() {
				t.Fatalf("out %q created %v", out, h.onlyExists())
			}
		})
	}
}

func TestTaskWithoutMinimumRunsWithOneMember(t *testing.T) {
	h := newHarness(t)
	h.pace.grace = time.Second
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1"
	cfg.Ticket = ""
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.send(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
}

func TestContinueBelowTaskMinimumWithoutDoneFails(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptChat("alpha")
	out, _, err := h.runBounded(h.continueKept())
	if err == nil || !strings.Contains(err.Error(), "requires 2") {
		t.Fatalf("err %v", err)
	}
	if out.outcome() != "error" {
		t.Fatalf("out %s", out)
	}
	if chat, _ := h.chat("kept"); chat.Status == "closed" {
		t.Fatal("closed")
	}
	// The roster check refuses the request as it does on create, so no stop error is said.
	h.wantNoStopError("kept")
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
}

func TestContinueWithStoredDoneReportsDoneUnderARisenMinimum(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptChat("alpha")
	h.send("kept", "alpha", "done", "finished", nil)
	out, stderr, err := h.runBounded(h.continueKept())
	if err != nil {
		t.Fatal(err, stderr)
	}
	if out.outcome() != "done" || strings.Contains(stderr, "requires") {
		t.Fatalf("out %s stderr %s", out, stderr)
	}
	if chat, _ := h.chat("kept"); chat.Status != "closed" {
		t.Fatalf("status %s", chat.Status)
	}
	for _, c := range h.calls() {
		if c.Kind == "field" && strings.Contains(strings.Join(c.Argv, " "), "min_members") {
			t.Fatal("minimum read before the stored done")
		}
	}
}

func TestContinueWithStoredDoneAndDeadLeftoverUnderTheMinimumReportsDone(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	child := exec.Command("sleep", "0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := int64(child.Process.Pid)
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	h.keptChat("alpha", "bravo")
	launched := statusLaunched
	h.setMember("kept", "bravo", launched, &deadPID)
	h.send("kept", "alpha", "done", "finished", nil)
	out, stderr, err := h.runBounded(h.continueKept())
	if err != nil {
		t.Fatal(err, stderr)
	}
	if out.outcome() != "done" {
		t.Fatalf("out %s", out)
	}
	if got := h.memberStatus("kept", "bravo"); got != statusTimedOut {
		t.Fatalf("bravo %s", got)
	}
	if countBody(h.messages("kept"), classBelowMinimum) != 0 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
}

func TestContinueThatFindsTooFewLiveMembersStops(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptChat("alpha", "bravo")
	launched, timedOut := statusLaunched, statusTimedOut
	h.setMember("kept", "bravo", launched, nil)
	h.setMember("kept", "bravo", timedOut, nil)
	h.send("kept", "alpha", "turn", "mode: parallel\nreview", []string{"alpha"})
	out, stderr, err := h.runBounded(h.continueKept())
	if err == nil || !strings.Contains(err.Error(), classBelowMinimum) {
		t.Fatalf("err %v", err)
	}
	if !out.endedBy(stopProtocol) || strings.Contains(stderr, " event=turn ") {
		t.Fatalf("out %s\nstderr %s", out, stderr)
	}
	if countBody(h.messages("kept"), classBelowMinimum) != 1 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
	if chat, _ := h.chat("kept"); chat.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestContinueAppliesTheMinimumToTheStoredRoster(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptChat("alpha", "bravo")
	launched, timedOut := statusLaunched, statusTimedOut
	h.setMember("kept", "bravo", launched, nil)
	h.setMember("kept", "bravo", timedOut, nil)
	res := h.awaitRun(h.goRun(t.Context(), h.continueKept()), 8*time.Second)
	if res.err == nil || res.out.outcome() != outcomeError {
		t.Fatalf("err %v\n%s", res.err, res.out)
	}
	if countBody(h.messages("kept"), classBelowMinimum) != 1 || h.launchCount() != 0 {
		t.Fatalf("msgs %+v calls %+v", h.messages("kept"), h.calls())
	}
	if chat, _ := h.chat("kept"); chat.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestMemberLostBelowMinimumLetsTheOtherFinishAndStops(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.plan.Launch = map[string]runFile{"alpha": {Exit: 1, ExitOn: "mode:"}}
	cfg := h.base()
	cfg.Ticket = ""
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	h.hold("bravo")
	h.send(name, "alpha", "turn", "mode: parallel\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "bravo", statusRunning)
	h.awaitBody(name, classBelowMinimum)
	if got := h.memberStatus(name, "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	// A turn from the finishing member during the stop launches nothing and stops nobody.
	h.send(name, "bravo", "turn", "mode: serial\nnext", []string{"alpha", "bravo"})
	time.Sleep(500 * time.Millisecond)
	h.stillRunning(ch)
	if got := h.memberStatus(name, "bravo"); got != statusRunning {
		t.Fatalf("bravo %s during the stop", got)
	}
	h.release("bravo")
	released := time.Now()
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), classBelowMinimum) {
		t.Fatalf("err %v", res.err)
	}
	if took := res.ended.Sub(released); took > 3*time.Second {
		t.Fatalf("run returned %s after the last exit", took)
	}
	if !res.out.endedBy(stopProtocol) || strings.Count(res.stderr, " event=turn ") != 1 {
		t.Fatalf("out %s\nstderr %s", res.out, res.stderr)
	}
	if got := h.memberStatus(name, "bravo"); got != statusExited {
		t.Fatalf("bravo %s", got)
	}
	if got := h.session(name, "bravo"); got != "sess-bravo" {
		t.Fatalf("bravo session %q", got)
	}
	if a, b := len(launches(h.calls(), "alpha")), len(launches(h.calls(), "bravo")); a != 2 || b != 2 {
		t.Fatalf("launches alpha %d bravo %d", a, b)
	}
	msgs := h.messages(name)
	wantErrors(t, msgs, errLine("alpha", classProcessExit, 0), classBelowMinimum, protocolStopLine(t, msgs, classBelowMinimum))
	if chat, _ := h.chat(name); chat.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestStoppingDoneDuringTheMinimumStopCloses(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "3\n"
	h.plan.Launch = map[string]runFile{"charlie": {Exit: 1, ExitOn: "mode:"}}
	h.pace.grace = time.Second
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.Ticket = ""
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		h.awaitStatus(name, n, statusExited)
	}
	h.hold("bravo")
	h.send(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo", "charlie"})
	h.awaitStatus(name, "bravo", statusRunning)
	h.awaitBody(name, classBelowMinimum)
	h.send(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
	if chat, _ := h.chat(name); chat.Status != "closed" {
		t.Fatalf("status %s", chat.Status)
	}
	if got := h.memberStatus(name, "bravo"); got != statusTimedOut {
		t.Fatalf("bravo %s", got)
	}
	if countBody(h.messages(name), classBelowMinimum) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestDoneThatStopsRunningMembersSendsNoBelowMinimum(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.pace.grace = 300 * time.Millisecond
	cfg := h.base()
	cfg.Ticket = ""
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	h.hold("alpha")
	h.hold("bravo")
	h.send(name, "alpha", "turn", "mode: parallel\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "alpha", statusRunning)
	h.awaitStatus(name, "bravo", statusRunning)
	h.send(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
	if h.memberStatus(name, "alpha") != statusTimedOut || h.memberStatus(name, "bravo") != statusTimedOut {
		t.Fatalf("alpha %s bravo %s", h.memberStatus(name, "alpha"), h.memberStatus(name, "bravo"))
	}
	if countKind(h.messages(name), kindError) != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestLaunchCapStartsNoMoreAndAContinuationLaunchesAgain(t *testing.T) {
	h := newHarness(t)
	h.pace.grace = 500 * time.Millisecond
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.Ticket = ""
	cfg.MaxLaunches = 2
	cfg.MaxLaunchesSet = true
	names := []string{"alpha", "bravo", "charlie"}
	for _, n := range names {
		h.hold(n)
	}
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitBody(name, classLaunchCap)
	time.Sleep(300 * time.Millisecond)
	h.stillRunning(ch)
	if n := h.launchCount(); n != 2 {
		t.Fatalf("launches %d", n)
	}
	for _, n := range names {
		h.release(n)
	}
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), classLaunchCap) {
		t.Fatalf("err %v", res.err)
	}
	if res.out.outcome() != "error" {
		t.Fatalf("out %s", res.out)
	}
	if n := h.launchCount(); n != 2 {
		t.Fatalf("launches %d", n)
	}
	exited := 0
	for _, n := range names {
		if h.memberStatus(name, n) == statusExited && h.session(name, n) == "sess-"+n {
			exited++
		}
	}
	if exited != 2 {
		t.Fatalf("exited with a session %d", exited)
	}
	if countBody(h.messages(name), classLaunchCap) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
	if chat, _ := h.chat(name); chat.Status == "closed" {
		t.Fatal("closed")
	}

	h.send(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo", "charlie"})
	next := cfg
	next.Chat = name
	next.MaxLaunches = 10
	ch = h.goRun(cctx, next)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && h.launchCount() < 4 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := h.launchCount(); n < 4 {
		t.Fatalf("continuation launches %d", n-2)
	}
	h.send(name, "alpha", "done", "finished", nil)
	res = h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.out.outcome() != "done" {
		t.Fatalf("out %s", res.out)
	}
}

func TestMaxLaunchesBelowOneCreatesNothing(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.MaxLaunches = 0
	cfg.MaxLaunchesSet = true
	out, _, err := h.runBounded(cfg)
	if err == nil || !strings.Contains(err.Error(), "max launches") || !out.silent() || h.onlyExists() {
		t.Fatalf("err %v out %q", err, out)
	}
	if DefaultMaxLaunches != 300 {
		t.Fatalf("default %d", DefaultMaxLaunches)
	}
}

// A turn launch counts toward the cap and a watched leftover does not, and the stop waits for the leftover to end.
func TestLaunchCapCountsTurnLaunchesAndWaitsForALeftover(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	h.keptChat("alpha", "bravo", "charlie")
	launched := statusLaunched
	live := int64(pid)
	h.setMember("kept", "charlie", launched, &live)
	n, s := "charlie", "sess-charlie"
	h.setSession("kept", n, s)
	h.send("kept", "alpha", "turn", "mode: parallel\nreview", []string{"bravo"})
	cfg := h.continueKept()
	cfg.MaxLaunches = 1
	cfg.MaxLaunchesSet = true
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	h.awaitBody("kept", classLaunchCap)
	h.awaitStatus("kept", "bravo", statusExited)
	time.Sleep(500 * time.Millisecond)
	h.stillRunning(ch)
	if !alive(pid) {
		t.Fatal("the stop ended the leftover")
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = child.Process.Wait()
	killed := time.Now()
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.Contains(res.err.Error(), classLaunchCap) {
		t.Fatalf("err %v", res.err)
	}
	if took := res.ended.Sub(killed); took > 3*time.Second {
		t.Fatalf("run returned %s after the leftover ended", took)
	}
	if res.out.outcome() != "error" {
		t.Fatalf("out %s", res.out)
	}
	if a, b := len(launches(h.calls(), "alpha")), len(launches(h.calls(), "bravo")); a != 0 || b != 1 {
		t.Fatalf("launches alpha %d bravo %d", a, b)
	}
	if got := h.memberStatus("kept", "charlie"); got != statusExited {
		t.Fatalf("charlie %s", got)
	}
	if countBody(h.messages("kept"), classLaunchCap) != 1 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
}

func TestTurnAfterTheWindDownBeganIsNotTaken(t *testing.T) {
	h := newHarness(t)
	e, stderr := h.closingEngine()
	e.windDown(classBelowMinimum)
	turn := client.Message{Seq: 2, From: "alpha", Kind: kindTurn, To: []string{"zulu"}, Body: "no mode"}
	if err := e.actTurn(turn, false, turn.Seq); err != nil {
		t.Fatal(err)
	}
	// The engine is verbose, so a taken turn or a reported error would write an event.
	if e.acted != 0 || stderr.String() != "" || countKind(h.messages("kept"), kindError) != 0 {
		t.Fatalf("acted %d stderr %q msgs %+v", e.acted, stderr.String(), h.messages("kept"))
	}
	h.noLaunchOf(stderr, "bravo")
}

// The wind-down is decided before the failure is handed off, so no backup can be queued for a launch.
func TestActingSeatLostBelowMinimumQueuesNoHandoff(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	e.minimum = 2
	if err := e.writeStatus("alpha", statusLaunched, launchedProc(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	e.setLocal("alpha", statusRunning)
	if err := e.markTimeout("alpha", classProcessExit); err != nil {
		t.Fatal(err)
	}
	if e.hasHandoff() || !e.windingDown() {
		t.Fatalf("handoff %v winding down %v", e.hasHandoff(), e.windingDown())
	}
	msgs := h.messages("kept")
	if countBody(msgs, classProcessExit) != 1 || countBody(msgs, classBelowMinimum) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	if last := msgs[len(msgs)-1]; last.Body != classBelowMinimum {
		t.Fatalf("last %+v", last)
	}
}
