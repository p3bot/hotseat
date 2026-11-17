// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func reOf(m client.Message) int64 {
	if m.Re == nil {
		return 0
	}
	return *m.Re
}

func withSeqRe(m client.Message, re int64) client.Message {
	m.Re = &re
	return m
}

// publishRe stores a message in conv with re, as a member would.
func (h *harness) publishRe(conv, from, kind, body string, to []string, re int64) client.Message {
	h.t.Helper()
	res := h.do("publish", client.PublishRequest{
		Conversation: conv, From: from, To: to, Body: body, TxID: "re-" + from + "-" + seqText(txSeq.Add(1)), Kind: kind, Re: &re,
	})
	if res.Outcome != bus.OutcomeOK || res.Message == nil {
		h.t.Fatalf("publish %s: %s", kind, res.Reason)
	}
	return *res.Message
}

// reCommand is the publish command text a prompt gives for kind with re.
func reCommand(kind string, re int64) string {
	return "--kind " + kind + " --re " + seqText(re) + ","
}

func TestFindingWithReBelongsOnlyToTheOpenSetItNames(t *testing.T) {
	l := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nfirst", "b"),               // 1
		withSeqRe(msg("b", kindFinding, "names its turn", abc...), 1),  // 2
		withSeqRe(msg("b", kindFinding, "names a finding", abc...), 2), // 3
		msg("b", kindFinding, "no re in the window", abc...),           // 4
		withSeqRe(msg("c", kindFinding, "not a recipient", abc...), 1), // 5
	), records{})
	if got := seqs(l.findingsOf(1)); slices.Compare(got, []int64{2, 4}) != 0 {
		t.Fatalf("set findings %v", got)
	}
	// A set has closed once it has a set record.
	closed := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nfirst", "b"),                    // 1
		msg("b", kindSay, "clean", "a"),                                     // 2
		msg("a", kindTurn, "mode: parallel\nsecond", "b"),                   // 3
		withSeqRe(msg("b", kindFinding, "names the closed set", abc...), 1), // 4
		withSeqRe(msg("b", kindFinding, "names the open set", abc...), 3),   // 5
		msg("b", kindFinding, "no re", abc...),                              // 6
	), records{sets: []client.Set{setRec(1, 2)}})
	if closed.valid(4) || slices.Compare(seqs(closed.findingsOf(3)), []int64{5, 6}) != 0 {
		t.Fatalf("closed set took a finding: %v %v", closed.valid(4), seqs(closed.findingsOf(3)))
	}
}

func TestFixedNamingAnotherMessageIsNotCreditedToTheSettle(t *testing.T) {
	transcript := []client.Message{
		msg("a", kindTurn, "mode: parallel\nreview", "b"),  // 1
		msg("b", kindFinding, "foo", abc...),               // 2
		withSeqRe(msg("b", kindFixed, "wrong", abc...), 1), // 3
	}
	recs := records{
		sets:      []client.Set{setRec(1, 2, 2)},
		groupings: []client.Grouping{groupingRec(1, 2)},
		settles:   []client.Settle{settleRec(2, 2, kindFinding, 2)},
	}
	if l := ledgerOf(tx(slices.Clone(transcript)...), recs); !l.needsFix(2) {
		t.Fatal("a fixed naming another message was credited")
	}
	right := append(slices.Clone(transcript), withSeqRe(msg("b", kindFixed, "right", abc...), 2))
	if l := ledgerOf(tx(right...), recs); l.needsFix(2) {
		t.Fatal("a fixed naming the finding was not credited")
	}
}

