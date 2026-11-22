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

// seatA is the fixers of a ledger whose seat is a and that has no backup.
var seatA = []string{"a"}

func seqs(msgs []client.Message) []int64 {
	var out []int64
	for _, m := range msgs {
		out = append(out, m.Seq)
	}
	return out
}

// ledgerOf reads msgs and recs with roster abc, self targets, and seat a.
func ledgerOf(msgs []client.Message, recs records) ledger {
	return newLedger(msgs, recs, abc, selfTargets(), seatA)
}

func setRec(turn, after int64, findings ...int64) client.Set {
	if findings == nil {
		findings = []int64{}
	}
	return client.Set{Turn: turn, Findings: findings, After: after}
}

func groupingRec(turn, after int64, drops ...client.Drop) client.Grouping {
	return client.Grouping{Turn: turn, Drops: drops, After: after}
}

func settleRec(finding, proposal int64, kind string, after int64) client.Settle {
	return client.Settle{Finding: finding, Proposal: proposal, ProposalKind: kind, After: after}
}

func fixRec(s client.Settle, fixed int64) client.Fix {
	return client.Fix{Finding: s.Finding, SettledAfter: s.After, Fixed: fixed}
}

func TestClosedSetsFindingsAreItsRecordsAndAnOpenSetsComeFromItsWindow(t *testing.T) {
	msgs := tx(
		msg("b", kindFinding, "before any set", abc...),        // 1
		msg("a", kindTurn, "mode: parallel\nreview", "a", "b"), // 2
		msg("b", kindFinding, "in window", abc...),             // 3
		msg("c", kindFinding, "not a recipient", abc...),       // 4
		msg("b", kindFinding, "after the set record", abc...),  // 5
		msg("a", kindTurn, "bad mode", "b"),                    // 6
		msg("b", kindFinding, "after a bad turn", abc...),      // 7
	)
	open := ledgerOf(msgs, records{})
	// The seat's later turn ends the open set, even with a mode run rejects.
	if got := seqs(open.findingsOf(2)); slices.Compare(got, []int64{3, 5}) != 0 {
		t.Fatalf("open set findings %v", got)
	}
	closed := ledgerOf(msgs, records{sets: []client.Set{setRec(2, 4, 3)}, groupings: []client.Grouping{groupingRec(2, 4)}})
	if got := seqs(closed.findings()); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("closed set findings %v", got)
	}
	// The record is the closed set's membership even where the window would take more.
	none := ledgerOf(msgs, records{sets: []client.Set{setRec(2, 4)}})
	if got := seqs(none.findings()); len(got) != 0 {
		t.Fatalf("a set recorded with no findings has %v", got)
	}
}

func TestFindingInTwoOpenWindowsBelongsToTheFirstSet(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindTurn, "mode: serial\nq", "c"),        // 2
		msg("c", kindFinding, "f", abc...),                // 3
		withSeqRe(msg("c", kindFixed, "done", abc...), 3), // 4
		msg("c", kindFinding, "g", abc...),                // 5
	)
	if got := seqs(ledgerOf(msgs[:3], records{}).findingsOf(1)); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("first set %v", got)
	}
	s := settleRec(3, 3, kindFinding, 3)
	l := ledgerOf(msgs, records{
		sets:      []client.Set{setRec(1, 3, 3)},
		groupings: []client.Grouping{groupingRec(1, 3)},
		settles:   []client.Settle{s},
		fixes:     []client.Fix{fixRec(s, 4)},
	})
	if got := seqs(l.findingsOf(1)); slices.Compare(got, []int64{3}) != 0 {
		t.Fatalf("first set %v", got)
	}
	if got := seqs(l.findingsOf(2)); slices.Compare(got, []int64{5}) != 0 {
		t.Fatalf("queued set %v", got)
	}
}

