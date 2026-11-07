// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

type runResult struct {
	stdout string
	stderr string
	err    error
	ended  time.Time
}

func (h *harness) goRun(ctx context.Context, cfg Config) <-chan runResult {
	h.t.Helper()
	ch := make(chan runResult, 1)
	h.save()
	go func() {
		var stdout, stderr lockedBuffer
		err := executeWithGrace(ctx, cfg, h.grace, &stdout, &stderr)
		ch <- runResult{stdout: stdout.String(), stderr: stderr.String(), err: err, ended: time.Now()}
	}()
	return ch
}

func (h *harness) awaitRun(ch <-chan runResult, limit time.Duration) runResult {
	h.t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(limit):
		h.t.Fatalf("run did not return within %s", limit)
		return runResult{}
	}
}

func (h *harness) awaitStatus(conv, name, status string) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(conv, name) == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("%s is %s, not %s", name, h.memberStatus(conv, name), status)
}

func (h *harness) session(conv, name string) string {
	h.t.Helper()
	n := name
	res := h.do("session", client.SessionRequest{Conversation: conv, Name: &n})
	if res.Outcome != bus.OutcomeOK || res.Session == nil {
		h.t.Fatalf("session %s %s", name, res.Reason)
	}
	return res.Session.Session
}

func exitLine(name string, code string) string {
	return "exit: " + name + "\ncode: " + code + "\n"
}