func TestEveryPromptGivesTheExactReAndACorrectReChangesNothing(t *testing.T) {
	h := newHarness(t)
	r := func(kind string, to ...string) messageFile { return withRe(says(kind, to...), "$re") }
	h.review("alpha", "alpha", "bravo", "charlie")
	h.script("alpha",
		on(promptTurn, r(kindSay, "alpha")),
		on(promptAggregate, messageFile{Kind: kindAggregate, To: abcRoster, Body: "$lines:findings", Re: "$re"}),
		on(promptDiscussion, r(kindAgree, abcRoster...)),
		on(promptDiscussion, r(kindAgree, abcRoster...)),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("bravo",
		on(promptTurn, r(kindFinding, abcRoster...), r(kindFinding, abcRoster...), r(kindSay, "charlie")),
		on(promptFix, r(kindFixed, abcRoster...)),
		on(promptFix, r(kindFixed, abcRoster...)),
	)
	h.script("charlie",
		on(promptTurn, r(kindSay, "alpha")),
		on(promptDiscussion, r(kindAgree, abcRoster...)),
		on(promptDiscussion, r(kindAgree, abcRoster...)),
	)
	res, name := h.play(h.team("alpha", abcRoster...))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	wantErrors(t, msgs)
	turn := first(msgs, "alpha", kindTurn)
	found, settled, fixed := ofKind(msgs, kindFinding), ofKind(msgs, kindSettled), ofKind(msgs, kindFixed)
	if len(found) != 2 || len(settled) != 2 || len(fixed) != 2 {
		t.Fatalf("findings %d settled %d fixed %d", len(found), len(settled), len(fixed))
	}
	f1, f2 := found[0], found[1]
	for _, c := range append(h.launchesWith("bravo", promptTurn), h.launchesWith("charlie", promptTurn)...) {
		if !strings.Contains(c.Stdin, reCommand(kindSay, turn.Seq)) || !strings.Contains(c.Stdin, reCommand(kindFinding, turn.Seq)) {
			t.Fatalf("turn prompt %q", c.Stdin)
		}
	}
	if a := h.launchesWith("alpha", promptAggregate); len(a) != 1 || !strings.Contains(a[0].Stdin, reCommand(kindAggregate, turn.Seq)) {
		t.Fatalf("aggregate prompts %+v", a)
	}
	if c := h.launchesWith("charlie", promptDiscussion); len(c) != 2 ||
		!strings.Contains(c[0].Stdin, reCommand("<stance>", f1.Seq)) || !strings.Contains(c[0].Stdin, reCommand(kindPass, f1.Seq)) ||
		!strings.Contains(c[1].Stdin, reCommand("<stance>", f2.Seq)) {
		t.Fatalf("discussion prompts %+v", c)
	}
	if c := h.launchesWith("bravo", promptFix); len(c) != 2 ||
		!strings.Contains(c[0].Stdin, reCommand(kindFixed, f1.Seq)) || !strings.Contains(c[1].Stdin, reCommand(kindFixed, f2.Seq)) {
		t.Fatalf("fix prompts %+v", c)
	}
	// Every member message carries the re its prompt gave, and bravo's say answers the turn although it names only charlie.
	for _, m := range msgs {
		var want int64
		switch m.Kind {
		case kindSay, kindFinding, kindAggregate:
			want = turn.Seq
		case kindAgree:
			want = f2.Seq
			if m.Seq < settled[0].Seq {
				want = f1.Seq
			}
		case kindFixed:
			want = f2.Seq
			if m.Seq < settled[1].Seq {
				want = f1.Seq
			}
		default:
			continue
		}
		if reOf(m) != want {
			t.Fatalf("%s %d has re %d, want %d", m.Kind, m.Seq, reOf(m), want)
		}
	}
	if f := h.launchesWith("alpha", promptFollow); len(f) != 1 || !strings.Contains(f[0].Stdin, "no reply: none\n") {
		t.Fatalf("follow-up %+v", f)
	}
}

func TestFindingWithAReOtherThanItsTurnIsReportedOnceAndOpensNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		re    string
		opens bool
	}{
		{"re names the turn", "$re", true},
		{"no re inside the window", "", true},
		{"re names another message", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.review("alpha", "bravo")
			h.script("bravo",
				on(promptTurn, withRe(says(kindFinding, "alpha", "bravo"), tc.re), says(kindSay, "alpha")),
				on(promptFix, says(kindFixed, "alpha", "bravo")),
			)
			h.script("alpha", on(promptDiscussion, says(kindAgree, "alpha", "bravo")), on(promptFollow, says(kindDone, "owner")))
			res, name := h.play(h.team("alpha", "alpha", "bravo"))
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			f := first(msgs, "bravo", kindFinding)
			if opened := len(ofKind(msgs, kindSettled)) == 1; opened != tc.opens {
				t.Fatalf("opened %v\n%s", opened, res.stdout)
			}
			if tc.opens {
				wantErrors(t, msgs)
				return
			}
			wantErrors(t, msgs, errLine("bravo", classBadFinding, f.Seq))
			if group := "error: bad finding\nfrom: bravo\nseq: " + seqOf(f) + "\n"; strings.Count(res.stdout, group) != 1 {
				t.Fatalf("no single %q\n%s", group, res.stdout)
			}
		})
	}
}