func TestFindingDuringAnotherSetsDiscussionsOpensNothing(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindTurn, "mode: serial\nq", "c"),        // 2
		msg("c", kindFinding, "f", abc...),                // 3
		msg("c", kindFinding, "in a discussion", abc...),  // 4
		msg("c", kindFinding, "before the fix", abc...),   // 5
		withSeqRe(msg("c", kindFixed, "done", abc...), 3), // 6
		msg("c", kindFinding, "after the fix", abc...),    // 7
	)
	s := settleRec(3, 3, kindFinding, 4)
	l := ledgerOf(msgs, records{
		sets:      []client.Set{setRec(1, 3, 3)},
		groupings: []client.Grouping{groupingRec(1, 3)},
		settles:   []client.Settle{s},
		fixes:     []client.Fix{fixRec(s, 6)},
	})
	if l.valid(4) || l.valid(5) {
		t.Fatal("a finding inside the first set's discussions is valid")
	}
	if got := seqs(l.findings()); slices.Compare(got, []int64{3, 7}) != 0 {
		t.Fatalf("valid %v", got)
	}
	if got := seqs(l.findingsOf(2)); slices.Compare(got, []int64{7}) != 0 {
		t.Fatalf("queued set %v", got)
	}
	// A set with two findings and no grouping has closed and its discussions have not opened, so they have not ended.
	ungrouped := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindTurn, "mode: serial\nq", "c"),        // 2
		msg("c", kindFinding, "f", abc...),                // 3
		msg("c", kindFinding, "g", abc...),                // 4
		msg("c", kindFinding, "before the grouping", abc...),
	), records{sets: []client.Set{setRec(1, 4, 3, 4)}})
	if ungrouped.valid(5) {
		t.Fatal("a finding before the grouping opened a discussion")
	}
}

func TestSetWithNoFindingsOpensAndEndsItsDiscussionsAtItsSetRecord(t *testing.T) {
	turn := msg("a", kindTurn, "mode: parallel\nr", "b")
	turn.Seq = 1
	msgs := tx(
		turn,                                  // 1
		msg("b", kindSay, "clean", "a"),       // 2
		msg("b", kindFinding, "late", abc...), // 3
		msg("a", kindTurn, "mode: serial\nnext", "b"), // 4
		msg("b", kindFinding, "next", abc...),         // 5
	)
	recs := records{sets: []client.Set{setRec(1, 2)}}
	l := ledgerOf(msgs, recs)
	if start, ok := l.opened(1); !ok || start != 2 {
		t.Fatalf("opened %d %v", start, ok)
	}
	if end, ok := l.discussed(1); !ok || end != 2 {
		t.Fatalf("discussed %d %v", end, ok)
	}
	if l.valid(3) || slices.Compare(seqs(l.findingsOf(4)), []int64{5}) != 0 {
		t.Fatalf("late finding valid %v, next set %v", l.valid(3), seqs(l.findingsOf(4)))
	}
	if !l.followedUp(turn) {
		t.Fatal("a turn after the set record is not the follow-up")
	}
	if ledgerOf(msgs[:3], recs).followedUp(turn) {
		t.Fatal("a set with no later turn is followed up")
	}
}

func TestOpenSetEndsAtItsTargetsNextTurnOrDone(t *testing.T) {
	l := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b"), // 1
		msg("b", kindFinding, "inside", abc...),      // 2
		msg("a", kindDone, "early", "owner"),         // 3
		msg("b", kindFinding, "after done", abc...),  // 4
	), records{})
	if got := seqs(l.findings()); slices.Compare(got, []int64{2}) != 0 {
		t.Fatalf("valid %v", got)
	}
	open := newLedger(tx(
		msg("b", kindTurn, "mode: parallel\nr", "c"),
		msg("c", kindFinding, "f", abc...),
	), records{}, abc, map[string]string{}, seatA)
	if got := seqs(open.findings()); slices.Compare(got, []int64{2}) != 0 {
		t.Fatalf("a set with no target runs without end: %v", got)
	}
}

