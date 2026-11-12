// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// Text that tells one launch kind from another in a member prompt.
const (
	promptBoot       = "Register with hotseat member register"
	promptTurn       = "mode: "
	promptFollow     = "The set has finished"
	promptDiscussion = "This is a discussion launch"
	promptStall      = "This is a stall launch"
	promptFix        = "This is a fix launch"
	promptReport     = "This is a report launch"
	promptAggregate  = "This is an aggregate launch"
)

// script appends launch steps for name. Every scripted member registers on each launch.
func (h *harness) script(name string, steps ...stepFile) {
	if h.plan.Launch == nil {
		h.plan.Launch = map[string]runFile{}
	}
	r := h.plan.Launch[name]
	r.Register = true
	r.Steps = append(r.Steps, steps...)
	h.plan.Launch[name] = r
}

func on(prompt string, msgs ...messageFile) stepFile {
	return stepFile{On: prompt, Publish: msgs}
}

func says(kind string, to ...string) messageFile {
	return messageFile{Kind: kind, To: to, Body: kind}
}

// team is a fresh conversation with roster names, each on a fake module, and seat.
func (h *harness) team(seat string, names ...string) Config {
	cfg := h.base()
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=hotseat/grok:m"
	}
	cfg.Agents = strings.Join(parts, " ")
	cfg.Seat = seat
	cfg.TicketSet = false
	return cfg
}

// play runs cfg to its end and returns the result and the only conversation's name.
func (h *harness) play(cfg Config) (runResult, string) {
	h.t.Helper()
	cctx, cancel := ctx()
	defer cancel()
	res := h.awaitRun(h.goRun(cctx, cfg), 25*time.Second)
	return res, h.only().Name
}

// review scripts the seat's boot to post one parallel review turn to names.
func (h *harness) review(seat string, names ...string) {
	h.script(seat, stepFile{On: promptBoot, Publish: []messageFile{{Kind: kindTurn, To: names, Body: "mode: parallel\nreview"}}})
}

func first(msgs []client.Message, from, kind string) client.Message {
	for _, m := range msgs {
		if m.From == from && m.Kind == kind {
			return m
		}
	}
	return client.Message{}
}

