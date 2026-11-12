// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"slices"
	"testing"

	"github.com/p3bot/hotseat/internal/client"
)

// tx builds a transcript with seq equal to the position plus one.
func tx(msgs ...client.Message) []client.Message {
	for i := range msgs {
		msgs[i].Seq = int64(i + 1)
	}
	return msgs
}

func msg(from, kind, body string, to ...string) client.Message {
	return client.Message{From: from, Kind: kind, Body: body, To: to}
}

var abc = []string{"a", "b", "c"}

func selfTargets() map[string]string {
	return map[string]string{"a": "a", "b": "b", "c": "c"}
}

func seqs(msgs []client.Message) []int64 {
	var out []int64
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

func TestFindingIsValidOnlyFromARecipientInsideItsSetWindow(t *testing.T) {
	l := newLedger(tx(
		msg("b", kindFinding, "before any set", abc...),        // 1
		msg("a", kindTurn, "mode: parallel\nreview", "a", "b"), // 2
		msg("b", kindFinding, "in window", abc...),             // 3
		msg("c", kindFinding, "not a recipient", abc...),       // 4
		msg("run", kindDiscuss, "2"),                           // 5
		msg("b", kindFinding, "after discuss", abc...),         // 6
		msg("a", kindTurn, "bad mode", "b"),                    // 7
		msg("b", kindFinding, "after a bad turn", abc...),      // 8
	), abc, selfTargets())
	if got := seqs(l.findings()); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("valid %v", got)
	}
	if got := seqs(l.findingsOf(2)); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("set findings %v", got)
	}
}

func TestFindingInTwoOpenWindowsBelongsToTheFirstSet(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindTurn, "mode: serial\nq", "c"),        // 2
		msg("c", kindFinding, "f", abc...),                // 3
		msg("run", kindDiscuss, "1"),                      // 4
		msg("run", kindSettled, "3 3"),                    // 5
		msg("c", kindFixed, "done", abc...),               // 6
		msg("c", kindFinding, "g", abc...),                // 7
	), abc, selfTargets())
	if got := seqs(l.findingsOf(1)); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("first set %v", got)
	}
	if got := seqs(l.findingsOf(2)); slices.Compare(got, []int64{7}) != 0 {
		t.Fatalf("queued set %v", got)
	}
}

func TestFindingDuringAnotherSetsDiscussionsOpensNothing(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindTurn, "mode: serial\nq", "c"),        // 2
		msg("c", kindFinding, "f", abc...),                // 3
		msg("run", kindDiscuss, "1"),                      // 4
		msg("c", kindFinding, "in a discussion", abc...),  // 5
		msg("run", kindSettled, "3 3"),                    // 6
		msg("c", kindFinding, "before the fix", abc...),   // 7
		msg("c", kindFixed, "done", abc...),               // 8
		msg("c", kindFinding, "after the fix", abc...),    // 9
	), abc, selfTargets())
	if l.valid(5) || l.valid(7) {
		t.Fatal("a finding inside the first set's discussions is valid")
	}
	if got := seqs(l.findings()); slices.Compare(got, []int64{3, 9}) != 0 {
		t.Fatalf("valid %v", got)
	}
	if got := seqs(l.findingsOf(2)); slices.Compare(got, []int64{9}) != 0 {
		t.Fatalf("queued set %v", got)
	}
}

func TestSetWithoutDiscussEndsAtItsTargetsNextTurnOrDone(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b"), // 1
		msg("b", kindFinding, "inside", abc...),      // 2
		msg("a", kindDone, "early", "owner"),         // 3
		msg("b", kindFinding, "after done", abc...),  // 4
	), abc, selfTargets())
	if got := seqs(l.findings()); slices.Compare(got, []int64{2}) != 0 {
		t.Fatalf("valid %v", got)
	}
	open := newLedger(tx(
		msg("b", kindTurn, "mode: parallel\nr", "c"),
		msg("c", kindFinding, "f", abc...),
	), abc, map[string]string{})
	if got := seqs(open.findings()); slices.Compare(got, []int64{2}) != 0 {
		t.Fatalf("a set with no target runs without end: %v", got)
	}
}