func TestFindingWhoseReNamesAClosedSetsTurnIsBad(t *testing.T) {
	h := newHarness(t)
	h.review("alpha", "bravo")
	h.script("bravo",
		on(promptTurn, says(kindSay, "alpha")),
		// The first turn is seq 2, after the instructions.
		on("second", withRe(says(kindFinding, "alpha", "bravo"), "2"), withRe(says(kindSay, "alpha"), "$re")),
	)
	h.script("alpha",
		stepFile{On: promptFollow, Publish: []messageFile{{Kind: kindTurn, To: []string{"bravo"}, Body: "mode: serial\nsecond"}}},
		on(promptFollow, says(kindDone, "owner")),
	)
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	turns, f := ofKind(msgs, kindTurn), first(msgs, "bravo", kindFinding)
	if len(turns) != 2 || turns[0].Seq != 2 || reOf(f) != 2 || f.Seq < turns[1].Seq {
		t.Fatalf("transcript %+v", msgs)
	}
	wantErrors(t, msgs, errLine("bravo", classBadFinding, f.Seq))
	if len(ofKind(msgs, kindSettled)) != 0 || len(h.launchesWith("alpha", promptDiscussion)) != 0 {
		t.Fatalf("a closed set's finding opened a discussion\n%s", res.stdout)
	}
}

func TestSayAnswersTheTurnItsReNamesWhateverItsTo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		say      messageFile
		answered bool
	}{
		{"re names the turn and to omits the sender", withRe(says(kindSay, "charlie"), "$re"), true},
		{"re names another message", withRe(says(kindSay, "alpha"), "1"), false},
		{"no re and to names the sender", says(kindSay, "alpha"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.review("alpha", "bravo", "charlie")
			h.script("bravo", on(promptTurn, tc.say))
			h.script("charlie", on(promptTurn, says(kindSay, "alpha")))
			h.script("alpha", on(promptFollow, says(kindDone, "owner")))
			res, name := h.play(h.team("alpha", abcRoster...))
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			msgs := h.messages(name)
			turn := first(msgs, "alpha", kindTurn)
			noReply := "no reply: none\n"
			if tc.answered {
				wantErrors(t, msgs)
			} else {
				wantErrors(t, msgs, errLine("bravo", classNoReply, turn.Seq))
				noReply = "no reply: bravo\n"
			}
			if f := h.launchesWith("alpha", promptFollow); len(f) != 1 || !strings.Contains(f[0].Stdin, noReply) {
				t.Fatalf("follow-up %+v", f)
			}
		})
	}
}

func TestContinueLaunchesARecipientWhoseStoredSayNamesAnotherMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		to     string
		atTurn bool
		launch bool
	}{
		{"re names the turn", "charlie", true, false},
		{"re names the instructions", "alpha", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.keptMembers(abcRoster...)
			turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
			re := int64(1)
			if tc.atTurn {
				re = turn.Seq
			}
			h.publishRe("kept", "bravo", kindSay, "clean", []string{tc.to}, re)
			h.script("bravo", on(promptTurn, says(kindSay, "alpha")))
			h.script("alpha", on(promptFollow, says(kindDone, "owner")))
			res := h.playKept()
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			if got := len(h.launchesWith("bravo", promptTurn)) == 1; got != tc.launch {
				t.Fatalf("launched %v: %+v", got, h.calls())
			}
			wantErrors(t, h.messages("kept"))
		})
	}
}

func TestStallPromptGivesTheFindingAndAnExtendNamingItRefreshes(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha", on(promptStall, withRe(says(kindExtend, "charlie"), "$re")), on(promptFollow, says(kindDone, "owner")))
	h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...)))
	h.script("bravo", on(promptFix, says(kindFixed, abcRoster...)))
	res, name := h.play(cfg)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	f := first(msgs, "bravo", kindFinding)
	if s := h.launchesWith("alpha", promptStall); len(s) != 1 || !strings.Contains(s[0].Stdin, reCommand("<extend or deadlock>", f.Seq)) {
		t.Fatalf("stall prompts %+v", s)
	}
	if reOf(first(msgs, "alpha", kindExtend)) != f.Seq || !strings.Contains(res.stdout, "extend: "+seqOf(f)+"\nto: charlie\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
	wantErrors(t, msgs)
}