func ofKind(msgs []client.Message, kind string) []client.Message {
	var out []client.Message
	for _, m := range msgs {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func errorBodies(msgs []client.Message) []string {
	var out []string
	for _, m := range ofKind(msgs, kindError) {
		out = append(out, m.Body)
	}
	return out
}

// launchesWith are the launches of name whose prompt holds text, in call order.
func (h *harness) launchesWith(name, text string) []call {
	var out []call
	for _, c := range launches(h.calls(), name) {
		if strings.Contains(c.Stdin, text) {
			out = append(out, c)
		}
	}
	return out
}

// callIndex is the position of the first launch of name whose prompt holds text, or -1.
func (h *harness) callIndex(name, text string) int {
	for i, c := range h.calls() {
		if c.Kind == "launch" && strings.Contains(c.Stdin, "You are "+name+" ") && strings.Contains(c.Stdin, text) {
			return i
		}
	}
	return -1
}

func wantErrors(t *testing.T, msgs []client.Message, want ...string) {
	t.Helper()
	got := errorBodies(msgs)
	if slices.Compare(got, want) != 0 {
		t.Fatalf("errors %q, want %q", got, want)
	}
}

// promptStart is the start seq a discussion or stall prompt names.
func promptStart(t *testing.T, prompt string) int64 {
	t.Helper()
	_, rest, ok := strings.Cut(prompt, "\nstart: ")
	if !ok {
		t.Fatalf("no start in %q", prompt)
	}
	line, _, _ := strings.Cut(rest, "\n")
	n, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func seqOf(m client.Message) string {
	return strconv.FormatInt(m.Seq, 10)
}

var abcRoster = []string{"alpha", "bravo", "charlie"}

func TestSetWithNoFindingsPublishesDiscussThenTheFollowUp(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "bravo", "charlie")
	h.script("bravo", on(promptTurn, says(kindSay, "alpha")))
	h.script("charlie", on(promptTurn, says(kindSay, "alpha")))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	turn := first(msgs, "alpha", kindTurn)
	discuss := first(msgs, fromRun, kindDiscuss)
	done := first(msgs, "alpha", kindDone)
	if discuss.Body != seqOf(turn) || len(discuss.To) != 0 || discuss.Seq > done.Seq {
		t.Fatalf("discuss %+v turn %d done %d", discuss, turn.Seq, done.Seq)
	}
	follow := h.launchesWith("alpha", promptFollow)
	if len(follow) != 1 || !strings.Contains(follow[0].Stdin, "fixed: 0\n") {
		t.Fatalf("follow-ups %+v", follow)
	}
	wantErrors(t, msgs)
}

func TestTwoFindingsAreDiscussedInOrderAndFixedBeforeTheFollowUp(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "alpha", "bravo", "charlie")
	h.script("alpha",
		on(promptTurn, says(kindSay, "alpha")),
		on(promptAggregate, messageFile{Kind: kindAggregate, To: abcRoster, Body: "$lines:findings"}),
		on(promptDiscussion, says(kindAgree, abcRoster...)),
		on(promptDiscussion, says(kindAgree, abcRoster...)),
		stepFile{On: promptFollow, Publish: []messageFile{{Kind: kindDone, To: []string{"owner"}, Body: "outcome: hold\nstub"}}},
	)
	h.script("bravo",
		on(promptTurn, says(kindFinding, abcRoster...), says(kindFinding, abcRoster...), says(kindSay, "alpha")),
		on(promptFix, says(kindFixed, abcRoster...)),
		on(promptFix, says(kindFixed, abcRoster...)),
	)
	h.script("charlie",
		on(promptTurn, says(kindSay, "alpha")),
		on(promptDiscussion, says(kindAgree, abcRoster...)),
		on(promptDiscussion, says(kindAgree, abcRoster...)),
	)
	res, name := h.play(h.team("alpha", abcRoster...))
	// A hold done with every finding resolved still exits zero.
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	found := ofKind(msgs, kindFinding)
	discuss := first(msgs, fromRun, kindDiscuss)
	issues := first(msgs, fromRun, kindIssues)
	settled := ofKind(msgs, kindSettled)
	fixed := ofKind(msgs, kindFixed)
	if len(found) != 2 || len(settled) != 2 || len(fixed) != 2 {
		t.Fatalf("findings %d settled %d fixed %d", len(found), len(settled), len(fixed))
	}
	f1, f2 := found[0], found[1]
	if discuss.Seq < f2.Seq ||
		settled[0].Body != settledBody(f1.Seq, f1.Seq) || settled[1].Body != settledBody(f2.Seq, f2.Seq) ||
		!slices.IsSorted([]int64{settled[0].Seq, fixed[0].Seq, settled[1].Seq, fixed[1].Seq}) {
		t.Fatalf("order: %+v", msgs)
	}
	// Each discussion runs to its end before the next opens.
	f2First := h.callIndex("charlie", "finding: "+seqOf(f2))
	if f2First < h.callIndex("bravo", promptFix) || f2First < h.callIndex("alpha", "finding: "+seqOf(f1)) {
		t.Fatalf("second discussion overlapped the first: %+v", h.calls())
	}
	// Each prompt names where its own discussion begins, so the first discussion's stances are history in the second.
	if c := h.launchesWith("charlie", promptDiscussion); len(c) != 2 ||
		promptStart(t, c[0].Stdin) != issues.Seq || promptStart(t, c[1].Stdin) != fixed[0].Seq {
		t.Fatalf("charlie discussion prompts %+v", c)
	}
	follow := h.launchesWith("alpha", promptFollow)
	if len(follow) != 1 || !strings.Contains(follow[0].Stdin, "fixed: 2\n") || h.callIndex("alpha", promptFollow) < h.callIndex("charlie", "finding: "+seqOf(f2)) {
		t.Fatalf("follow-up %+v", follow)
	}
	for _, part := range []string{
		"finding: " + seqOf(f1) + "\nfrom: bravo\n",
		"stance: agree\nfrom: charlie\nseq: ",
		"settled: " + seqOf(f1) + "\n",
		"fixed: " + seqOf(f1) + "\nfrom: bravo\n",
		"settled: " + seqOf(f2) + "\n",
	} {
		if !strings.Contains(res.stdout, part) {
			t.Fatalf("stdout has no %q\n%s", part, res.stdout)
		}
	}
	wantErrors(t, msgs)
}

func TestSoleLiveMemberSettlesItsFindingWithoutADiscussionLaunch(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "alpha", "bravo")
	h.script("alpha",
		on(promptTurn, says(kindFinding, "alpha", "bravo"), says(kindSay, "alpha")),
		on(promptFix, says(kindFixed, "alpha", "bravo")),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("bravo", stepFile{On: promptTurn, Exit: 1})
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if n := len(h.launchesWith("alpha", promptDiscussion)) + len(h.launchesWith("bravo", promptDiscussion)); n != 0 {
		t.Fatalf("discussion launches %d", n)
	}
	msgs := h.messages(name)
	f := first(msgs, "alpha", kindFinding)
	if first(msgs, fromRun, kindSettled).Body != settledBody(f.Seq, f.Seq) || len(ofKind(msgs, kindFixed)) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
}

// disputed scripts the worked example: a2 proposes bar over a1's foo, a1 objects until both budgets run out, and a3 agrees.
func disputed(h *harness) {
	all := []string{"a1", "a2", "a3"}
	h.review("a3", all...)
	h.script("a1", on(promptTurn, says(kindFinding, all...), says(kindSay, "a3")))
	for i := 0; i < 4; i++ {
		h.script("a1", on(promptDiscussion, says(kindObject, all...)))
	}
	h.script("a2",
		on(promptTurn, says(kindSay, "a3")),
		on(promptDiscussion, says(kindPropose, all...), says(kindPass, "a1")),
	)
	h.script("a3",
		on(promptTurn, says(kindSay, "a3")),
		on(promptDiscussion, says(kindAgree, all...)),
	)
}

func TestDisputedFindingStallsOnceAndSettlesOnTheAmendedProposal(t *testing.T) {
	h := newHarness(t)
	disputed(h)
	all := []string{"a1", "a2", "a3"}
	for i := 0; i < 4; i++ {
		h.script("a2", on(promptDiscussion, says(kindStand, all...)))
	}
	h.script("a1", on(promptDiscussion, says(kindAgree, all...)), on(promptFix, says(kindFixed, all...)))
	h.script("a3",
		on(promptStall, says(kindExtend, "a1", "a2")),
		on(promptFollow, says(kindDone, "owner")),
	)
	res, name := h.play(h.team("a3", all...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	f := first(msgs, "a1", kindFinding)
	bar := first(msgs, "a2", kindPropose)
	if s := ofKind(msgs, kindSettled); len(s) != 1 || s[0].Body != settledBody(f.Seq, bar.Seq) {
		t.Fatalf("settled %+v", s)
	}
	if fx := ofKind(msgs, kindFixed); len(fx) != 1 || fx[0].From != "a1" {
		t.Fatalf("fixed %+v", fx)
	}
	if strings.Count(res.stdout, "stall: ") != 1 || len(h.launchesWith("a3", promptStall)) != 1 {
		t.Fatalf("stalls\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "pass: a1\nfrom: a2\n") || !strings.Contains(res.stdout, "extend: "+seqOf(f)+"\nto: a1\nto: a2\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
	var stances []string
	for _, line := range strings.Split(res.stdout, "\n") {
		if kind, ok := strings.CutPrefix(line, "stance: "); ok {
			stances = append(stances, kind)
		}
	}
	want := []string{"propose", "object", "agree", "stand", "object", "stand", "object", "stand", "object", "stand", "agree"}
	if slices.Compare(stances, want) != 0 {
		t.Fatalf("stances %v", stances)
	}
	if n := len(h.launchesWith("a1", promptDiscussion)); n != 5 {
		t.Fatalf("a1 discussion launches %d", n)
	}
	wantErrors(t, msgs)
}

func TestDeadlockHaltsReportsAndExitsUnresolved(t *testing.T) {
	h := newHarness(t)
	disputed(h)
	all := []string{"a1", "a2", "a3"}
	h.script("a3",
		on(promptStall, says(kindDeadlock, "a1", "a2", "a3", "owner")),
		stepFile{On: promptReport, Publish: []messageFile{{Kind: kindDone, To: []string{"owner"}, Body: "outcome: hold\nreport"}}},
	)
	cfg := h.team("a3", all...)
	cfg.MaxSays, cfg.MaxSaysSet = 1, true
	res, name := h.play(cfg)
	if !errors.Is(res.err, ErrUnresolved) {
		t.Fatalf("err %v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	f := first(msgs, "a1", kindFinding)
	if first(msgs, fromRun, kindHalted).Body != seqOf(f) {
		t.Fatalf("halted %+v", msgs)
	}
	if !strings.HasSuffix(res.stdout, "unsettled: "+seqOf(f)+"\noutcome: deadlock\n") || !strings.Contains(res.stdout, "deadlock: "+seqOf(f)+"\nfrom: a3\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
	report := h.launchesWith("a3", promptReport)
	if len(report) != 1 || !strings.Contains(report[0].Stdin, "finding: "+seqOf(f)+"\n") || !strings.Contains(report[0].Stdin, "disagree: a1\n") {
		t.Fatalf("report %+v", report)
	}
	if len(h.launchesWith("a1", promptFix)) != 0 || len(h.launchesWith("a3", promptFollow)) != 0 {
		t.Fatal("a deadlock launched a fix or a follow-up")
	}
	// A deadlock is reported by the done and the exit status, not by an unsettled error.
	wantErrors(t, msgs)
	if c, _ := h.conv(name); c.Status != "closed" {
		t.Fatalf("status %s", c.Status)
	}
}

// earlyDone has the seat post done in place of its discussion stance on bravo's finding.
func earlyDone(h *harness) {
	h.review("alpha", "bravo")
	h.script("bravo", on(promptTurn, says(kindFinding, "alpha", "bravo"), says(kindSay, "alpha")))
	h.script("alpha", on(promptDiscussion, says(kindDone, "owner")))
}

func TestEarlyDoneWithAnOpenFindingExitsUnsettledAndAReplayMatches(t *testing.T) {
	h := newHarness(t)
	earlyDone(h)
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if !errors.Is(res.err, ErrUnresolved) {
		t.Fatalf("err %v\n%s", res.err, res.stdout)
	}
	f := first(h.messages(name), "bravo", kindFinding)
	tail := "unsettled: " + seqOf(f) + "\nerror: unsettled\noutcome: unsettled\n"
	if !strings.HasSuffix(res.stdout, tail) {
		t.Fatalf("stdout\n%s", res.stdout)
	}
	wantErrors(t, h.messages(name), classUnsettled)
	for _, agents := range []string{"alpha=hotseat/grok:m bravo=hotseat/grok:m", ""} {
		cfg := h.continueKept(agents)
		cfg.Conversation = name
		stdout, _, err := h.runBounded(cfg)
		if !errors.Is(err, ErrUnresolved) || !strings.HasSuffix(stdout, "unsettled: "+seqOf(f)+"\noutcome: unsettled\n") {
			t.Fatalf("replay %v\n%s", err, stdout)
		}
		wantErrors(t, h.messages(name), classUnsettled)
	}
}

func TestSettledChangeWithNoFixedAtTheStopIsUnfixed(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "bravo")
	h.script("bravo", on(promptTurn, says(kindFinding, "alpha", "bravo"), says(kindSay, "alpha")), on(promptFix))
	h.script("alpha", on(promptDiscussion, says(kindAgree, "alpha", "bravo")), on(promptFix, says(kindDone, "owner")))
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	f := first(h.messages(name), "bravo", kindFinding)
	if !errors.Is(res.err, ErrUnresolved) || !strings.HasSuffix(res.stdout, "unfixed: "+seqOf(f)+"\nerror: unsettled\noutcome: unsettled\n") {
		t.Fatalf("err %v\n%s", res.err, res.stdout)
	}
}

func TestFixedStoredAfterTheStoppingDoneDoesNotCount(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "alpha", "bravo")
	h.script("alpha",
		on(promptTurn, says(kindFinding, "alpha", "bravo"), says(kindSay, "alpha")),
		on(promptFix, says(kindDone, "owner"), says(kindFixed, "alpha", "bravo")),
	)
	h.script("bravo", on(promptTurn, says(kindSay, "alpha")), on(promptDiscussion, says(kindAgree, "alpha", "bravo")))
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	msgs := h.messages(name)
	f := first(msgs, "alpha", kindFinding)
	if first(msgs, "alpha", kindFixed).Seq < first(msgs, "alpha", kindDone).Seq {
		t.Fatalf("fixed before done: %+v", msgs)
	}
	want := "unfixed: " + seqOf(f) + "\n"
	if !errors.Is(res.err, ErrUnresolved) || !strings.Contains(res.stdout, want) {
		t.Fatalf("err %v\n%s", res.err, res.stdout)
	}
	cfg := h.continueKept("")
	cfg.Conversation = name
	stdout, _, err := h.runBounded(cfg)
	if !errors.Is(err, ErrUnresolved) || !strings.Contains(stdout, want) {
		t.Fatalf("replay %v\n%s", err, stdout)
	}
	wantErrors(t, h.messages(name), classUnsettled)
}

// bravoFinds scripts bravo's review of the seat alpha's turn to every name as one finding.
func bravoFinds(h *harness, names ...string) {
	h.review("alpha", names[1:]...)
	h.script("bravo", on(promptTurn, says(kindFinding, names...), says(kindSay, "alpha")))
	for _, n := range names[2:] {
		h.script(n, on(promptTurn, says(kindSay, "alpha")))
	}
}

// settles scripts the rest of a discussion: each name agrees, bravo fixes, and alpha ends the run.
func settles(h *harness, names []string, agree ...string) {
	for _, n := range agree {
		h.script(n, on(promptDiscussion, says(kindAgree, names...)))
	}
	h.script("bravo", on(promptFix, says(kindFixed, names...)))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
}

func TestStanceWindowViolationsReportAndLeaveTheProposal(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		// want is the error body, with %d for the offending seq when it has one.
		want  string
		class string
		from  string
		kind  string
	}{
		{
			name: "zero stances",
			setup: func(h *harness) {
				h.script("charlie", on(promptDiscussion))
				settles(h, abcRoster, "alpha", "charlie")
			},
			want: "charlie\nno stance", class: classNoStance, from: "charlie",
		},
		{
			name: "two stances",
			setup: func(h *harness) {
				h.script("charlie", on(promptDiscussion, says(kindObject, abcRoster...), says(kindAgree, abcRoster...)))
				settles(h, abcRoster, "alpha", "charlie")
			},
			want: "charlie\nextra stance", class: classExtraStance, from: "charlie",
		},
		{
			name: "stance from a member who is not the speaker",
			setup: func(h *harness) {
				h.plan.Launch["charlie"] = runFile{Register: true, Steps: []stepFile{
					on(promptTurn, says(kindAgree, abcRoster...), says(kindSay, "alpha")),
				}}
				settles(h, abcRoster, "charlie", "alpha")
			},
			want: "charlie\nbad stance\n%d", class: classBadStance, from: "charlie", kind: kindAgree,
		},
		{
			name: "proposer agree",
			setup: func(h *harness) {
				h.script("charlie", on(promptDiscussion, says(kindObject, abcRoster...)))
				h.script("bravo", on(promptDiscussion, says(kindAgree, abcRoster...)), on(promptDiscussion, says(kindStand, abcRoster...)))
				settles(h, abcRoster, "alpha", "charlie")
			},
			want: "bravo\nbad stance\n%d", class: classBadStance, from: "bravo", kind: kindAgree,
		},
		{
			name: "stand from a member who is not the proposer",
			setup: func(h *harness) {
				h.script("charlie", on(promptDiscussion, says(kindStand, abcRoster...)))
				settles(h, abcRoster, "alpha", "charlie")
			},
			want: "charlie\nbad stance\n%d", class: classBadStance, from: "charlie", kind: kindStand,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			bravoFinds(h, abcRoster...)
			tc.setup(h)
			res, name := h.play(h.team("alpha", abcRoster...))
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			want, group := tc.want, "error: "+tc.class+"\nfrom: "+tc.from+"\n"
			if tc.kind != "" {
				bad := first(msgs, tc.from, tc.kind)
				want = fmt.Sprintf(tc.want, bad.Seq)
				group += "seq: " + seqOf(bad) + "\n"
			}
			wantErrors(t, msgs, want)
			if !strings.Contains(res.stdout, group) {
				t.Fatalf("no group %q\n%s", group, res.stdout)
			}
			f := first(msgs, "bravo", kindFinding)
			if s := ofKind(msgs, kindSettled); len(s) != 1 || s[0].Body != settledBody(f.Seq, f.Seq) {
				t.Fatalf("settled %+v", s)
			}
		})
	}
}

var abcd = []string{"alpha", "bravo", "charlie", "delta"}

func TestPassTakesEffectOnlyFromOneStanceToOneEligibleMember(t *testing.T) {
	pass := func(to ...string) messageFile { return says(kindPass, to...) }
	agree := says(kindAgree, abcd...)
	cases := []struct {
		name string
		// launch is charlie's first discussion launch.
		launch []messageFile
		bad    int
		// next is who speaks after charlie: alpha when the pass took effect, delta by the roster order.
		next string
	}{
		{name: "to an eligible member", launch: []messageFile{agree, pass("alpha")}, next: "alpha"},
		{name: "with no stance", launch: []messageFile{pass("alpha")}, bad: 1, next: "delta"},
		{name: "naming two members", launch: []messageFile{agree, pass("alpha", "delta")}, bad: 1, next: "delta"},
		{name: "to the passer", launch: []messageFile{agree, pass("charlie")}, bad: 1, next: "delta"},
		{name: "outside the roster", launch: []messageFile{agree, pass("zed")}, bad: 1, next: "delta"},
		{name: "to the proposer with no objection", launch: []messageFile{agree, pass("bravo")}, bad: 1, next: "delta"},
		{name: "twice", launch: []messageFile{agree, pass("alpha"), pass("alpha")}, bad: 2, next: "delta"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			bravoFinds(h, abcd...)
			h.script("charlie", stepFile{On: promptDiscussion, Publish: tc.launch})
			settles(h, abcd, "charlie", "alpha", "delta")
			res, name := h.play(h.team("alpha", abcd...))
			if res.err != nil {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			var want []string
			for _, p := range ofKind(msgs, kindPass) {
				if len(want) < tc.bad {
					want = append(want, "charlie\nbad pass\n"+seqOf(p))
				}
			}
			if tc.next == "delta" && tc.launch[0].Kind == kindPass {
				want = append([]string{"charlie\nno stance"}, want...)
			}
			wantErrors(t, msgs, want...)
			a, d := h.callIndex("alpha", promptDiscussion), h.callIndex("delta", promptDiscussion)
			if (tc.next == "alpha") != (a < d) {
				t.Fatalf("alpha at %d delta at %d, want %s next", a, d, tc.next)
			}
			if got := strings.Contains(res.stdout, "pass: alpha\nfrom: charlie\n"); got != (tc.bad == 0) {
				t.Fatalf("pass group %v\n%s", got, res.stdout)
			}
		})
	}
}

func TestPassOutsideADiscussionLaunchIsReported(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	settles(h, abcRoster, "charlie", "alpha")
	h.plan.Launch["bravo"] = runFile{Register: true, Steps: []stepFile{
		on(promptTurn, says(kindFinding, abcRoster...), says(kindSay, "alpha")),
		on(promptFix, says(kindFixed, abcRoster...), says(kindPass, "charlie")),
	}}
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	wantErrors(t, msgs, "bravo\nbad pass\n"+seqOf(first(msgs, "bravo", kindPass)))
}

func TestTimedOutFinderHandsTheFixToTheSeatAndASecondMissStops(t *testing.T) {
	for _, seatFixes := range []bool{true, false} {
		t.Run(fmt.Sprintf("seat fixes %v", seatFixes), func(t *testing.T) {
			h := newHarness(t)
			h.review("alpha", "bravo", "charlie")
			h.script("bravo", stepFile{On: promptTurn, Publish: []messageFile{says(kindFinding, abcRoster...), says(kindSay, "alpha")}, Exit: 1})
			h.script("charlie", on(promptTurn, says(kindSay, "alpha")), on(promptDiscussion, says(kindAgree, abcRoster...)))
			h.script("alpha", on(promptDiscussion, says(kindAgree, abcRoster...)))
			if seatFixes {
				h.script("alpha", on(promptFix, says(kindFixed, abcRoster...)), on(promptFollow, says(kindDone, "owner")))
			} else {
				h.script("alpha", on(promptFix))
			}
			res, name := h.play(h.team("alpha", abcRoster...))
			msgs := h.messages(name)
			f := first(msgs, "bravo", kindFinding)
			if seatFixes {
				if res.err != nil || !strings.Contains(res.stdout, "fixed: "+seqOf(f)+"\nfrom: alpha\n") {
					t.Fatalf("%v\n%s", res.err, res.stdout)
				}
				return
			}
			if res.err == nil || res.err.Error() != classNoFix || !strings.HasSuffix(res.stdout, "outcome: error\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			if !strings.Contains(res.stdout, "error: no fix\nfrom: alpha\n") || countBody(msgs, "alpha\nno fix") != 1 {
				t.Fatalf("no fix not reported\n%s", res.stdout)
			}
			if c, _ := h.conv(name); c.Status != "open" {
				t.Fatalf("status %s", c.Status)
			}
		})
	}
}

// stalled scripts a finding by bravo that stalls with charlie out of budget after one objection, under --max-says 1.
func stalled(h *harness, names ...string) Config {
	bravoFinds(h, names...)
	h.script("charlie", on(promptDiscussion, says(kindObject, names...)))
	h.script("alpha", on(promptDiscussion, says(kindAgree, names...)))
	h.script("bravo", on(promptDiscussion, says(kindStand, names...)))
	cfg := h.team("alpha", names...)
	cfg.MaxSays, cfg.MaxSaysSet = 1, true
	return cfg
}

func TestExtendRefreshesLiveNamesAndReportsTheRest(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcd...)
	h.plan.Launch["delta"] = runFile{Register: true, Steps: []stepFile{{On: promptTurn, Exit: 1}}}
	h.script("alpha",
		on(promptStall, says(kindExtend)),
		on(promptStall, says(kindExtend, "charlie", "delta", "zed")),
	)
	h.script("charlie", on(promptDiscussion, says(kindAgree, abcd...)))
	h.script("bravo", on(promptFix, says(kindFixed, abcd...)))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res, name := h.play(cfg)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	ext := ofKind(msgs, kindExtend)
	f := first(msgs, "bravo", kindFinding)
	if len(ext) != 2 || len(h.launchesWith("alpha", promptStall)) != 2 {
		t.Fatalf("extends %+v", ext)
	}
	wantErrors(t, msgs,
		errBody("delta", classProcessExit, 0),
		errBody("alpha", classBadExtend, ext[0].Seq),
		errBody("delta", classBadExtend, ext[1].Seq),
		errBody("zed", classBadExtend, ext[1].Seq),
	)
	if !strings.Contains(res.stdout, "extend: "+seqOf(f)+"\nto: charlie\n") || strings.Count(res.stdout, "extend: ") != 1 {
		t.Fatalf("stdout\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "error: bad extend\nfrom: zed\nseq: "+seqOf(ext[1])+"\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
}

func TestDeadlockOutsideAStallLaunchIsReportedAndHaltsNothing(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...), says(kindDeadlock, "alpha", "bravo", "charlie", "owner")))
	h.script("alpha", on(promptDiscussion, says(kindAgree, abcRoster...), says(kindDeadlock, "alpha", "bravo", "charlie", "owner")))
	h.script("bravo", on(promptFix, says(kindFixed, abcRoster...)))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	dl := ofKind(msgs, kindDeadlock)
	wantErrors(t, msgs, "charlie\nbad deadlock\n"+seqOf(dl[0]), "alpha\nbad deadlock\n"+seqOf(dl[1]))
	if len(ofKind(msgs, kindHalted)) != 0 || !strings.Contains(res.stdout, "error: bad deadlock\nfrom: charlie\nseq: "+seqOf(dl[0])+"\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
}

func TestSeatThatTimesOutInItsStallLaunchHandsTheStallToTheBackup(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha", stepFile{On: promptStall, Exit: 1})
	h.script("bravo",
		on(promptStall, says(kindExtend, "charlie")),
		on(promptFix, says(kindFixed, abcRoster...)),
		on("alpha is timed-out. You are the acting seat."),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...)))
	res, name := h.play(cfg)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if len(h.launchesWith("alpha", promptStall)) != 1 || len(h.launchesWith("bravo", promptStall)) != 1 {
		t.Fatalf("stall launches %+v", h.calls())
	}
	msgs := h.messages(name)
	wantErrors(t, msgs, errBody("alpha", classProcessExit, 0))
	if len(ofKind(msgs, kindSettled)) != 1 || len(ofKind(msgs, kindHalted)) != 0 {
		t.Fatalf("msgs %+v", msgs)
	}
}

func TestStallLaunchWithNoSingleDecisionStopsWithTheConversationOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []messageFile
	}{
		{"neither", nil},
		{"both", []messageFile{says(kindExtend, "charlie"), says(kindDeadlock, "alpha", "bravo", "charlie", "owner")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cfg := stalled(h, abcRoster...)
			h.script("alpha", stepFile{On: promptStall, Publish: tc.msgs})
			res, name := h.play(cfg)
			if res.err == nil || res.err.Error() != classNoDecision || !strings.HasSuffix(res.stdout, "error: no decision\nfrom: alpha\noutcome: error\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			wantErrors(t, msgs, "alpha\nno decision")
			if c, _ := h.conv(name); c.Status != "open" || len(ofKind(msgs, kindHalted)) != 0 {
				t.Fatalf("status %s", c.Status)
			}
		})
	}
}

