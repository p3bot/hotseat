// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// recordStore opens a store with conversation job and returns a publisher of kinds into it.
func recordStore(t *testing.T) (*Store, func(kind string) int64) {
	t.Helper()
	st := openStore(t)
	if _, _, err := st.Create(context.Background(), "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	return st, func(kind string) int64 {
		t.Helper()
		n++
		m, _, _, err := st.Publish(context.Background(), Publish{
			Conversation: "job", From: "alice", To: []string{}, Body: kind,
			TxID: fmt.Sprint(n), Kind: kind, Time: "t",
		})
		if err != nil {
			t.Fatal(err)
		}
		return m.Seq
	}
}

func records(t *testing.T, st *Store) Records {
	t.Helper()
	r, err := st.Records(context.Background(), "job")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertNoRecords(t *testing.T, st *Store) {
	t.Helper()
	r := records(t, st)
	if len(r.Sets)+len(r.Groupings)+len(r.Settles)+len(r.Fixes)+len(r.Halts) != 0 {
		t.Fatalf("records written: %+v", r)
	}
}

func mustSet(t *testing.T, st *Store, turn int64, findings ...int64) Set {
	t.Helper()
	set, _, _, err := st.WriteSet(context.Background(), "job", turn, findings)
	if err != nil {
		t.Fatalf("set %d %v: %v", turn, findings, err)
	}
	return set
}

func mustGrouping(t *testing.T, st *Store, turn int64, drops ...Drop) Grouping {
	t.Helper()
	g, _, err := st.WriteGrouping(context.Background(), "job", turn, drops)
	if err != nil {
		t.Fatalf("grouping %d %v: %v", turn, drops, err)
	}
	return g
}

func mustSettle(t *testing.T, st *Store, finding, proposal int64, kind string) Settle {
	t.Helper()
	s, _, err := st.WriteSettle(context.Background(), "job", finding, proposal, kind)
	if err != nil {
		t.Fatalf("settle %d %d %s: %v", finding, proposal, kind, err)
	}
	return s
}

func TestSetStoresFindingsAndAfterIsTheHighestSeqAtCommit(t *testing.T) {
	st, put := recordStore(t)
	turn := put("turn")
	f1, _, f2, f3 := put("finding"), put("say"), put("finding"), put("finding")
	last := put("error")
	set, grouping, already, err := st.WriteSet(context.Background(), "job", turn, []int64{f3, f1, f2})
	if err != nil || already || grouping != nil {
		t.Fatalf("set = %+v %+v %v %v", set, grouping, already, err)
	}
	if !slices.Equal(set.Findings, []int64{f1, f2, f3}) || set.After != last || set.Turn != turn {
		t.Fatalf("set = %+v, want findings ascending and after %d", set, last)
	}
	r := records(t, st)
	if len(r.Sets) != 1 || !slices.Equal(r.Sets[0].Findings, []int64{f1, f2, f3}) || r.Sets[0].After != last {
		t.Fatalf("records = %+v", r)
	}
}

func TestSetReplayComparesFindingsAsASetAndKeepsTheOriginalAfter(t *testing.T) {
	st, put := recordStore(t)
	turn := put("turn")
	f1, f2, f3 := put("finding"), put("finding"), put("finding")
	first := mustSet(t, st, turn, f1, f2, f3)
	put("say")
	again, _, already, err := st.WriteSet(context.Background(), "job", turn, []int64{f3, f2, f1})
	if err != nil || !already || again.After != first.After || !slices.Equal(again.Findings, first.Findings) {
		t.Fatalf("replay = %+v %v %v, want %+v", again, already, err, first)
	}
	if _, _, _, err := st.WriteSet(context.Background(), "job", turn, []int64{f1, f2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("other findings = %v", err)
	}
	if r := records(t, st); len(r.Sets) != 1 || len(r.Sets[0].Findings) != 3 {
		t.Fatalf("records = %+v", r)
	}
}

func TestSetReferencingAMessageOfTheWrongKindIsRefusedAndWritesNothing(t *testing.T) {
	st, put := recordStore(t)
	turn, say, finding := put("turn"), put("say"), put("finding")
	for name, tc := range map[string]struct {
		turn     int64
		findings []int64
	}{
		"say as turn":    {say, []int64{finding}},
		"say as finding": {turn, []int64{finding, say}},
		"missing turn":   {99, nil},
	} {
		if _, _, _, err := st.WriteSet(context.Background(), "job", tc.turn, tc.findings); !errors.Is(err, ErrRecordReference) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	assertNoRecords(t, st)
}

func TestEachKindForeignKeyRefusesAMessageOfTheWrongKind(t *testing.T) {
	st, put := recordStore(t)
	turn, finding, say := put("turn"), put("finding"), put("say")
	propose, dismiss := put("propose"), put("dismiss")
	ctx := context.Background()
	if _, _, _, err := st.WriteSet(ctx, "job", say, nil); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("turn_kind: %v", err)
	}
	if _, _, _, err := st.WriteSet(ctx, "job", turn, []int64{say}); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("finding_kind: %v", err)
	}
	mustSet(t, st, turn, finding)
	for _, tc := range []struct {
		proposal int64
		kind     string
	}{{say, "propose"}, {dismiss, "propose"}, {propose, "dismiss"}} {
		if _, _, err := st.WriteSettle(ctx, "job", finding, tc.proposal, tc.kind); !errors.Is(err, ErrRecordReference) {
			t.Fatalf("proposal_kind %s for %d: %v", tc.kind, tc.proposal, err)
		}
	}
	mustSettle(t, st, finding, propose, "propose")
	notFixed := put("say")
	if _, _, err := st.WriteFix(ctx, "job", finding, notFixed); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("fixed_kind: %v", err)
	}
}