func TestUnresolvedNamesUnsettledAndUnfixedFindings(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c"), // 1
		msg("a", kindFinding, "f1", abc...),                    // 2
		msg("b", kindFinding, "f2", abc...),                    // 3
		msg("c", kindFinding, "f3", abc...),                    // 4
		msg("b", kindFinding, "f4", abc...),                    // 5
		withSeqRe(msg("b", kindDismiss, "dup", abc...), 2),     // 6
		msg("b", kindFixed, "done", abc...),                    // 7
		withSeqRe(msg("c", kindPropose, "bar", abc...), 4),     // 8
	)
	dismissed := settleRec(2, 6, kindDismiss, 6)
	fixed := settleRec(3, 3, kindFinding, 6)
	l := ledgerOf(msgs, records{
		sets:      []client.Set{setRec(1, 5, 2, 3, 4, 5)},
		groupings: []client.Grouping{groupingRec(1, 5)},
		settles:   []client.Settle{dismissed, fixed, settleRec(4, 8, kindPropose, 8)},
		fixes:     []client.Fix{fixRec(fixed, 7)},
	})
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
	// A fix whose fixed is at the cut does not count, and neither does a settle whose after is at it.
	if unsettled, unfixed := l.before(7).unresolved(); slices.Compare(unsettled, []int64{4, 5}) != 0 || slices.Compare(unfixed, []int64{3}) != 0 {
		t.Fatalf("cut unsettled %v unfixed %v", unsettled, unfixed)
	}
}

func TestStopBeforeASetRecordReadsTheOpenSetFromItsWindow(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "b"), // 1
		msg("b", kindFinding, "f", abc...),           // 2
		msg("a", kindDone, "early", "owner"),         // 3
	)
	l := ledgerOf(msgs, records{sets: []client.Set{setRec(1, 4, 2)}, groupings: []client.Grouping{groupingRec(1, 4)}})
	cut := l.before(3)
	if cut.recorded(1) {
		t.Fatal("a set record after the done counted")
	}
	if unsettled, _ := cut.unresolved(); slices.Compare(unsettled, []int64{2}) != 0 {
		t.Fatalf("unsettled %v", unsettled)
	}
	halted := ledgerOf(msgs, records{halts: []client.Halt{{Finding: 2, After: 3}}})
	if _, ok := halted.before(3).halted(); ok {
		t.Fatal("a halt at the done counted")
	}
	if _, ok := halted.before(4).halted(); !ok {
		t.Fatal("a halt below the done did not count")
	}
}

func TestOnlyACreditableFixedFixesASettledChangeWithNoFixRecord(t *testing.T) {
	base := []client.Message{
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindFinding, "f", abc...),                // 2
		msg("c", kindAgree, "yes", abc...),                // 3
	}
	s := settleRec(2, 2, kindFinding, 3)
	recs := records{sets: []client.Set{setRec(1, 2, 2)}, groupings: []client.Grouping{groupingRec(1, 2)}, settles: []client.Settle{s}}
	for _, tc := range []struct {
		name   string
		fixed  client.Message
		fixers []string
		want   bool
	}{
		{"finder with re", withSeqRe(msg("b", kindFixed, "x", abc...), 2), seatA, true},
		{"seat with re", withSeqRe(msg("a", kindFixed, "x", abc...), 2), seatA, true},
		{"backup with re", withSeqRe(msg("c", kindFixed, "x", abc...), 2), []string{"a", "c"}, true},
		{"another member", withSeqRe(msg("c", kindFixed, "x", abc...), 2), seatA, false},
		{"finder with no re", msg("b", kindFixed, "x", abc...), seatA, false},
		{"finder naming another message", withSeqRe(msg("b", kindFixed, "x", abc...), 1), seatA, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLedger(tx(append(slices.Clone(base), tc.fixed)...), recs, abc, selfTargets(), tc.fixers)
			if l.needsFix(2) == tc.want {
				t.Fatalf("fixed %v, want %v", !l.needsFix(2), tc.want)
			}
			if _, unfixed := l.unresolved(); (len(unfixed) == 0) != tc.want {
				t.Fatalf("unfixed %v", unfixed)
			}
		})
	}
	// A fixed stored before the settle's after is not creditable, and the first creditable one is credited.
	l := ledgerOf(tx(
		base[0], base[1],
		withSeqRe(msg("b", kindFixed, "early", abc...), 2), // 3
		withSeqRe(msg("b", kindFixed, "one", abc...), 2),   // 4
		withSeqRe(msg("b", kindFixed, "two", abc...), 2),   // 5
	), records{sets: recs.sets, groupings: recs.groupings, settles: []client.Settle{s}})
	if fixed, ok := l.creditable(2); !ok || fixed != 4 {
		t.Fatalf("credited %d %v, want 4", fixed, ok)
	}
	// A fix record names its own fixed, which need not be creditable.
	recorded := ledgerOf(tx(append(slices.Clone(base), msg("c", kindFixed, "x", abc...))...),
		records{sets: recs.sets, groupings: recs.groupings, settles: []client.Settle{s}, fixes: []client.Fix{fixRec(s, 4)}})
	if fixed, ok := recorded.fixOf(2); !ok || fixed != 4 || recorded.needsFix(2) {
		t.Fatalf("recorded fix %d %v", fixed, ok)
	}
}