func TestExtendWithAWrongReRefreshesNobodyAndTheDiscussionContinues(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha",
		on(promptStall, withRe(says(kindExtend, "charlie"), "1")),
		on(promptStall, withRe(says(kindExtend, "charlie"), "$re")),
		on(promptFollow, says(kindDone, "owner")),
	)
	h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...)))
	h.script("bravo", on(promptFix, says(kindFixed, abcRoster...)))
	res, name := h.play(cfg)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	ext := ofKind(msgs, kindExtend)
	if len(ext) != 2 || len(h.launchesWith("alpha", promptStall)) != 2 || strings.Count(res.stdout, "extend: ") != 1 {
		t.Fatalf("extends %+v\n%s", ext, res.stdout)
	}
	wantErrors(t, msgs, errLine("alpha", classBadExtend, ext[0].Seq))
	if !strings.Contains(res.stdout, "error: bad extend\nfrom: alpha\nseq: "+seqOf(ext[0])+"\n") {
		t.Fatalf("stdout\n%s", res.stdout)
	}
}

func TestDeadlockWithAWrongReIsBadThenNoDecisionAndWritesNoHalt(t *testing.T) {
	h := newHarness(t)
	cfg := stalled(h, abcRoster...)
	h.script("alpha", on(promptStall, withRe(says(kindDeadlock, "alpha", "bravo", "charlie", "owner"), "1")))
	res, name := h.play(cfg)
	if res.err == nil || res.err.Error() != classNoDecision || !strings.HasSuffix(res.stdout, "error: no decision\nfrom: alpha\noutcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	wantErrors(t, msgs, errLine("alpha", classBadDeadlock, first(msgs, "alpha", kindDeadlock).Seq), errLine("alpha", classNoDecision, 0))
	if c, _ := h.conv(name); c.Status != "open" || len(h.recordsOf(name).halts) != 0 || len(h.launchesWith("alpha", promptReport)) != 0 {
		t.Fatalf("status %s msgs %+v", c.Status, msgs)
	}
}

func TestFixedWithAWrongReDoesNotCountAndTheFixIsHandedOff(t *testing.T) {
	for _, seatRe := range []string{"$re", "1"} {
		t.Run("seat re "+seatRe, func(t *testing.T) {
			h := newHarness(t)
			bravoFinds(h, abcRoster...)
			h.script("charlie", on(promptDiscussion, says(kindAgree, abcRoster...)))
			h.script("bravo", on(promptFix, withRe(says(kindFixed, abcRoster...), "1")))
			h.script("alpha",
				on(promptDiscussion, says(kindAgree, abcRoster...)),
				on(promptFix, withRe(says(kindFixed, abcRoster...), seatRe)),
				on(promptFollow, says(kindDone, "owner")),
			)
			res, name := h.play(h.team("alpha", abcRoster...))
			msgs := h.messages(name)
			f := first(msgs, "bravo", kindFinding)
			if seatRe == "$re" {
				if res.err != nil || !strings.Contains(res.stdout, "fixed: "+seqOf(f)+"\nfrom: alpha\n") || !strings.HasSuffix(res.stdout, "outcome: done\n") {
					t.Fatalf("%v\n%s", res.err, res.stdout)
				}
				wantErrors(t, msgs)
				return
			}
			if res.err == nil || res.err.Error() != classNoFix {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			// Neither wrong fixed is named by an error. Only the handoff's miss is.
			wantErrors(t, msgs, errLine("alpha", classNoFix, 0))
		})
	}
}

// keptRejectedTurn stores a turn run rejects, then a valid turn to bravo that bravo answers.
func (h *harness) keptRejectedTurn(stored bool) client.Message {
	h.t.Helper()
	h.keptMembers(abcRoster...)
	bad := h.publish("kept", "alpha", kindTurn, "nope", nil)
	if stored {
		h.storedErr("", classBadTurn, bad.Seq, "bad turn/"+seqOf(bad))
	}
	h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
	h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	return bad
}

func TestContinueReportsAStoredRejectedTurnOnceAndDoesNotCountIt(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run("stored "+map[bool]string{true: "error", false: "no error"}[stored], func(t *testing.T) {
			h := newHarness(t)
			bad := h.keptRejectedTurn(stored)
			cfg := h.continueKept(agentsOf(abcRoster...))
			cfg.MaxTurns, cfg.MaxTurnsSet = 1, true
			stdout, _, err := h.runBounded(cfg)
			if err != nil || !strings.HasSuffix(stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", err, stdout)
			}
			msgs := h.messages("kept")
			wantErrors(t, msgs, errLine("", classBadTurn, bad.Seq))
			if len(h.launchesWith("alpha", promptFollow)) != 1 {
				t.Fatalf("the valid turn was not acted on: %+v", h.calls())
			}
		})
	}
}

func TestContinueMeetsAStoredFactErrorAndStoresNoSecondCopy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class string
		// setup stores the transcript and the earlier process's error, and returns that error's line.
		setup func(h *harness) string
	}{
		{
			name: "no reply", class: classNoReply,
			setup: func(h *harness) string {
				turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
				h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
				h.storedErr("charlie", classNoReply, turn.Seq, "no reply/charlie/"+seqOf(turn))
				h.script("charlie", on(promptTurn))
				return errLine("charlie", classNoReply, turn.Seq)
			},
		},
		{
			name: "bad turn naming a name outside the roster", class: classBadTurn,
			setup: func(h *harness) string {
				turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "zulu"})
				h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
				h.storedErr("zulu", classBadTurn, turn.Seq, "bad turn/zulu/"+seqOf(turn))
				return errLine("zulu", classBadTurn, turn.Seq)
			},
		},
		{
			name: "bad finding", class: classBadFinding,
			setup: func(h *harness) string {
				h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
				bad := h.publish("kept", "charlie", kindFinding, "not mine", abcRoster)
				h.storedErr("charlie", classBadFinding, bad.Seq, "bad finding/charlie/"+seqOf(bad))
				h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
				return errLine("charlie", classBadFinding, bad.Seq)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.keptMembers(abcRoster...)
			want := tc.setup(h)
			h.script("alpha", on(promptFollow, says(kindDone, "owner")))
			res := h.playKept()
			if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
				t.Fatalf("%v\n%s", res.err, res.stdout)
			}
			wantErrors(t, h.messages("kept"), want)
			if strings.Contains(res.stdout, "error: "+tc.class+"\n") {
				t.Fatalf("a second status group\n%s", res.stdout)
			}
		})
	}
}