func TestDoneSenderThatExitsInTheGraceIsExitedWithItsSession(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	h.grace = 10 * time.Second
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.publish(name, "alpha", "done", "finished", nil)
	time.Sleep(300 * time.Millisecond)
	h.release("alpha")
	released := time.Now()
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.ended.Sub(released) > 3*time.Second {
		t.Fatalf("run returned %s after the exit", res.ended.Sub(released))
	}
	if got := h.memberStatus(name, "alpha"); got != statusExited {
		t.Fatalf("alpha %s", got)
	}
	if got := h.session(name, "alpha"); got != "sess-alpha" {
		t.Fatalf("alpha session %q", got)
	}
	if !strings.Contains(res.stdout, exitLine("alpha", "0")) || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
	if countKind(h.messages(name), kindError) != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestDoneSenderThatHangsIsKilledAtTheEndOfTheGrace(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	h.grace = 500 * time.Millisecond
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	// Taken before the publish, so the grace cannot start before it.
	published := time.Now()
	h.publish(name, "alpha", "done", "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if took := res.ended.Sub(published); took < h.grace || took > 4*time.Second {
		t.Fatalf("run returned %s after done", took)
	}
	if got := h.memberStatus(name, "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	if !strings.Contains(res.stdout, exitLine("alpha", "-1")) || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
	if countKind(h.messages(name), kindError) != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestMemberDueAfterTheDoneSenderInASerialTurnIsNotLaunched(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	h.grace = 10 * time.Second
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		h.awaitStatus(name, n, statusExited)
	}
	h.hold("alpha")
	h.publish(name, "charlie", "turn", "mode: serial\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "alpha", statusRunning)
	h.publish(name, "alpha", "done", "finished", nil)
	time.Sleep(500 * time.Millisecond)
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if n := len(launches(h.calls(), "bravo")); n != 1 {
		t.Fatalf("bravo launches %d", n)
	}
	// The boot writes launched and exited. A launch for the turn would add running and a second exit line.
	if strings.Count(res.stdout, "member: bravo\n") != 2 || strings.Count(res.stdout, "exit: bravo\n") != 1 {
		t.Fatalf("stdout %s", res.stdout)
	}
	for _, c := range h.calls() {
		if strings.Contains(c.Stdin, "The set has finished.") {
			t.Fatal("follow-up launched after done")
		}
	}
	if got := h.memberStatus(name, "alpha"); got != statusExited {
		t.Fatalf("alpha %s", got)
	}
	if !strings.Contains(res.stdout, exitLine("alpha", "0")) {
		t.Fatalf("stdout %s", res.stdout)
	}
}

func TestOtherRunningMemberIsStoppedWithoutWaitingForTheDoneSender(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	h.grace = 10 * time.Second
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		h.awaitStatus(name, n, statusExited)
	}
	h.hold("alpha")
	h.hold("bravo")
	h.publish(name, "charlie", "turn", "mode: parallel\nreview", []string{"alpha", "bravo"})
	h.awaitStatus(name, "alpha", statusRunning)
	h.awaitStatus(name, "bravo", statusRunning)
	h.publish(name, "alpha", "done", "finished", nil)
	published := time.Now()
	h.awaitStatus(name, "bravo", statusTimedOut)
	if took := time.Since(published); took > 3*time.Second {
		t.Fatalf("bravo stopped %s after done", took)
	}
	if got := h.memberStatus(name, "alpha"); got != statusRunning {
		t.Fatalf("alpha %s while bravo stopped", got)
	}
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if got := h.memberStatus(name, "alpha"); got != statusExited {
		t.Fatalf("alpha %s", got)
	}
	if !strings.Contains(res.stdout, exitLine("alpha", "0")) || !strings.Contains(res.stdout, exitLine("bravo", "-1")) {
		t.Fatalf("stdout %s", res.stdout)
	}
	if countKind(h.messages(name), kindError) != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestBackupActingAsSeatGetsTheDoneGrace(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {Exit: 1, Sleep: "1s"},
		"bravo": {Register: true},
	}
	cfg := h.base()
	cfg.TicketSet = false
	h.grace = 10 * time.Second
	h.hold("bravo")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitRegistered(name, "bravo")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && countBody(h.messages(name), classProcessExit) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if h.memberStatus(name, "alpha") != statusTimedOut {
		t.Fatalf("alpha %s", h.memberStatus(name, "alpha"))
	}
	h.publish(name, "bravo", "done", "finished", nil)
	time.Sleep(300 * time.Millisecond)
	h.release("bravo")
	released := time.Now()
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if res.ended.Sub(released) > 3*time.Second {
		t.Fatalf("run returned %s after the exit", res.ended.Sub(released))
	}
	if got := h.memberStatus(name, "bravo"); got != statusExited {
		t.Fatalf("bravo %s", got)
	}
	if got := h.session(name, "bravo"); got != "sess-bravo" {
		t.Fatalf("bravo session %q", got)
	}
	if n := len(launches(h.calls(), "bravo")); n != 1 {
		t.Fatalf("bravo launches %d", n)
	}
	if !strings.Contains(res.stdout, exitLine("bravo", "0")) || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
}

func TestLeftoverDoneSenderThatExitsInTheGraceFollowsTheShutdownRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session bool
		want    string
	}{
		{"stored session", true, statusExited},
		{"no session", false, statusTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			child := exec.Command("sleep", "1")
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			pid := child.Process.Pid
			t.Cleanup(func() {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_, _ = child.Process.Wait()
			})
			live := int64(pid)
			res := h.do("create", client.CreateRequest{
				Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
				Directory: h.dir, Roster: []string{"alpha"},
			})
			if res.Outcome != bus.OutcomeOK {
				t.Fatal(res.Reason)
			}
			h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha"})
			launched := statusLaunched
			h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
			if tc.session {
				n := "alpha"
				s := "sess-alpha"
				h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
			}
			h.publish("kept", "alpha", "done", "yes", nil)
			cfg := h.base()
			cfg.Conversation = "kept"
			cfg.ConversationSet = true
			cfg.TaskSet = false
			cfg.TicketSet = false
			cfg.AgentsSet = false
			h.grace = 10 * time.Second
			start := time.Now()
			stdout, stderr, err := h.run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err, stdout, stderr)
			}
			if took := time.Since(start); took > 4*time.Second {
				t.Fatalf("run took %s", took)
			}
			state, err := child.Process.Wait()
			if err != nil {
				t.Fatal(err)
			}
			if !state.Success() {
				t.Fatalf("leftover did not exit on its own: %s", state)
			}
			if got := h.member("kept", "alpha").Status; got != tc.want {
				t.Fatalf("alpha %s", got)
			}
			if countKind(h.messages("kept"), kindError) != 0 {
				t.Fatalf("msgs %+v", h.messages("kept"))
			}
			if !strings.Contains(stdout, "outcome: done") {
				t.Fatalf("stdout %s", stdout)
			}
		})
	}
}

func TestCancelledCallerEndsTheDoneGraceAtOnce(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	h.grace = 20 * time.Second
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	row := h.member(name, "alpha")
	if row.PID == nil {
		t.Fatal("alpha has no pid")
	}
	h.publish(name, "alpha", "done", "finished", nil)
	time.Sleep(300 * time.Millisecond)
	cancel()
	cancelled := time.Now()
	res := h.awaitRun(ch, 8*time.Second)
	if took := res.ended.Sub(cancelled); took > 2*time.Second {
		t.Fatalf("run returned %s after cancel", took)
	}
	if alive(int(*row.PID)) {
		t.Fatal("left the done sender running")
	}
}

// closingEngine returns an engine on conversation kept with roster alpha and bravo, seat alpha, as drive would hold it.
func (h *harness) closingEngine() (*engine, *lockedBuffer) {
	h.t.Helper()
	h.save()
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		h.t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	var stdout lockedBuffer
	e, err := newEngine(context.Background(), h.base(), h.grace, &stdout, io.Discard)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(e.cancel)
	e.roster, err = parseAgents(e.cfg.Agents)
	if err != nil {
		h.t.Fatal(err)
	}
	e.name = "kept"
	e.seat = "alpha"
	e.dir = h.dir
	e.started = true
	return e, &stdout
}