func TestFollowUpCountsOnlyATurnAfterTheDiscussionsEnded(t *testing.T) {
	base := []client.Message{
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"),          // 1
		msg("b", kindFinding, "f", abc...),                         // 2
		msg("a", kindTurn, "mode: serial\nposted in a stall", "c"), // 3
		withSeqRe(msg("b", kindFixed, "x", abc...), 2),             // 4
	}
	turn := base[0]
	turn.Seq = 1
	s := settleRec(2, 2, kindFinding, 3)
	for _, tc := range []struct {
		name  string
		fixes []client.Fix
	}{
		{"fix record", []client.Fix{fixRec(s, 4)}},
		{"creditable fixed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := records{
				sets:      []client.Set{setRec(1, 2, 2)},
				groupings: []client.Grouping{groupingRec(1, 2)},
				settles:   []client.Settle{s},
				fixes:     tc.fixes,
			}
			l := ledgerOf(tx(slices.Clone(base)...), recs)
			if end, ok := l.discussed(1); !ok || end != 4 {
				t.Fatalf("end %d %v", end, ok)
			}
			if l.followedUp(turn) {
				t.Fatal("a turn posted before the end counted as the follow-up")
			}
			l = ledgerOf(tx(append(slices.Clone(base), msg("a", kindDone, "ok", "owner"))...), recs)
			if !l.followedUp(turn) {
				t.Fatal("a done after the end did not count")
			}
		})
	}
}

func TestMarkerAndAnnouncementMessagesAreNotState(t *testing.T) {
	l := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nr", "b"), // 1
		msg("b", kindFinding, "f", abc...),           // 2
		msg("run", "discuss", "1"),                   // 3
		msg("run", "issues", "1\n2"),                 // 4
		withSeqRe(msg("run", kindSettled, "settled finding 2: finding by b, fix follows", "owner"), 2), // 5
		msg("run", "halted", "2"), // 6
	), records{})
	if l.recorded(1) || l.isGrouped(1) {
		t.Fatal("a marker closed or grouped the set")
	}
	if _, ok := l.settledOf(2); ok {
		t.Fatal("an announcement settled the finding")
	}
	if _, ok := l.halted(); ok {
		t.Fatal("a halted marker halted the run")
	}
}