func TestStallAndReportPromptsListEveryAskOfTheDiscussion(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha",
		on(promptStall, says(kindAsk, "owner"), says(kindExtend, "zulu")),
		on(promptStall, says(kindDeadlock, "alpha", "bravo", "charlie", "owner")),
		stepFile{On: promptReport, Publish: []messageFile{{Kind: kindDone, To: []string{"owner"}, Body: "outcome: hold\nreport"}}},
	)
	res, name := h.play(cfg)
	if !errors.Is(res.err, ErrUnresolved) {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	ask := "ask: " + seqOf(first(h.messages(name), "alpha", kindAsk)) + "\n"
	stalls := h.launchesWith("alpha", promptStall)
	if len(stalls) != 2 || strings.Contains(stalls[0].Stdin, "ask: ") || !strings.Contains(stalls[1].Stdin, ask) {
		t.Fatalf("stall prompts %+v", stalls)
	}
	if !strings.Contains(h.launchesWith("alpha", promptReport)[0].Stdin, ask) {
		t.Fatal("report prompt has no ask")
	}
}

func TestReportLaunchWithNoDoneStopsWithTheConversationOpen(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha", on(promptStall, says(kindDeadlock, "alpha", "bravo", "charlie", "owner")), on(promptReport))
	res, name := h.play(cfg)
	if res.err == nil || res.err.Error() != classNoReport || !strings.Contains(res.stdout, "error: no report\nfrom: alpha\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if c, _ := h.conv(name); c.Status != "open" {
		t.Fatalf("status %s", c.Status)
	}
	wantErrors(t, h.messages(name), "alpha\nno report")
}