func TestRefusedDerivedErrorStopsTheRun(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
	bad := h.publish("kept", "charlie", kindFinding, "not mine", abcRoster)
	h.publish("kept", "bravo", kindSay, "clean", []string{"alpha"})
	// Another caller took the derived id first with other content.
	h.storedErr("charlie", classNoReply, bad.Seq, "bad finding/charlie/"+seqOf(bad))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res := h.playKept()
	if res.err == nil || !strings.Contains(res.err.Error(), "publish error") || !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if n := len(ofKind(h.messages("kept"), kindError)); n != 1 {
		t.Fatalf("errors %d", n)
	}
	if c, _ := h.conv("kept"); c.Status != "open" {
		t.Fatalf("status %s", c.Status)
	}
}

func TestUnsettledErrorNamesTheStoppingDoneAndAReplayStoresNoSecondCopy(t *testing.T) {
	h := newHarness(t)
	earlyDone(h)
	res, name := h.play(h.team("alpha", "alpha", "bravo"))
	if !errors.Is(res.err, ErrUnresolved) {
		t.Fatalf("err %v\n%s", res.err, res.stdout)
	}
	msgs := h.messages(name)
	done := first(msgs, "alpha", kindDone)
	errs := ofKind(msgs, kindError)
	if len(errs) != 1 || errs[0].Body != classUnsettled || reOf(errs[0]) != done.Seq || errs[0].TxID != "unsettled/"+seqOf(done) {
		t.Fatalf("errors %+v", errs)
	}
	cfg := h.continueKept("")
	cfg.Conversation = name
	stdout, _, err := h.runBounded(cfg)
	if !errors.Is(err, ErrUnresolved) || strings.Contains(stdout, "error: ") {
		t.Fatalf("replay %v\n%s", err, stdout)
	}
	wantErrors(t, h.messages(name), errLine("", classUnsettled, done.Seq))
}

func TestContinueSkipsAFinishedSetWhoseOnlyFindingIsRejected(t *testing.T) {
	h := newHarness(t)
	h.keptMembers(abcRoster...)
	h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
	bad := h.publishRe("kept", "bravo", kindFinding, "names the instructions", abcRoster, 1)
	h.publish("kept", "bravo", kindSay, "found one", []string{"alpha"})
	h.publish("kept", "charlie", kindSay, "clean", []string{"alpha"})
	next := h.publish("kept", "alpha", kindTurn, "mode: serial\nnext", []string{"bravo"})
	h.script("bravo", on("next", says(kindSay, "alpha")))
	h.script("alpha", on(promptFollow, says(kindDone, "owner")))
	res := h.playKept()
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	msgs := h.messages("kept")
	if sets := h.recordsOf("kept").sets; len(sets) != 1 || sets[0].Turn != next.Seq {
		t.Fatalf("sets %+v", sets)
	}
	if n := len(h.launchesWith("alpha", promptFollow)); n != 1 {
		t.Fatalf("follow-ups %d", n)
	}
	wantErrors(t, msgs, errLine("bravo", classBadFinding, bad.Seq))
}