func TestDiscussionStartsAfterTheSetsEarlierFindingsAndItsAsksEndAtItsRecords(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindFinding, "f", abc...),                // 2
		msg("c", kindFinding, "g", abc...),                // 3
		msg("c", kindAsk, "before the grouping", "owner"), // 4
		msg("a", kindAggregate, "2\n3", abc...),           // 5
		msg("c", kindAsk, "about f", "owner"),             // 6
		msg("c", kindAgree, "yes", abc...),                // 7
		msg("a", kindAsk, "during the fix", "owner"),      // 8
		withSeqRe(msg("b", kindFixed, "done", abc...), 2), // 9
		msg("a", kindAsk, "about g", "owner"),             // 10
		msg("owner", kindAsk, "not a member", "a"),        // 11
		msg("a", kindAsk, "in the report", "owner"),       // 12
	)
	s := settleRec(2, 2, kindFinding, 7)
	l := ledgerOf(msgs, records{
		sets:      []client.Set{setRec(1, 4, 2, 3)},
		groupings: []client.Grouping{groupingRec(1, 5)},
		settles:   []client.Settle{s},
		fixes:     []client.Fix{fixRec(s, 9)},
		halts:     []client.Halt{{Finding: 3, After: 11}},
	})
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

func TestGroupingDropsAreEachDuplicateAndTheFindingThatKeptItsIssue(t *testing.T) {
	groups, ok := aggregate("5 4\n3", []int64{3, 4, 5})
	if !ok {
		t.Fatal("aggregate refused")
	}
	if got := drops(groups); !slices.Equal(got, []client.Drop{{Finding: 4, Kept: 5}}) {
		t.Fatalf("drops %v", got)
	}
	if got := drops(singles([]int64{3, 4})); len(got) != 0 {
		t.Fatalf("singles drop %v", got)
	}
}

func TestDroppedFindingIsNeitherDiscussedNorUnresolved(t *testing.T) {
	l := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c"), // 1
		msg("b", kindFinding, "narrow", abc...),                // 2
		msg("c", kindFinding, "wide", abc...),                  // 3
		msg("a", kindFinding, "other", abc...),                 // 4
		msg("a", kindAggregate, "3 2\n4", abc...),              // 5
		msg("b", kindAgree, "yes", abc...),                     // 6
	), records{
		sets:      []client.Set{setRec(1, 4, 2, 3, 4)},
		groupings: []client.Grouping{groupingRec(1, 5, client.Drop{Finding: 2, Kept: 3})},
		settles:   []client.Settle{settleRec(3, 3, kindFinding, 6)},
	})
	if got := seqs(l.findingsOf(1)); slices.Compare(got, []int64{3, 4}) != 0 {
		t.Fatalf("kept %v", got)
	}
	if !l.valid(2) || slices.Compare(l.duplicatesOf(3), []int64{2}) != 0 || slices.Compare(l.droppedOf(1), []int64{2}) != 0 {
		t.Fatalf("dropped 2: valid %v duplicates %v", l.valid(2), l.duplicatesOf(3))
	}
	if start, ok := l.discussionStart(3); !ok || start != 5 {
		t.Fatalf("first discussion starts after %d %v, want the grouping's after 5", start, ok)
	}
	unsettled, unfixed := l.unresolved()
	if slices.Compare(unsettled, []int64{4}) != 0 || slices.Compare(unfixed, []int64{3}) != 0 {
		t.Fatalf("unsettled %v unfixed %v", unsettled, unfixed)
	}
	if l.needsAggregate(1) {
		t.Fatal("a grouped set aggregates again")
	}
}