func TestFindingOutsideARunningSetIsReportedAndOpensNothing(t *testing.T) {
	h := newHarness(t)
	h.script("alpha",
		on(promptBoot, says(kindFinding, abcRoster...), messageFile{Kind: kindTurn, To: []string{"bravo"}, Body: "mode: parallel\nreview"}),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("bravo", on(promptTurn, says(kindSay, "alpha")))
	h.script("charlie", on(promptBoot, says(kindFinding, abcRoster...)))
	// charlie publishes after alpha's turn exists and before bravo ends the set, so its finding lies in a set it is not a recipient of.
	h.hold("charlie")
	h.hold("bravo")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.team("alpha", abcRoster...))
	name := h.waitName()
	h.awaitBody(name, "mode: parallel")
	h.release("charlie")
	deadline := time.Now().Add(8 * time.Second)
	for first(h.messages(name), "charlie", kindFinding).Seq == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	h.release("bravo")
	res := h.awaitRun(ch, 20*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	none, outsider := first(msgs, "alpha", kindFinding), first(msgs, "charlie", kindFinding)
	if outsider.Seq < first(msgs, "alpha", kindTurn).Seq {
		t.Fatalf("charlie's finding came before the turn: %+v", msgs)
	}
	wantErrors(t, msgs, errBody("alpha", classBadFinding, none.Seq), errBody("charlie", classBadFinding, outsider.Seq))
	if len(ofKind(msgs, kindSettled)) != 0 || strings.Contains(res.stdout, "\nfinding: ") {
		t.Fatalf("a bad finding opened a discussion\n%s", res.stdout)
	}
	for _, f := range []client.Message{none, outsider} {
		if group := "error: bad finding\nfrom: " + f.From + "\nseq: " + seqOf(f) + "\n"; !strings.Contains(res.stdout, group) {
			t.Fatalf("no %q\n%s", group, res.stdout)
		}
	}
}

// keptMembers creates kept with roster and seat alpha, each member registered and exited with a session.
func (h *harness) keptMembers(roster ...string) {
	h.t.Helper()
	h.keptConversation(roster...)
	launched, exited := statusLaunched, statusExited
	for _, name := range roster {
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &launched})
		h.do("register", client.RegisterRequest{Conversation: "kept", Name: name})
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &exited})
		n, s := name, "sess-"+name
		h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	}
}