func (h *harness) noLaunchOf(stdout *lockedBuffer, name string) {
	h.t.Helper()
	if n := len(launches(h.calls(), name)); n != 0 {
		h.t.Fatalf("%s launches %d", name, n)
	}
	if res := h.do("member", client.MemberRequest{Conversation: "kept", Name: name}); res.Reason != bus.ReasonMemberMissing {
		h.t.Fatalf("%s row %+v %s", name, res.Member, res.Reason)
	}
	if out := stdout.String(); out != "" {
		h.t.Fatalf("stdout %s", out)
	}
}

func TestLaunchThatFindsTheRunClosingStartsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch func(e *engine) error
	}{
		{"follow-up", func(e *engine) error {
			return e.launchMember("bravo", launchFollow, e.followPrompt("bravo"))
		}},
		{"handoff", func(e *engine) error {
			return e.launchMember("bravo", launchFollow, e.handoffPrompt("alpha", "bravo"))
		}},
		// The answer check reads the transcript on the cancelled run context, so the closing rule must come first.
		{"turn", func(e *engine) error {
			turn := client.Message{Seq: 1, From: "alpha", Kind: kindTurn, To: []string{"bravo"}}
			return e.launchForTurn("bravo", turn, e.turnPrompt("bravo", turn, modeSerial, ""))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			e, stdout := h.closingEngine()
			if err := e.finishDone("alpha"); err != errDone {
				t.Fatalf("finishDone %v", err)
			}
			if err := tc.launch(e); err != nil {
				t.Fatal(err)
			}
			h.noLaunchOf(stdout, "bravo")
		})
	}
}

func TestDoneSeenDuringLaunchSetupStartsNothing(t *testing.T) {
	h := newHarness(t)
	e, stdout := h.closingEngine()
	err := e.launchUnless("bravo", launchFollow, e.followPrompt("bravo"), func() (bool, error) {
		if err := e.finishDone("alpha"); err != errDone {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.noLaunchOf(stdout, "bravo")
}

func TestLaunchAfterTheRunStoppedWithoutDoneStartsNothing(t *testing.T) {
	h := newHarness(t)
	e, stdout := h.closingEngine()
	e.cancel()
	err := e.launchMember("bravo", launchFollow, e.followPrompt("bravo"))
	if !errors.Is(err, errStopped) {
		t.Fatalf("launch %v", err)
	}
	h.noLaunchOf(stdout, "bravo")
}

func TestLeftoverThatExitsWhileTheRunClosesPublishesNoError(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	dead := exec.Command("sleep", "0")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	pid := dead.Process.Pid
	if err := dead.Wait(); err != nil {
		t.Fatal(err)
	}
	launched := statusLaunched
	p := int64(pid)
	row := h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &p})
	if row.Member == nil {
		t.Fatal(row.Reason)
	}
	e.setLocal("alpha", statusLaunched)
	// Closing without the cancel is the window where the exit is seen before the stop.
	e.mu.Lock()
	e.closing = true
	e.doneFrom = "alpha"
	e.mu.Unlock()
	if err := e.watchLocked(liveProc{name: "alpha", pid: pid, launched: row.Member.Launched}); err != nil {
		t.Fatal(err)
	}
	if got := h.memberStatus("kept", "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	if countKind(h.messages("kept"), kindError) != 0 || e.hasHandoff() {
		t.Fatalf("msgs %+v handoff %v", h.messages("kept"), e.hasHandoff())
	}
}

func TestDoneSenderThatFailsInTheGraceHandsOffToNobody(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{"alpha": {Exit: 1}}
	cfg := h.base()
	cfg.TicketSet = false
	h.grace = 10 * time.Second
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.publish(name, "alpha", "done", "finished", nil)
	time.Sleep(300 * time.Millisecond)
	h.release("alpha")
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil {
		t.Fatal(res.err, res.stderr)
	}
	if got := h.memberStatus(name, "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	msgs := h.messages(name)
	if countBody(msgs, classProcessExit) != 1 || countBody(msgs, classNoBackup) != 0 {
		t.Fatalf("msgs %+v", msgs)
	}
	for _, c := range h.calls() {
		if strings.Contains(c.Stdin, "acting seat") {
			t.Fatalf("handoff %q", c.Stdin)
		}
	}
	if !strings.Contains(res.stdout, exitLine("alpha", "1")) || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("stdout %s", res.stdout)
	}
}