func TestSetAggregatesOnlyWithTwoRecordedFindingsAndNoGrouping(t *testing.T) {
	msgs := tx(
		msg("a", kindTurn, "mode: parallel\nr", "b", "c"), // 1
		msg("b", kindFinding, "f", abc...),                // 2
		msg("c", kindFinding, "g", abc...),                // 3
	)
	for _, tc := range []struct {
		name string
		recs records
		want bool
	}{
		{"no set record", records{}, false},
		{"two findings and no grouping", records{sets: []client.Set{setRec(1, 3, 2, 3)}}, true},
		{"grouped", records{sets: []client.Set{setRec(1, 3, 2, 3)}, groupings: []client.Grouping{groupingRec(1, 3)}}, false},
		{"one finding", records{sets: []client.Set{setRec(1, 3, 2)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ledgerOf(msgs, tc.recs).needsAggregate(1); got != tc.want {
				t.Fatalf("needsAggregate %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRecipientIsUnansweredWithoutASayOrFindingToTheTurnSender(t *testing.T) {
	l := ledgerOf(tx(
		msg("a", kindTurn, "mode: parallel\nr", "a", "b", "c", "x"), // 1
		msg("a", kindSay, "verdicts", "a"),                          // 2
		msg("b", kindSay, "to all", "all"),                          // 3
		msg("c", kindFinding, "f", abc...),                          // 4
		msg("b", kindSay, "late", "a"),                              // 5
	), records{sets: []client.Set{setRec(1, 4, 4)}, groupings: []client.Grouping{groupingRec(1, 4)}})
	if got := l.unanswered(l.turns[0]); slices.Compare(got, []string{"b"}) != 0 {
		t.Fatalf("unanswered %v, want b", got)
	}
}

func TestRecordsKeepEachKeyOnce(t *testing.T) {
	var r records
	set := setRec(1, 3, 2)
	g := groupingRec(1, 3)
	r.add(client.Result{Set: &set, Grouping: &g})
	r.add(client.Result{Sets: &[]client.Set{set}, Groupings: &[]client.Grouping{g}})
	h := client.Halt{Finding: 2, After: 4}
	r.add(client.Result{Halt: &h})
	r.add(client.Result{Halts: &[]client.Halt{h}})
	if len(r.sets) != 1 || len(r.groupings) != 1 || len(r.halts) != 1 {
		t.Fatalf("records %+v", r)
	}
}

func TestAnnouncementRefusesAProposalMissingFromTheTranscript(t *testing.T) {
	e := &engine{}
	if err := e.announce(ledgerOf(nil, records{}), settleRec(2, 2, kindFinding, 3)); err == nil {
		t.Fatal("announced a settle whose proposal sender is unknown")
	}
}

func TestUnderWayIsTheFirstUnresolvedStartedFindingAndNoneOnceAHaltNamesIt(t *testing.T) {
	base := tx(
		msg("a", kindTurn, "mode: parallel\nreview", "b", "c"),
		msg("b", kindFinding, "one", abc...),
		msg("c", kindFinding, "two", abc...),
		msg("b", kindSay, "found", "a"),
		msg("c", kindSay, "found", "a"),
	)
	fixed := msg("b", kindFixed, "fixed", abc...)
	fixed.Seq = 6
	fixed.Re = &base[1].Seq
	set := setRec(1, 5, 2, 3)
	settle := settleRec(2, 2, kindFinding, 5)
	cases := []struct {
		name string
		msgs []client.Message
		recs records
		want int64
	}{
		{"set not grouped", base, records{sets: []client.Set{set}}, 0},
		{"grouped", base, records{sets: []client.Set{set}, groupings: []client.Grouping{groupingRec(1, 5)}}, 2},
		{"settled change awaiting its fixed", base, records{sets: []client.Set{set}, groupings: []client.Grouping{groupingRec(1, 5)}, settles: []client.Settle{settle}}, 2},
		{"fixed", append(slices.Clone(base), fixed), records{sets: []client.Set{set}, groupings: []client.Grouping{groupingRec(1, 5)}, settles: []client.Settle{settle}}, 3},
		{"halt on the first reports neither", base, records{sets: []client.Set{set}, groupings: []client.Grouping{groupingRec(1, 5)}, halts: []client.Halt{{Finding: 2, After: 5}}}, 0},
		{"halt on the last", append(slices.Clone(base), fixed), records{sets: []client.Set{set}, groupings: []client.Grouping{groupingRec(1, 5)}, settles: []client.Settle{settle}, halts: []client.Halt{{Finding: 3, After: 6}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ledgerOf(tc.msgs, tc.recs).underWay()
			if ok != (tc.want != 0) || got != tc.want {
				t.Fatalf("under way %d %v, want %d", got, ok, tc.want)
			}
		})
	}
}
