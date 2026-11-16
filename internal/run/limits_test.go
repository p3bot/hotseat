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

// awaitBody waits until a message in conv contains part.
func (h *harness) awaitBody(conv, part string) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if countBody(h.messages(conv), part) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("no %q in %+v", part, h.messages(conv))
}

// stillRunning fails when the run has returned.
func (h *harness) stillRunning(ch <-chan runResult) {
	h.t.Helper()
	select {
	case res := <-ch:
		h.t.Fatalf("run returned early: %v\n%s", res.err, res.stdout)
	default:
	}
}

// keptConversation creates kept with roster and seat alpha and publishes its instructions.
func (h *harness) keptConversation(roster ...string) {
	h.t.Helper()
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: roster,
	})
	if res.Outcome != bus.OutcomeOK {
		h.t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", roster)
}

func (h *harness) continueKept(agents string) Config {
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.Agents = agents
	cfg.AgentsSet = agents != ""
	return cfg
}

// runBounded is run with a deadline, so a run that does not stop fails instead of hanging the suite.
func (h *harness) runBounded(cfg Config) (string, string, error) {
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
	stdout, _, err := h.runBounded(cfg)
	if err == nil || !strings.Contains(err.Error(), "requires 2") {
		t.Fatalf("err %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout %s", stdout)
	}
	if h.onlyExists() {
		t.Fatal("conversation created")
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
			stdout, _, err := h.runBounded(h.base())
			if err == nil || !strings.Contains(err.Error(), "not a positive integer") {
				t.Fatalf("err %v", err)
			}
			if stdout != "" || h.onlyExists() {
				t.Fatalf("stdout %q created %v", stdout, h.onlyExists())
			}
		})
	}
}

func TestTaskWithoutMinimumRunsWithOneMember(t *testing.T) {
	h := newHarness(t)
	h.grace = time.Second
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1"
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.publish(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
}

func TestContinueBelowTaskMinimumWithoutDoneFails(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptConversation("alpha")
	stdout, _, err := h.runBounded(h.continueKept(""))
	if err == nil || !strings.Contains(err.Error(), "requires 2") {
		t.Fatalf("err %v", err)
	}
	if !strings.HasSuffix(stdout, "outcome: error\n") {
		t.Fatalf("stdout %s", stdout)
	}
	if conv, _ := h.conv("kept"); conv.Status == "closed" {
		t.Fatal("closed")
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
}

func TestContinueWithStoredDoneReportsDoneUnderARisenMinimum(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptConversation("alpha")
	h.publish("kept", "alpha", "done", "finished", nil)
	stdout, stderr, err := h.runBounded(h.continueKept(""))
	if err != nil {
		t.Fatal(err, stderr)
	}
	if !strings.HasSuffix(stdout, "outcome: done\n") || strings.Contains(stderr, "requires") {
		t.Fatalf("stdout %s stderr %s", stdout, stderr)
	}
	if conv, _ := h.conv("kept"); conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
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
	h.keptConversation("alpha", "bravo")
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched, PID: &deadPID})
	h.publish("kept", "alpha", "done", "finished", nil)
	stdout, stderr, err := h.runBounded(h.continueKept("alpha=hotseat/grok:g1 bravo=hotseat/claude:c1"))
	if err != nil {
		t.Fatal(err, stderr)
	}
	if !strings.HasSuffix(stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", stdout)
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
	h.keptConversation("alpha", "bravo")
	launched, timedOut := statusLaunched, statusTimedOut
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &timedOut})
	h.publish("kept", "alpha", "turn", "mode: parallel\nreview", []string{"alpha"})
	stdout, _, err := h.runBounded(h.continueKept("alpha=hotseat/grok:g1 bravo=hotseat/claude:c1"))
	if err == nil || !strings.Contains(err.Error(), classBelowMinimum) {
		t.Fatalf("err %v", err)
	}
	if !strings.HasSuffix(stdout, "outcome: error\n") || strings.Contains(stdout, "turn:") {
		t.Fatalf("stdout %s", stdout)
	}
	if countBody(h.messages("kept"), classBelowMinimum) != 1 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
	if n := h.launchCount(); n != 0 {
		t.Fatalf("launches %d", n)
	}
	if conv, _ := h.conv("kept"); conv.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestResumeThatCannotLaunchDoesNotApplyTheMinimum(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.keptConversation("alpha", "bravo")
	launched, timedOut := statusLaunched, statusTimedOut
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &timedOut})
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.continueKept(""))
	time.Sleep(500 * time.Millisecond)
	h.stillRunning(ch)
	h.publish("kept", "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if countBody(h.messages("kept"), classBelowMinimum) != 0 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
}