func agentsOf(names ...string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=hotseat/grok:m"
	}
	return strings.Join(parts, " ")
}

// reviewed stores alpha's review turn to bravo and charlie, bravo's finding, and both answers.
func (h *harness) reviewed() (turn, finding client.Message) {
	turn = h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
	finding = h.publish("kept", "bravo", kindFinding, "foo", abcRoster)
	h.publish("kept", "bravo", kindSay, "found one", []string{"alpha"})
	h.publish("kept", "charlie", kindSay, "clean", []string{"alpha"})
	return turn, finding
}

func (h *harness) marker(kind, body string) client.Message {
	return h.publish("kept", fromRun, kind, body, []string{})
}

// finishes scripts the discussion of bravo's finding to its fix and alpha's done.
func finishes(h *harness) {
	settles(h, abcRoster, "charlie", "alpha")
}

func (h *harness) playKept() runResult {
	h.t.Helper()
	cctx, cancel := ctx()
	defer cancel()
	return h.awaitRun(h.goRun(cctx, h.continueKept(agentsOf(abcRoster...))), 25*time.Second)
}

func TestContinueBeforeDiscussFinishesTheSetBeforeTheSeatsLaterTurn(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn, _ := h.reviewed()
	h.publish("kept", "alpha", kindTurn, "mode: serial\nlater", []string{"charlie"})
	finishes(h)
	res := h.playKept()
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages("kept")
	if d := ofKind(msgs, kindDiscuss); len(d) != 1 || d[0].Body != seqOf(turn) {
		t.Fatalf("discuss %+v", d)
	}
	if len(ofKind(msgs, kindSettled)) != 1 || len(ofKind(msgs, kindFixed)) != 1 || len(h.launchesWith("alpha", promptFollow)) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	if len(h.launchesWith("charlie", "later")) != 0 || len(h.launchesWith("bravo", promptTurn)) != 0 {
		t.Fatalf("calls %+v", h.calls())
	}
}

func TestContinueWithoutMarkersOrFindingsSkipsAnAnsweredTurn(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
	h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
	next := h.publish("kept", "alpha", kindTurn, "mode: serial\nnext", []string{"bravo"})
	h.script("bravo", on("next", says(kindSay, "alpha")))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res := h.playKept()
	if res.err != nil {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if d := ofKind(h.messages("kept"), kindDiscuss); len(d) != 1 || d[0].Body != seqOf(next) {
		t.Fatalf("discuss %+v", d)
	}
	if n := len(h.launchesWith("alpha", promptFollow)); n != 1 {
		t.Fatalf("follow-ups %d", n)
	}
}

func TestContinueMidDiscussionRestartsTheOpenFindingAndItsFollowUp(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn, _ := h.reviewed()
	h.marker(kindDiscuss, seqOf(turn))
	h.publish("kept", "charlie", kindAgree, "yes", abcRoster)
	// A turn posted in a stall launch is stored before the discussions end, so it is not the follow-up.
	h.publish("kept", "alpha", kindTurn, "mode: serial\nlater", []string{"charlie"})
	finishes(h)
	res := h.playKept()
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	disc := h.launchesWith("charlie", promptDiscussion)
	if len(disc) != 1 || !strings.Contains(disc[0].Stdin, "This discussion restarted.") {
		t.Fatalf("charlie discussion %+v", disc)
	}
	// The agree stored before the restart lies at or before start, so the prompt marks it as history.
	if old := first(h.messages("kept"), "charlie", kindAgree); promptStart(t, disc[0].Stdin) < old.Seq {
		t.Fatalf("start %d is before the old agree %d", promptStart(t, disc[0].Stdin), old.Seq)
	}
	if !strings.Contains(h.launchesWith("alpha", promptDiscussion)[0].Stdin, "This discussion restarted.") {
		t.Fatal("a later launch of the restarted discussion does not say so")
	}
	if len(ofKind(h.messages("kept"), kindDiscuss)) != 1 || len(h.launchesWith("alpha", promptFollow)) != 1 {
		t.Fatalf("calls %+v", h.calls())
	}
	wantErrors(t, h.messages("kept"))
}

func TestContinueAfterSettledBeforeFixedRunsTheFixAgain(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn, f := h.reviewed()
	h.marker(kindDiscuss, seqOf(turn))
	h.marker(kindSettled, settledBody(f.Seq, f.Seq))
	finishes(h)
	res := h.playKept()
	if res.err != nil || !strings.Contains(res.stdout, "fixed: "+seqOf(f)+"\nfrom: bravo\n") || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if n := len(h.launchesWith("charlie", promptDiscussion)) + len(h.launchesWith("alpha", promptDiscussion)); n != 0 {
		t.Fatalf("discussion launches %d", n)
	}
}

// leftover records name as launched with a live process that the test ends.
func (h *harness) leftover(name string) func() {
	h.t.Helper()
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		h.t.Fatal(err)
	}
	pid := child.Process.Pid
	stop := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	}
	h.t.Cleanup(stop)
	running, p := statusRunning, int64(pid)
	h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &running, PID: &p})
	return stop
}