func TestSettleProposalKindOutsideFindingProposeAndDismissIsRefused(t *testing.T) {
	st, put := recordStore(t)
	turn, finding := put("turn"), put("finding")
	mustSet(t, st, turn, finding)
	say := put("say")
	if _, _, err := st.WriteSettle(context.Background(), "job", finding, say, "say"); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	if r := records(t, st); len(r.Settles) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestSingleFindingSetWritesItsGroupingWithNoDrops(t *testing.T) {
	st, put := recordStore(t)
	turn, finding := put("turn"), put("finding")
	set, grouping, _, err := st.WriteSet(context.Background(), "job", turn, []int64{finding})
	if err != nil || grouping == nil || grouping.Turn != turn || len(grouping.Drops) != 0 || grouping.After != set.After {
		t.Fatalf("set = %+v grouping = %+v err = %v", set, grouping, err)
	}
	put("say")
	_, again, already, err := st.WriteSet(context.Background(), "job", turn, []int64{finding})
	if err != nil || !already || again == nil || again.Turn != turn || again.After != grouping.After || len(again.Drops) != 0 {
		t.Fatalf("replay grouping = %+v %v %v", again, already, err)
	}
	r := records(t, st)
	if len(r.Groupings) != 1 || r.Groupings[0].Turn != turn || r.Groupings[0].Drops == nil || len(r.Groupings[0].Drops) != 0 {
		t.Fatalf("records = %+v", r)
	}
	if _, already, err := st.WriteGrouping(context.Background(), "job", turn, nil); err != nil || !already {
		t.Fatalf("grouping replay = %v %v", already, err)
	}
}

func TestEmptySetWritesOnlyTheSet(t *testing.T) {
	st, put := recordStore(t)
	turn := put("turn")
	set, grouping, _, err := st.WriteSet(context.Background(), "job", turn, nil)
	if err != nil || grouping != nil || set.Findings == nil || len(set.Findings) != 0 {
		t.Fatalf("set = %+v %+v %v", set, grouping, err)
	}
	if r := records(t, st); len(r.Sets) != 1 || len(r.Groupings) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestFindingAlreadyInAnotherSetIsRefused(t *testing.T) {
	st, put := recordStore(t)
	t1, f1 := put("turn"), put("finding")
	t2 := put("turn")
	mustSet(t, st, t1, f1)
	if _, _, _, err := st.WriteSet(context.Background(), "job", t2, []int64{f1}); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	if r := records(t, st); len(r.Sets) != 1 {
		t.Fatalf("records = %+v", r)
	}
}

// twoSets stores set a with three findings and set b with one, and returns their seqs.
func twoSets(t *testing.T, st *Store, put func(string) int64) (a int64, af []int64, b, bf int64) {
	t.Helper()
	a = put("turn")
	af = []int64{put("finding"), put("finding"), put("finding")}
	b = put("turn")
	bf = put("finding")
	mustSet(t, st, a, af...)
	mustSet(t, st, b, bf)
	return a, af, b, bf
}

func assertNoDrops(t *testing.T, st *Store, turn int64) {
	t.Helper()
	for _, g := range records(t, st).Groupings {
		if g.Turn == turn {
			t.Fatalf("grouping %d stored: %+v", turn, g)
		}
	}
}

func TestGroupingThatDropsAFindingOfAnotherSetIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, bf := twoSets(t, st, put)
	if _, _, err := st.WriteGrouping(context.Background(), "job", a, []Drop{{Finding: bf, Kept: af[0]}}); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	assertNoDrops(t, st, a)
}

func TestGroupingThatDropsTheKeptFindingItselfIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	if _, _, err := st.WriteGrouping(context.Background(), "job", a, []Drop{{Finding: af[1], Kept: af[1]}}); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	assertNoDrops(t, st, a)
}