func TestMemberLostBelowMinimumLetsTheOtherFinishAndStops(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.plan.Launch = map[string]runFile{"alpha": {Exit: 1, ExitOn: "mode:"}}
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "bravo", statusRunning)
	h.awaitBody(name, classBelowMinimum)
	if got := h.memberStatus(name, "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	// A turn from the finishing member during the stop launches nothing and stops nobody.
	h.publish(name, "bravo", "turn", "mode: serial\nnext", []string{"alpha", "bravo"})
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
	if !strings.HasSuffix(res.stdout, "outcome: error\n") || strings.Count(res.stdout, "turn:") != 1 {
		t.Fatalf("stdout %s", res.stdout)
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
	if countKind(msgs, kindError) != 2 || countBody(msgs, classBelowMinimum) != 1 || countBody(msgs, classProcessExit) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	if conv, _ := h.conv(name); conv.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestStoppingDoneDuringTheMinimumStopCloses(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "3\n"
	h.plan.Launch = map[string]runFile{"charlie": {Exit: 1, ExitOn: "mode:"}}
	h.grace = time.Second
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		h.awaitStatus(name, n, statusExited)
	}
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo", "charlie"})
	h.awaitStatus(name, "bravo", statusRunning)
	h.awaitBody(name, classBelowMinimum)
	h.publish(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
	if conv, _ := h.conv(name); conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
	if got := h.memberStatus(name, "bravo"); got != statusTimedOut {
		t.Fatalf("bravo %s", got)
	}
	if countBody(h.messages(name), classBelowMinimum) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestDoneThatStopsRunningMembersPublishesNoBelowMinimum(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	h.grace = 300 * time.Millisecond
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	h.hold("alpha")
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "alpha", statusRunning)
	h.awaitStatus(name, "bravo", statusRunning)
	h.publish(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
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
	h.grace = 500 * time.Millisecond
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
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
	if !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("stdout %s", res.stdout)
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
	if conv, _ := h.conv(name); conv.Status == "closed" {
		t.Fatal("closed")
	}

	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo", "charlie"})
	next := cfg
	next.Conversation = name
	next.ConversationSet = true
	next.TaskSet = false
	next.MaxLaunches = 10
	ch = h.goRun(cctx, next)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && h.launchCount() < 4 {
		time.Sleep(20 * time.Millisecond)
	}
	if n := h.launchCount(); n < 4 {
		t.Fatalf("continuation launches %d", n-2)
	}
	h.publish(name, "alpha", "done", "finished", nil)
	res = h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
}

func TestMaxLaunchesBelowOneCreatesNothing(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.MaxLaunches = 0
	cfg.MaxLaunchesSet = true
	stdout, _, err := h.runBounded(cfg)
	if err == nil || !strings.Contains(err.Error(), "max launches") || stdout != "" || h.onlyExists() {
		t.Fatalf("err %v stdout %q", err, stdout)
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
	h.keptConversation("alpha", "bravo", "charlie")
	launched := statusLaunched
	live := int64(pid)
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "charlie", Status: &launched, PID: &live})
	n, s := "charlie", "sess-charlie"
	h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	h.publish("kept", "alpha", "turn", "mode: parallel\nreview", []string{"bravo"})
	cfg := h.continueKept("alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2")
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
	if !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("stdout %s", res.stdout)
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
	e, stdout := h.closingEngine()
	e.windDown(classBelowMinimum)
	turn := client.Message{Seq: 2, From: "alpha", Kind: kindTurn, To: []string{"zulu"}, Body: "no mode"}
	if err := e.actTurn(turn, false, turn.Seq); err != nil {
		t.Fatal(err)
	}
	if e.acted != 0 || stdout.String() != "" || countKind(h.messages("kept"), kindError) != 0 {
		t.Fatalf("acted %d stdout %q msgs %+v", e.acted, stdout.String(), h.messages("kept"))
	}
	h.noLaunchOf(stdout, "bravo")
}

// The wind-down is decided before the failure is handed off, so no backup can be queued for a launch.
func TestActingSeatLostBelowMinimumQueuesNoHandoff(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	e.minimum = 2
	if err := e.writeStatus("alpha", statusLaunched, int64(os.Getpid()), true); err != nil {
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