func TestContinueWaitsForALeftoverFixAndLaunchesNoneWhenItFixed(t *testing.T) {
	for _, name := range []string{"bravo", "alpha"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.keptMembers(abcRoster...)
			turn, f := h.reviewed()
			h.marker(kindDiscuss, seqOf(turn))
			h.marker(kindSettled, settledBody(f.Seq, f.Seq))
			stop := h.leftover(name)
			finishes(h)
			cctx, cancel := ctx()
			defer cancel()
			ch := h.goRun(cctx, h.continueKept(agentsOf(abcRoster...)))
			time.Sleep(300 * time.Millisecond)
			h.stillRunning(ch)
			if n := h.launchCount(); n != 0 {
				t.Fatalf("launched %d while the leftover ran", n)
			}
			h.publish("kept", name, kindAgree, "late", abcRoster)
			h.publish("kept", name, kindFixed, "applied", abcRoster)
			stop()
			res := h.awaitRun(ch, 20*time.Second)
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			if n := len(h.launchesWith("bravo", promptFix)) + len(h.launchesWith("alpha", promptFix)); n != 0 {
				t.Fatalf("fix launches %d", n)
			}
			wantErrors(t, h.messages("kept"))
		})
	}
}

func TestContinueJudgesAStrayFromATurnLaunchedWhileALeftoverRuns(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	h.publish("kept", "alpha", kindTurn, "mode: serial\ncheck", []string{"charlie"})
	stop := h.leftover("alpha")
	h.script("charlie", on(promptTurn, says(kindAgree, abcRoster...), says(kindSay, "alpha")))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.continueKept(agentsOf(abcRoster...)))
	deadline := time.Now().Add(8 * time.Second)
	for first(h.messages("kept"), "charlie", kindSay).Seq == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	h.stillRunning(ch)
	stop()
	res := h.awaitRun(ch, 20*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages("kept")
	wantErrors(t, msgs, errBody("charlie", classBadStance, first(msgs, "charlie", kindAgree).Seq))
}

func TestContinueAfterHaltedReportsAgainAndNothingElse(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn, f := h.reviewed()
	h.marker(kindDiscuss, seqOf(turn))
	ask := h.publish("kept", "charlie", kindAsk, "which one?", []string{"owner"})
	h.marker(kindHalted, seqOf(f))
	h.script("alpha", stepFile{On: promptReport, Publish: []messageFile{{Kind: kindDone, To: []string{"owner"}, Body: "outcome: hold\nreport"}}})
	res := h.playKept()
	if !errors.Is(res.err, ErrUnresolved) || !strings.HasSuffix(res.stdout, "unsettled: "+seqOf(f)+"\noutcome: deadlock\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if h.launchCount() != 1 || len(h.launchesWith("alpha", promptReport)) != 1 {
		t.Fatalf("calls %+v", h.calls())
	}
	report := h.launchesWith("alpha", promptReport)[0].Stdin
	if !strings.Contains(report, "The discussion state before the restart is in the transcript.") || !strings.Contains(report, "ask: "+seqOf(ask)+"\n") {
		t.Fatalf("report prompt\n%s", report)
	}
}

func TestRejectedMemberDeadlockBeforeARestartDoesNotEndTheRun(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn, _ := h.reviewed()
	h.marker(kindDiscuss, seqOf(turn))
	dl := h.publish("kept", "charlie", kindDeadlock, "stop", []string{"alpha", "bravo", "charlie", "owner"})
	h.publish("kept", fromRun, kindError, errBody("charlie", classBadDeadlock, dl.Seq), []string{"owner"})
	finishes(h)
	res := h.playKept()
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages("kept")
	if len(ofKind(msgs, kindHalted)) != 0 || len(ofKind(msgs, kindSettled)) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	wantErrors(t, msgs, errBody("charlie", classBadDeadlock, dl.Seq))
}

func TestContinueDoesNotReportAStoredBadFindingAgain(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprintf("stored %v", stored), func(t *testing.T) {
			h := newHarness(t)
			h.keptMembers(abcRoster...)
			h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
			bad := h.publish("kept", "charlie", kindFinding, "not mine", abcRoster)
			if stored {
				h.publish("kept", fromRun, kindError, errBody("charlie", classBadFinding, bad.Seq), []string{"owner"})
			}
			h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
			h.script("alpha", on(promptFollow, says(kindDone, "owner")))
			res := h.playKept()
			if res.err != nil {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			wantErrors(t, h.messages("kept"), errBody("charlie", classBadFinding, bad.Seq))
			if got := strings.Contains(res.stdout, "error: bad finding\n"); got == stored {
				t.Fatalf("error group %v\n%s", got, res.stdout)
			}
		})
	}
}

func TestResumeWithoutRosterOpensNothingAndReportsTheStoredDone(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	_, f := h.reviewed()
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.continueKept(""))
	time.Sleep(300 * time.Millisecond)
	h.stillRunning(ch)
	if n := len(ofKind(h.messages("kept"), kindDiscuss)) + h.launchCount(); n != 0 {
		t.Fatalf("a resume with no roster opened a discussion or launched: %+v", h.calls())
	}
	h.publish("kept", "alpha", kindDone, "early", []string{"owner"})
	res := h.awaitRun(ch, 10*time.Second)
	tail := "unsettled: " + seqOf(f) + "\nerror: unsettled\noutcome: unsettled\n"
	if !errors.Is(res.err, ErrUnresolved) || !strings.HasSuffix(res.stdout, tail) {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	stdout, _, err := h.runBounded(h.continueKept(""))
	if !errors.Is(err, ErrUnresolved) || !strings.HasSuffix(stdout, "unsettled: "+seqOf(f)+"\noutcome: unsettled\n") {
		t.Fatalf("replay %v\n%s", err, stdout)
	}
	wantErrors(t, h.messages("kept"), classUnsettled)
	if h.launchCount() != 0 {
		t.Fatalf("calls %+v", h.calls())
	}
}