func TestKeptFindingIsNotItselfDropped(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	drops := []Drop{{Finding: af[0], Kept: af[1]}, {Finding: af[1], Kept: af[2]}}
	if _, _, err := st.WriteGrouping(context.Background(), "job", a, drops); !errors.Is(err, ErrKeptDropped) {
		t.Fatalf("err = %v", err)
	}
	assertNoDrops(t, st, a)
}

func TestGroupingWithoutASetIsRefused(t *testing.T) {
	st, put := recordStore(t)
	turn := put("turn")
	if _, _, err := st.WriteGrouping(context.Background(), "job", turn, nil); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	assertNoRecords(t, st)
}

func TestGroupingReplayComparesDropsAsASet(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	drops := []Drop{{Finding: af[2], Kept: af[0]}, {Finding: af[1], Kept: af[0]}}
	first := mustGrouping(t, st, a, drops...)
	if !slices.Equal(first.Drops, []Drop{drops[1], drops[0]}) {
		t.Fatalf("drops not in finding order: %+v", first.Drops)
	}
	put("say")
	again, already, err := st.WriteGrouping(context.Background(), "job", a, []Drop{drops[1], drops[0]})
	if err != nil || !already || again.After != first.After || !slices.Equal(again.Drops, first.Drops) {
		t.Fatalf("replay = %+v %v %v", again, already, err)
	}
	if _, _, err := st.WriteGrouping(context.Background(), "job", a, drops[:1]); !errors.Is(err, ErrConflict) {
		t.Fatalf("other drops = %v", err)
	}
}

func TestSettleWhoseFindingProposalIsAnotherFindingIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	mustGrouping(t, st, a)
	if _, _, err := st.WriteSettle(context.Background(), "job", af[0], af[1], "finding"); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
	if r := records(t, st); len(r.Settles) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestSettleProposalBeforeTheFindingIsRefused(t *testing.T) {
	st, put := recordStore(t)
	early := put("propose")
	turn, finding := put("turn"), put("finding")
	mustSet(t, st, turn, finding)
	if _, _, err := st.WriteSettle(context.Background(), "job", finding, early, "propose"); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestSettleOfADroppedFindingIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	mustGrouping(t, st, a, Drop{Finding: af[0], Kept: af[1]})
	if _, _, err := st.WriteSettle(context.Background(), "job", af[0], af[0], "finding"); !errors.Is(err, ErrNotKept) {
		t.Fatalf("err = %v", err)
	}
	mustSettle(t, st, af[1], af[1], "finding")
}

