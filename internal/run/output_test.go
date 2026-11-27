// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRunThatEndsOnDoneReportsItsHeaderThenItsDoneAndOutcome(t *testing.T) {
	h := newHarness(t)
	h.script("alpha", on(promptBoot, says(kindDone, "owner")))
	res, name := h.play(h.base())
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stderr)
	}
	done := first(h.messages(name), "alpha", kindDone)
	header := Header{
		Chat: name, Task: "tasks:hotseat/review/ticket", Ticket: "foo-x234", Seat: "alpha",
		Members: []Member{{Name: "alpha", Module: "hotseat/grok", Model: "g1"}, {Name: "bravo", Module: "hotseat/claude", Model: "c1"}},
	}
	result := Result{Stop: &StopResult{Done: done.Seq, Outcome: outcomeDone}, Outcome: outcomeDone}
	if res.out.unheld || !reflect.DeepEqual(res.out.header, &header) || !reflect.DeepEqual(res.out.result, &result) {
		t.Fatalf("reported %s", res.out)
	}
}

func TestContinuationReportsTheStoredRosterAsMembers(t *testing.T) {
	h := newHarness(t)
	h.keptChat("alpha", "bravo")
	h.send("kept", "alpha", kindDone, "finished", []string{"owner"})
	out, stderr, err := h.runBounded(h.continueKept())
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	want := Header{Chat: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha", Members: []Member{
		{Name: "alpha", Module: "hotseat/grok", Model: "g1"}, {Name: "bravo", Module: "hotseat/claude", Model: "c1"},
	}}
	if !reflect.DeepEqual(out.header, &want) || out.outcome() != outcomeDone {
		t.Fatalf("reported %s", out)
	}
}

func TestElapsedIsTheRunsDurationRoundedToSeconds(t *testing.T) {
	h := newHarness(t)
	h.keptChat("alpha", "bravo")
	h.send("kept", "alpha", kindDone, "finished", []string{"owner"})
	cfg := h.continueKept()
	start := fixedClock()
	reads := 0
	cfg.Clock = func() time.Time {
		reads++
		if reads == 1 {
			return start
		}
		return start.Add(90*time.Second + 600*time.Millisecond)
	}
	out, stderr, err := h.runBounded(cfg)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if out.outcome() != outcomeDone || out.result.Elapsed != 91*time.Second {
		t.Fatalf("reported %s", out)
	}
}

func TestWithoutVerboseStderrCarriesNoEventLineOrSuccessfulToolStderr(t *testing.T) {
	h := newHarness(t)
	h.plan.IDNotice = "id notice\n"
	h.plan.TkNotice = "tk notice\n"
	h.script("alpha", on(promptBoot, says(kindDone, "owner")))
	cfg := h.base()
	cfg.Verbose = false
	res, _ := h.play(cfg)
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stderr)
	}
	if strings.Contains(res.stderr, "event=") || strings.Contains(res.stderr, "notice") {
		t.Fatalf("stderr\n%s", res.stderr)
	}
}

func TestVerboseEventLinesNameEachLaunchKindAndSeqAndToolStderrIsPrefixed(t *testing.T) {
	h := newHarness(t)
	h.plan.IDNotice = "id notice\nsecond line\n"
	h.plan.TkNotice = "tk notice\n"
	h.review("alpha", "bravo")
	h.script("bravo", on(promptTurn, says(kindSay, "alpha")))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stderr)
	}
	msgs := h.messages(name)
	task, turn := first(msgs, "owner", kindInstructions), first(msgs, "alpha", kindTurn)
	for _, want := range []string{
		"start: id notice\nstart: second line\n",
		eventLine("launch", "member", "alpha", "kind", "boot", "seq", task.Seq),
		eventLine("launch", "member", "bravo", "kind", "boot", "seq", task.Seq),
		eventLine("launch", "member", "bravo", "kind", "turn", "seq", turn.Seq),
		eventLine("launch", "member", "alpha", "kind", "follow", "seq", turn.Seq),
		eventLine("turn", "seq", turn.Seq, "mode", "parallel", "to", "bravo"),
	} {
		if !strings.Contains(res.stderr, want) {
			t.Fatalf("stderr has no %q\n%s", want, res.stderr)
		}
	}
	// The team has no ticket, so tk is not called.
	if strings.Contains(res.stderr, "tk notice") {
		t.Fatalf("stderr\n%s", res.stderr)
	}
	line := regexp.MustCompile(`(?m)^time=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}(Z|[+-]\d\d:\d\d) event=exit member=bravo code=0 duration=(\d+h)?(\d+m)?\d+(\.\d+)?m?s$`)
	if !line.MatchString(res.stderr) {
		t.Fatalf("no timed exit event with code and duration\n%s", res.stderr)
	}
}

func TestVerboseForwardsSuccessfulTkStderrPrefixedWithTk(t *testing.T) {
	h := newHarness(t)
	h.plan.TkNotice = "tk notice\n"
	h.script("alpha", on(promptBoot, says(kindDone, "owner")))
	res, _ := h.play(h.base())
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stderr)
	}
	if !strings.Contains(res.stderr, "tk: tk notice\n") {
		t.Fatalf("stderr\n%s", res.stderr)
	}
}

func TestContinuationOfAnIncompleteSetupReportsNothing(t *testing.T) {
	h := newHarness(t)
	h.keptChatAs(seeded("alpha", "bravo"), "")
	out, _, err := h.runBounded(h.continueKept())
	if err == nil || !strings.Contains(err.Error(), "no permission") || !out.silent() {
		t.Fatalf("err %v out %q", err, out)
	}
}

func TestErrorStopReportsTheStopClassStatusReads(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "two\n"
	h.keptChat("alpha")
	out, stderr, err := h.runBounded(h.continueKept())
	if err == nil {
		t.Fatalf("out\n%s\nstderr\n%s", out, stderr)
	}
	st := h.status("kept")
	if st.StopError == nil || st.StopError.Class != stopCommand {
		t.Fatalf("stop error %+v", st.StopError)
	}
	if !out.held() || out.stop() != nil || !out.endedBy(st.StopError.Class) {
		t.Fatalf("reported %s", out)
	}
	if n := strings.Count(stderr, eventLine("error", "class", stopCommand)); n != 1 {
		t.Fatalf("stop error events %d\n%s", n, stderr)
	}
}