func TestSeatTimeoutDuringDiscussionsHoldsTheHandoffAndTheBackupDecidesTheStall(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	h.script("charlie", on(promptDiscussion, says(kindObject, abcRoster...)), on(promptDiscussion, says(kindAgree, abcRoster...)))
	h.script("alpha", stepFile{On: promptDiscussion, Exit: 1})
	h.script("bravo",
		on(promptDiscussion, says(kindStand, abcRoster...)),
		on(promptStall, says(kindExtend, "charlie")),
		on(promptFix, says(kindFixed, abcRoster...)),
		on("alpha is timed-out. You are the acting seat."),
		on(promptFollow, says(kindDone, "owner")),
	)
	cfg := h.team("alpha", abcRoster...)
	cfg.MaxSays, cfg.MaxSaysSet = 1, true
	res, name := h.play(cfg)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if len(h.launchesWith("bravo", promptStall)) != 1 || len(h.launchesWith("alpha", promptStall)) != 0 {
		t.Fatalf("stall launches %+v", h.calls())
	}
	handoff := h.callIndex("bravo", "alpha is timed-out. You are the acting seat.")
	if handoff < 0 || handoff < h.callIndex("bravo", promptFix) {
		t.Fatalf("handoff at %d fix at %d", handoff, h.callIndex("bravo", promptFix))
	}
	if len(ofKind(h.messages(name), kindSettled)) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestLaunchCapStopsADiscussionWithTheConversationOpen(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	cfg := h.team("alpha", abcRoster...)
	// Three boots and two review launches use the cap, so the first discussion launch would pass it.
	cfg.MaxLaunches, cfg.MaxLaunchesSet = 5, true
	res, name := h.play(cfg)
	if res.err == nil || res.err.Error() != classLaunchCap || !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	if len(ofKind(msgs, kindDiscuss)) != 1 || len(ofKind(msgs, kindSettled)) != 0 || h.launchCount() != 5 {
		t.Fatalf("launches %d msgs %+v", h.launchCount(), msgs)
	}
	wantErrors(t, msgs, classLaunchCap)
	if c, _ := h.conv(name); c.Status != "open" {
		t.Fatalf("status %s", c.Status)
	}
}

func TestMemberLostBelowTheMinimumStopsADiscussion(t *testing.T) {
	h := newHarness(t)
	h.plan.MinMembers = "2\n"
	bravoFinds(h, "alpha", "bravo")
	h.script("alpha", stepFile{On: promptDiscussion, Exit: 1})
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if res.err == nil || res.err.Error() != classBelowMinimum || !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	if len(ofKind(msgs, kindSettled)) != 0 || len(h.launchesWith("bravo", promptFix))+len(h.launchesWith("bravo", promptStall)) != 0 {
		t.Fatalf("calls %+v", h.calls())
	}
	wantErrors(t, msgs, errBody("alpha", classProcessExit, 0), classBelowMinimum)
	if c, _ := h.conv(name); c.Status != "open" {
		t.Fatalf("status %s", c.Status)
	}
}

func TestFailedDiscussionLaunchKeepsItsStanceAndReportsOnlyTheExit(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	h.script("charlie", stepFile{On: promptDiscussion, Publish: []messageFile{says(kindAgree, abcRoster...), says(kindPass, "alpha")}, Exit: 1})
	settles(h, abcRoster, "alpha")
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if !strings.Contains(res.stdout, "stance: agree\nfrom: charlie\nseq: ") || strings.Contains(res.stdout, "pass: alpha") {
		t.Fatalf("stdout %s", res.stdout)
	}
	msgs := h.messages(name)
	if len(ofKind(msgs, kindSettled)) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	wantErrors(t, msgs, errBody("charlie", classProcessExit, 0))
}

func TestFailedDiscussionLaunchReportsOnlyAnInvalidStance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		msgs   []messageFile
		stance string
	}{
		{"extra stances", []messageFile{says(kindAgree, abcRoster...), says(kindObject, abcRoster...)}, ""},
		{"invalid stance", []messageFile{says(kindStand, abcRoster...)}, kindStand},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			bravoFinds(h, abcRoster...)
			h.script("charlie", stepFile{On: promptDiscussion, Publish: tc.msgs, Exit: 1})
			settles(h, abcRoster, "alpha")
			res, name := h.play(h.team("alpha", abcRoster...))
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			want := []string{errBody("charlie", classProcessExit, 0)}
			if tc.stance != "" {
				want = append(want, errBody("charlie", classBadStance, first(msgs, "charlie", tc.stance).Seq))
			}
			wantErrors(t, msgs, want...)
		})
	}
}

func TestHaltWhileAFindingIsJudgedReturnsTheHaltError(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{Name: "kept", Seat: "alpha", Roster: []string{"alpha", "bravo"}})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	child, cancel := context.WithCancel(context.Background())
	e := &engine{
		root:   context.Background(),
		child:  child,
		cancel: cancel,
		addr:   h.addr,
		name:   "kept",
		seat:   "alpha",
		roster: []agent{{name: "alpha", module: "hotseat/grok", model: "g1"}, {name: "bravo", module: "hotseat/grok", model: "g1"}},
		status: map[string]string{},
		out:    &statusWriter{w: io.Discard},
	}
	halt := errors.New(classLaunchCap)
	_ = e.noteHalt(halt)
	err := e.judgeFinding(client.Message{Seq: 1, From: "bravo", Kind: kindFinding})
	if !errors.Is(err, halt) {
		t.Fatalf("err %v, want %v", err, halt)
	}
}

func TestLedgerBuildReadsOnlyNewMessagesAndTheBackupOnce(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"})
	h.publish("kept", "alpha", kindTurn, "mode: serial\nfirst", []string{"bravo"})
	h.publish("kept", "owner", kindTurn, "mode: serial\nsecond", []string{"bravo"})
	h.publish("kept", "charlie", kindTurn, "mode: serial\nthird", []string{"bravo"})
	var mu sync.Mutex
	var cursors []int64
	members := 0
	addr := h.proxy(func(_ http.ResponseWriter, path string, body []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(path, "/member"):
			members++
		case strings.HasSuffix(path, "/read"):
			var req client.ReadRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
			}
			cursors = append(cursors, req.Cursor)
		}
		return false
	})
	e := &engine{
		root:   context.Background(),
		addr:   addr,
		name:   "kept",
		seat:   "alpha",
		roster: []agent{{name: "alpha"}, {name: "bravo"}, {name: "charlie"}},
		status: map[string]string{"alpha": statusTimedOut},
	}
	build := func() (ledger, []int64, int) {
		t.Helper()
		mu.Lock()
		cursors, members = nil, 0
		mu.Unlock()
		l, err := e.ledgerOn(e.root)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return l, cursors, members
	}
	l, reads, calls := build()
	if slices.Compare(reads, []int64{0}) != 0 || calls != 1 || l.targets["alpha"] != "bravo" || l.targets["owner"] != "bravo" || l.targets["charlie"] != "charlie" {
		t.Fatalf("reads %v member calls %d targets %v", reads, calls, l.targets)
	}
	last := l.msgs[len(l.msgs)-1].Seq
	later := h.publish("kept", "charlie", kindTurn, "mode: serial\nfourth", []string{"bravo"})
	l, reads, calls = build()
	if slices.Compare(reads, []int64{last}) != 0 || calls != 1 || len(l.msgs) != 4 || l.msgs[3].Seq != later.Seq {
		t.Fatalf("reads %v member calls %d msgs %+v", reads, calls, l.msgs)
	}
}

func TestAcceptedPassAndExtendCountTowardTheTurnCap(t *testing.T) {
	t.Run("pass", func(t *testing.T) {
		h := newHarness(t)
		bravoFinds(h, abcRoster...)
		h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...), says(kindPass, "alpha")))
		cfg := h.team("alpha", abcRoster...)
		cfg.MaxTurns, cfg.MaxTurnsSet = 1, true
		res, name := h.play(cfg)
		if res.err == nil || res.err.Error() != classTurnCap || strings.Contains(res.stdout, "pass: ") {
			t.Fatalf("%v\n%s", res.err, res.stdout)
		}
		wantErrors(t, h.messages(name), classTurnCap)
	})
	t.Run("extend", func(t *testing.T) {
		h := newHarness(t)
		cfg := stalled(h, abcRoster...)
		h.script("alpha", on(promptStall, says(kindExtend, "charlie")))
		cfg.MaxTurns, cfg.MaxTurnsSet = 1, true
		res, name := h.play(cfg)
		if res.err == nil || res.err.Error() != classTurnCap || strings.Contains(res.stdout, "extend: ") {
			t.Fatalf("%v\n%s", res.err, res.stdout)
		}
		wantErrors(t, h.messages(name), classTurnCap)
	})
	t.Run("extend that refreshes nobody", func(t *testing.T) {
		h := newHarness(t)
		cfg := stalled(h, abcRoster...)
		h.script("alpha", on(promptStall, says(kindExtend, "zulu")), on(promptStall, says(kindExtend, "charlie")))
		cfg.MaxTurns, cfg.MaxTurnsSet = 1, true
		res, name := h.play(cfg)
		if res.err == nil || res.err.Error() != classTurnCap || len(h.launchesWith("alpha", promptStall)) != 1 {
			t.Fatalf("%v\n%s", res.err, res.stdout)
		}
		msgs := h.messages(name)
		wantErrors(t, msgs, errBody("zulu", classBadExtend, first(msgs, "alpha", kindExtend).Seq), classTurnCap)
	})
}