func TestSettleForASetWithNoGroupingIsRefused(t *testing.T) {
	st, put := recordStore(t)
	_, af, _, _ := twoSets(t, st, put)
	if _, _, err := st.WriteSettle(context.Background(), "job", af[0], af[0], "finding"); !errors.Is(err, ErrNotKept) {
		t.Fatalf("err = %v", err)
	}
	if r := records(t, st); len(r.Settles) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestSettleOfAFindingInNoSetIsRefused(t *testing.T) {
	st, put := recordStore(t)
	finding := put("finding")
	if _, _, err := st.WriteSettle(context.Background(), "job", finding, finding, "finding"); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestSettleReplayComparesProposalAndKindAndKeepsTheOriginalAfter(t *testing.T) {
	st, put := recordStore(t)
	_, _, _, bf := twoSets(t, st, put)
	propose, other := put("propose"), put("propose")
	first := mustSettle(t, st, bf, propose, "propose")
	if first.After != other || first.ProposalKind != "propose" {
		t.Fatalf("settle = %+v, want after %d", first, other)
	}
	put("say")
	again, already, err := st.WriteSettle(context.Background(), "job", bf, propose, "propose")
	if err != nil || !already || again != first {
		t.Fatalf("replay = %+v %v %v", again, already, err)
	}
	for _, tc := range []struct {
		proposal int64
		kind     string
	}{{other, "propose"}, {propose, "dismiss"}, {bf, "finding"}} {
		if _, _, err := st.WriteSettle(context.Background(), "job", bf, tc.proposal, tc.kind); !errors.Is(err, ErrConflict) {
			t.Fatalf("%d %s: %v", tc.proposal, tc.kind, err)
		}
	}
}

func TestFixCarriesSettledAfterFromTheSettle(t *testing.T) {
	st, put := recordStore(t)
	_, _, _, bf := twoSets(t, st, put)
	settle := mustSettle(t, st, bf, bf, "finding")
	put("say")
	fixed := put("fixed")
	fix, already, err := st.WriteFix(context.Background(), "job", bf, fixed)
	if err != nil || already || fix.SettledAfter != settle.After || fix.Fixed != fixed {
		t.Fatalf("fix = %+v %v %v", fix, already, err)
	}
	again, already, err := st.WriteFix(context.Background(), "job", bf, fixed)
	if err != nil || !already || again != fix {
		t.Fatalf("replay = %+v %v %v", again, already, err)
	}
	if _, _, err := st.WriteFix(context.Background(), "job", bf, put("fixed")); !errors.Is(err, ErrConflict) {
		t.Fatalf("other fixed = %v", err)
	}
	if r := records(t, st); len(r.Fixes) != 1 || r.Fixes[0] != fix {
		t.Fatalf("records = %+v", r)
	}
}

func TestFixOfADismissSettleIsRefused(t *testing.T) {
	st, put := recordStore(t)
	_, _, _, bf := twoSets(t, st, put)
	mustSettle(t, st, bf, put("dismiss"), "dismiss")
	if _, _, err := st.WriteFix(context.Background(), "job", bf, put("fixed")); !errors.Is(err, ErrFixDismissed) {
		t.Fatalf("err = %v", err)
	}
	if r := records(t, st); len(r.Fixes) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestFixWhoseFixedIsNotAfterTheSettleIsRefused(t *testing.T) {
	st, put := recordStore(t)
	_, _, _, bf := twoSets(t, st, put)
	early := put("fixed")
	mustSettle(t, st, bf, bf, "finding")
	if _, _, err := st.WriteFix(context.Background(), "job", bf, early); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestFixWithoutASettleIsRefused(t *testing.T) {
	st, put := recordStore(t)
	_, _, _, bf := twoSets(t, st, put)
	if _, _, err := st.WriteFix(context.Background(), "job", bf, put("fixed")); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestFixedAlreadyUsedByAnotherFixIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, bf := twoSets(t, st, put)
	mustGrouping(t, st, a)
	mustSettle(t, st, af[0], af[0], "finding")
	mustSettle(t, st, bf, bf, "finding")
	fixed := put("fixed")
	if _, _, err := st.WriteFix(context.Background(), "job", af[0], fixed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.WriteFix(context.Background(), "job", bf, fixed); !errors.Is(err, ErrRecordReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestSecondHaltWithADifferentFindingConflicts(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, bf := twoSets(t, st, put)
	mustGrouping(t, st, a)
	halt, already, err := st.WriteHalt(context.Background(), "job", bf)
	if err != nil || already || halt.Finding != bf {
		t.Fatalf("halt = %+v %v %v", halt, already, err)
	}
	put("say")
	again, already, err := st.WriteHalt(context.Background(), "job", bf)
	if err != nil || !already || again != halt {
		t.Fatalf("replay = %+v %v %v", again, already, err)
	}
	if _, _, err := st.WriteHalt(context.Background(), "job", af[0]); !errors.Is(err, ErrConflict) {
		t.Fatalf("second halt = %v", err)
	}
}

func TestHaltOfAFindingThatIsNotKeptIsRefused(t *testing.T) {
	st, put := recordStore(t)
	a, af, _, _ := twoSets(t, st, put)
	if _, _, err := st.WriteHalt(context.Background(), "job", af[0]); !errors.Is(err, ErrNotKept) {
		t.Fatalf("ungrouped: %v", err)
	}
	mustGrouping(t, st, a, Drop{Finding: af[0], Kept: af[1]})
	if _, _, err := st.WriteHalt(context.Background(), "job", af[0]); !errors.Is(err, ErrNotKept) {
		t.Fatalf("dropped: %v", err)
	}
	if r := records(t, st); len(r.Halts) != 0 {
		t.Fatalf("records = %+v", r)
	}
}

func TestRecordsListsAreEmptyNotNilAndInKeyOrder(t *testing.T) {
	st, put := recordStore(t)
	r := records(t, st)
	if r.Sets == nil || r.Groupings == nil || r.Settles == nil || r.Fixes == nil || r.Halts == nil {
		t.Fatalf("nil list: %+v", r)
	}
	t1, f1 := put("turn"), put("finding")
	t2, f2 := put("turn"), put("finding")
	mustSet(t, st, t2, f2)
	mustSet(t, st, t1, f1)
	mustSettle(t, st, f2, f2, "finding")
	mustSettle(t, st, f1, f1, "finding")
	r = records(t, st)
	if r.Sets[0].Turn != t1 || r.Groupings[0].Turn != t1 || r.Settles[0].Finding != f1 {
		t.Fatalf("records not in key order: %+v", r)
	}
}

// everyWrite writes one record of each kind to conv and returns each write's error by record name.
func everyWrite(st *Store, conv string) map[string]error {
	ctx := context.Background()
	_, _, _, setErr := st.WriteSet(ctx, conv, 1, []int64{2})
	_, _, groupingErr := st.WriteGrouping(ctx, conv, 1, nil)
	_, _, settleErr := st.WriteSettle(ctx, conv, 2, 2, "finding")
	_, _, fixErr := st.WriteFix(ctx, conv, 2, 3)
	_, _, haltErr := st.WriteHalt(ctx, conv, 2)
	return map[string]error{"set": setErr, "grouping": groupingErr, "settle": settleErr, "fix": fixErr, "halt": haltErr}
}

func TestRecordsOfAMissingConversationIsNotFound(t *testing.T) {
	st, _ := recordStore(t)
	if _, err := st.Records(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("records = %v", err)
	}
	for record, err := range everyWrite(st, "nope") {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s = %v", record, err)
		}
	}
}

func TestRecordWriteToAConversationWithNoMessagesIsAReferenceFailure(t *testing.T) {
	st, _ := recordStore(t)
	for record, err := range everyWrite(st, "job") {
		if !errors.Is(err, ErrRecordReference) {
			t.Fatalf("%s = %v", record, err)
		}
	}
	assertNoRecords(t, st)
}

func TestOpenRejectsVersion1WithoutARecordTable(t *testing.T) {
	for _, table := range recordTables {
		t.Run(table, func(t *testing.T) {
			dir := writeVersion1(t, replyMessagesHead+`, UNIQUE (conversation, seq), UNIQUE (conversation, seq, kind)) STRICT`)
			db := openRaw(t, dir)
			if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, FileName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrSchema) {
				t.Fatalf("open = %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("database file changed")
			}
		})
	}
}