func TestUnresolvedNamesUnsettledAndUnfixedFindings(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c"), // 1
		msg("a", kindFinding, "f1", abc...),                    // 2
		msg("b", kindFinding, "f2", abc...),                    // 3
		msg("c", kindFinding, "f3", abc...),                    // 4
		msg("b", kindFinding, "f4", abc...),                    // 5
		msg("run", kindDiscuss, "1"),                           // 6
		msg("b", kindDismiss, "dup", abc...),                   // 7
		msg("run", kindSettled, settledBody(2, 7)),             // 8
		msg("run", kindSettled, settledBody(3, 3)),             // 9
		msg("b", kindFixed, "done", abc...),                    // 10
		msg("c", kindPropose, "bar", abc...),                   // 11
		msg("run", kindSettled, settledBody(4, 11)),            // 12
	), abc, selfTargets())
	unsettled, unfixed := l.unresolved()
	if slices.Compare(unsettled, []int64{5}) != 0 || slices.Compare(unfixed, []int64{4}) != 0 {
		t.Fatalf("unsettled %v unfixed %v", unsettled, unfixed)
	}
	if l.needsFix(2) || l.needsFix(3) || !l.needsFix(4) {
		t.Fatal("needsFix")
	}
	if _, ok := l.discussed(1); ok {
		t.Fatal("discussions ended with an open finding")
	}
	// A fixed stored at the cut does not count.
	if unsettled, unfixed := l.before(10).unresolved(); slices.Compare(unsettled, []int64{4, 5}) != 0 || slices.Compare(unfixed, []int64{3}) != 0 {
		t.Fatalf("cut unsettled %v unfixed %v", unsettled, unfixed)
	}
}

func TestFollowUpCountsOnlyATurnAfterTheDiscussionsEnded(t *testing.T) {
	base := []client.Message{
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"),          // 1
		msg("b", kindFinding, "f", abc...),                         // 2
		msg("run", kindDiscuss, "1"),                               // 3
		msg("a", kindTurn, "mode: serial\nposted in a stall", "c"), // 4
		msg("run", kindSettled, settledBody(2, 2)),                 // 5
	}
	turn := msg("a", kindTurn, "mode: parallel\nr", "b", "c")
	turn.Seq = 1
	l := newLedger(tx(append(slices.Clone(base), msg("b", kindFixed, "x", abc...))...), abc, selfTargets())
	if end, ok := l.discussed(1); !ok || end != 6 {
		t.Fatalf("end %d %v", end, ok)
	}
	if l.followedUp(turn) {
		t.Fatal("a turn posted before the end counted as the follow-up")
	}
	l = newLedger(tx(append(slices.Clone(base), msg("b", kindFixed, "x", abc...), msg("a", kindDone, "ok", "owner"))...), abc, selfTargets())
	if !l.followedUp(turn) {
		t.Fatal("a done after the end did not count")
	}
	empty := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b"),
		msg("run", kindDiscuss, "1"),
	), abc, selfTargets())
	if end, ok := empty.discussed(1); !ok || end != 2 {
		t.Fatalf("no finding ends at discuss: %d %v", end, ok)
	}
}

func TestHaltedAndReportedReadOnlyRunMarkers(t *testing.T) {
	l := newLedger(tx(
		msg("b", kindHalted, "1"),
		msg("b", kindError, "unsettled", "owner"),
	), abc, selfTargets())
	if _, ok := l.halted(); ok || l.reported(classUnsettled) {
		t.Fatal("a member marker counted")
	}
	l = newLedger(tx(
		msg("run", kindHalted, "7"),
		msg("run", kindError, "unsettled", "owner"),
	), abc, selfTargets())
	if f, ok := l.halted(); !ok || f != 7 || !l.reported(classUnsettled) {
		t.Fatal("run markers not read")
	}
}

func TestDiscussionStartsAfterTheSetsEarlierFindingsAndItsAsksEndAtItsMarker(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindFinding, "f", abc...),                // 2
		msg("c", kindFinding, "g", abc...),                // 3
		msg("c", kindAsk, "before discuss", "owner"),      // 4
		msg("run", kindDiscuss, "1"),                      // 5
		msg("c", kindAsk, "about f", "owner"),             // 6
		msg("run", kindSettled, "2 2"),                    // 7
		msg("a", kindAsk, "during the fix", "owner"),      // 8
		msg("b", kindFixed, "done", abc...),               // 9
		msg("a", kindAsk, "about g", "owner"),             // 10
		msg("owner", kindAsk, "not a member", "a"),        // 11
		msg("run", kindHalted, "3"),                       // 12
		msg("a", kindAsk, "in the report", "owner"),       // 13
	), abc, selfTargets())
	if start, ok := l.discussionStart(2); !ok || start != 5 {
		t.Fatalf("first start %d %v", start, ok)
	}
	if start, ok := l.discussionStart(3); !ok || start != 9 {
		t.Fatalf("second start %d %v", start, ok)
	}
	if got := l.asksOf(2); slices.Compare(got, []int64{6}) != 0 {
		t.Fatalf("first asks %v", got)
	}
	if got := l.asksOf(3); slices.Compare(got, []int64{10}) != 0 {
		t.Fatalf("second asks %v", got)
	}
}