func TestSeatAggregateDropsADuplicateFindingBeforeDiscussion(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "alpha", "bravo", "charlie")
	h.script("alpha",
		on(promptTurn, says(kindSay, "alpha")),
		on(promptAggregate, messageFile{Kind: kindAggregate, To: abcRoster, Body: "$line:findings"}),
		on(promptDiscussion, says(kindAgree, abcRoster...)),
		on(promptFollow, says(kindDone, "owner")),
	)
	// The parallel launches race, so either finding can be first and either finder can own the kept issue.
	for _, name := range []string{"bravo", "charlie"} {
		h.script(name,
			on(promptTurn, says(kindFinding, abcRoster...), says(kindSay, "alpha")),
			on(promptDiscussion, says(kindAgree, abcRoster...)),
			on(promptFix, says(kindFixed, abcRoster...)),
		)
	}
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	turn := first(msgs, "alpha", kindTurn)
	found := ofKind(msgs, kindFinding)
	if len(found) != 2 {
		t.Fatalf("findings %+v", found)
	}
	kept, dup := found[0], found[1]
	discuss := first(msgs, fromRun, kindDiscuss)
	issues := first(msgs, fromRun, kindIssues)
	if issues.Body != seqOf(turn)+"\n"+seqOf(kept)+" "+seqOf(dup) || len(issues.To) != 0 || issues.Seq < discuss.Seq {
		t.Fatalf("issues %+v discuss %d", issues, discuss.Seq)
	}
	settled := ofKind(msgs, kindSettled)
	if len(settled) != 1 || settled[0].Body != settledBody(kept.Seq, kept.Seq) || len(ofKind(msgs, kindFixed)) != 1 {
		t.Fatalf("settled %+v", settled)
	}
	if a := h.launchesWith("alpha", promptAggregate); len(a) != 1 || !strings.Contains(a[0].Stdin, "findings: "+seqOf(kept)+" "+seqOf(dup)+"\n") {
		t.Fatalf("aggregate launches %+v", a)
	}
	for _, c := range h.launchesWith("alpha", promptDiscussion) {
		if !strings.Contains(c.Stdin, "duplicates: "+seqOf(dup)+"\n") || promptStart(t, c.Stdin) != issues.Seq {
			t.Fatalf("discussion prompt %q", c.Stdin)
		}
	}
	if f := h.launchesWith("alpha", promptFollow); len(f) != 1 || !strings.Contains(f[0].Stdin, "no reply: none\ndropped: "+seqOf(dup)+"\n") {
		t.Fatalf("follow-up %+v", f)
	}
	if !strings.Contains(res.stdout, "issue: "+seqOf(kept)+"\ndropped: "+seqOf(dup)+"\n") {
		t.Fatalf("stdout has no issue group\n%s", res.stdout)
	}
	wantErrors(t, msgs)
}

func TestFailedAggregateDiscussesEveryFinding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		step  stepFile
		class string
		atSeq bool
	}{
		{"no aggregate", on(promptAggregate), classNoAggregate, false},
		{"bad aggregate", on(promptAggregate, messageFile{Kind: kindAggregate, To: abcRoster, Body: "999"}), classBadAggregate, true},
		{"two aggregates", on(promptAggregate,
			messageFile{Kind: kindAggregate, To: abcRoster, Body: "$line:findings"},
			messageFile{Kind: kindAggregate, To: abcRoster, Body: "$line:findings"},
		), classBadAggregate, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.review("alpha", "alpha", "bravo", "charlie")
			h.script("alpha",
				on(promptTurn, says(kindSay, "alpha")),
				tc.step,
				on(promptDiscussion, says(kindAgree, abcRoster...)),
				on(promptDiscussion, says(kindAgree, abcRoster...)),
				on(promptFollow, says(kindDone, "owner")),
			)
			h.script("bravo",
				on(promptTurn, says(kindFinding, abcRoster...), says(kindFinding, abcRoster...), says(kindSay, "alpha")),
				on(promptFix, says(kindFixed, abcRoster...)),
				on(promptFix, says(kindFixed, abcRoster...)),
			)
			h.script("charlie",
				on(promptTurn, says(kindSay, "alpha")),
				on(promptDiscussion, says(kindAgree, abcRoster...)),
				on(promptDiscussion, says(kindAgree, abcRoster...)),
			)
			res, name := h.play(h.team("alpha", abcRoster...))
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			turn := first(msgs, "alpha", kindTurn)
			found := ofKind(msgs, kindFinding)
			if len(found) != 2 {
				t.Fatalf("findings %+v", found)
			}
			if issues := first(msgs, fromRun, kindIssues); issues.Body != seqOf(turn)+"\n"+seqOf(found[0])+"\n"+seqOf(found[1]) {
				t.Fatalf("issues %q", issues.Body)
			}
			if settled := ofKind(msgs, kindSettled); len(settled) != 2 {
				t.Fatalf("settled %+v", settled)
			}
			// Each aggregate the seat stored is reported on its own seq. A missing aggregate is reported once with no seq.
			want := []string{errBody("alpha", tc.class, 0)}
			if tc.atSeq {
				want = nil
				for _, a := range ofKind(msgs, kindAggregate) {
					want = append(want, errBody("alpha", tc.class, a.Seq))
				}
			}
			wantErrors(t, msgs, want...)
		})
	}
}

func TestContinueAfterDiscussWithoutIssuesAggregatesAgain(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
	kept := h.publish("kept", "bravo", kindFinding, "foo", abcRoster)
	dup := h.publish("kept", "charlie", kindFinding, "foo again", abcRoster)
	h.publish("kept", "bravo", kindSay, "found one", []string{"alpha"})
	h.publish("kept", "charlie", kindSay, "found one", []string{"alpha"})
	h.marker(kindDiscuss, seqOf(turn))
	h.script("alpha", on(promptAggregate, messageFile{Kind: kindAggregate, To: abcRoster, Body: "$line:findings"}))
	finishes(h)
	res := h.playKept()
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages("kept")
	if a := h.launchesWith("alpha", promptAggregate); len(a) != 1 || !strings.Contains(a[0].Stdin, "findings: "+seqOf(kept)+" "+seqOf(dup)+"\n") {
		t.Fatalf("aggregate launches %+v", a)
	}
	issues := ofKind(msgs, kindIssues)
	if len(issues) != 1 || issues[0].Body != seqOf(turn)+"\n"+seqOf(kept)+" "+seqOf(dup) {
		t.Fatalf("issues %+v", issues)
	}
	if d := h.callIndex("charlie", promptDiscussion); d < 0 || h.callIndex("alpha", promptAggregate) > d {
		t.Fatalf("aggregate after the first discussion launch %+v", h.calls())
	}
	if settled := ofKind(msgs, kindSettled); len(settled) != 1 || settled[0].Body != settledBody(kept.Seq, kept.Seq) {
		t.Fatalf("settled %+v", settled)
	}
	wantErrors(t, msgs)
}

func TestRecipientThatPublishesNothingIsReportedAndNamedInTheFollowUp(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "alpha", "bravo", "charlie")
	h.script("alpha",
		on(promptTurn, says(kindSay, "alpha")),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("bravo", on(promptTurn, says(kindSay, "alpha")))
	h.script("charlie", on(promptTurn))
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	turn := first(msgs, "alpha", kindTurn)
	wantErrors(t, msgs, errBody("charlie", classNoReply, turn.Seq))
	if e := first(msgs, fromRun, kindError); e.Seq > first(msgs, fromRun, kindDiscuss).Seq {
		t.Fatalf("no reply %d after discuss", e.Seq)
	}
	if f := h.launchesWith("alpha", promptFollow); len(f) != 1 || !strings.Contains(f[0].Stdin, "no reply: charlie\n") {
		t.Fatalf("follow-up %+v", f)
	}
	if !strings.Contains(res.stdout, "error: no reply\nfrom: charlie\nseq: "+seqOf(turn)+"\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
}