func TestAggregateRequiresEveryFindingOnExactlyOneLine(t *testing.T) {
	findings := []int64{3, 4, 5}
	for _, tc := range []struct {
		name string
		body string
		want [][]int64
	}{
		{"each alone", "3\n4\n5", [][]int64{{3}, {4}, {5}}},
		{"kept first, groups by kept seq, duplicates sorted", "5 4\n3\n", [][]int64{{3}, {5, 4}}},
		{"duplicates sorted after the kept finding", "4 5 3", [][]int64{{4, 3, 5}}},
		{"missing finding", "3 4", nil},
		{"finding twice", "3 4\n4 5", nil},
		{"not a finding of the set", "3 4 5 6", nil},
		{"empty line", "3\n\n4 5", nil},
		{"not a seq", "3 x\n4 5", nil},
		{"empty body", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := aggregate(tc.body, findings)
			if ok != (tc.want != nil) || !slices.EqualFunc(got, tc.want, slices.Equal) {
				t.Fatalf("aggregate %q = %v %v, want %v", tc.body, got, ok, tc.want)
			}
		})
	}
}

func TestDroppedFindingIsNeitherDiscussedNorUnresolved(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c"),        // 1
		msg("b", kindFinding, "narrow", abc...),                       // 2
		msg("c", kindFinding, "wide", abc...),                         // 3
		msg("a", kindFinding, "other", abc...),                        // 4
		msg("run", kindDiscuss, "1"),                                  // 5
		msg("a", kindAggregate, "3 2\n4", abc...),                     // 6
		msg("run", kindIssues, issuesBody(1, [][]int64{{3, 2}, {4}})), // 7
		msg("run", kindSettled, "3 3"),                                // 8
	), abc, selfTargets())
	if got := seqs(l.findingsOf(1)); slices.Compare(got, []int64{3, 4}) != 0 {
		t.Fatalf("kept %v", got)
	}
	if !l.valid(2) || slices.Compare(l.duplicatesOf(3), []int64{2}) != 0 || slices.Compare(l.droppedOf(1), []int64{2}) != 0 {
		t.Fatalf("dropped 2: valid %v duplicates %v", l.valid(2), l.duplicatesOf(3))
	}
	if start, ok := l.discussionStart(3); !ok || start != 7 {
		t.Fatalf("first discussion starts after %d %v, want the issues marker 7", start, ok)
	}
	unsettled, unfixed := l.unresolved()
	if slices.Compare(unsettled, []int64{4}) != 0 || slices.Compare(unfixed, []int64{3}) != 0 {
		t.Fatalf("unsettled %v unfixed %v", unsettled, unfixed)
	}
	if l.needsAggregate(1) {
		t.Fatal("a set with an issues marker aggregates again")
	}
}

func TestSetAggregatesOnlyAfterDiscussWithTwoFindingsAndNothingSettled(t *testing.T) {
	base := []client.Message{
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindFinding, "f", abc...),                // 2
		msg("c", kindFinding, "g", abc...),                // 3
	}
	for _, tc := range []struct {
		name string
		more []client.Message
		want bool
	}{
		{"before discuss", nil, false},
		{"after discuss", []client.Message{msg("run", kindDiscuss, "1")}, true},
		{"discussion began before markers existed", []client.Message{msg("run", kindDiscuss, "1"), msg("run", kindSettled, "2 2")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := append(slices.Clone(base), tc.more...)
			l := newLedger(tx(msgs...), abc, selfTargets())
			if got := l.needsAggregate(1); got != tc.want {
				t.Fatalf("needsAggregate %v, want %v", got, tc.want)
			}
		})
	}
	one := newLedger(tx(base[0], base[1], msg("run", kindDiscuss, "1")), abc, selfTargets())
	if one.needsAggregate(1) {
		t.Fatal("a set with one finding aggregates")
	}
}

func TestRecipientIsUnansweredWithoutASayOrFindingToTheTurnSender(t *testing.T) {
	l := newLedger(tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c", "x"), // 1
		msg("a", kindSay, "verdicts", "a"),                          // 2
		msg("b", kindSay, "to all", "all"),                          // 3
		msg("c", kindFinding, "f", abc...),                          // 4
		msg("run", kindDiscuss, "1"),                                // 5
		msg("b", kindSay, "late", "a"),                              // 6
	), abc, selfTargets())
	if got := l.unanswered(l.sets[0]); slices.Compare(got, []string{"b"}) != 0 {
		t.Fatalf("unanswered %v, want b", got)
	}
}
