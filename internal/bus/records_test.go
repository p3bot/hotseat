// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/store"
)

// kindSender returns a function that sends one message of a kind to chat and returns its seq.
func kindSender(t *testing.T, base, chat string) func(kind string) int64 {
	t.Helper()
	n := 0
	return func(kind string) int64 {
		t.Helper()
		n++
		res := mustPost(t, base, PathSend, map[string]any{
			"chat": chat, "from": "alice", "to": []string{}, "body": kind,
			"txid": fmt.Sprint(n), "kind": kind,
		})
		if res.Outcome != OutcomeOK || res.Message == nil {
			t.Fatalf("send %s: %+v", kind, res)
		}
		return res.Message.Seq
	}
}

func record(chat, kind string, fields map[string]any) map[string]any {
	body := map[string]any{"chat": chat, "record": kind, "holder": testHolder}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

func mustRecord(t *testing.T, base string, body map[string]any) wire {
	t.Helper()
	res := mustPost(t, base, PathRecord, body)
	if res.Outcome != OutcomeOK {
		t.Fatalf("record %v: %+v", body, res)
	}
	return res
}

func assertRefused(t *testing.T, res wire, reason string) {
	t.Helper()
	if res.Outcome != OutcomeRefused || res.Reason != reason {
		t.Fatalf("got %s %q, want refused %q", res.Outcome, res.Reason, reason)
	}
	if res.Set != nil || res.Grouping != nil || res.Settle != nil || res.Fix != nil || res.Halt != nil || res.Stop != nil {
		t.Fatalf("refusal carries a record: %+v", res)
	}
}

func readRecords(t *testing.T, base, chat string) (wire, []byte) {
	t.Helper()
	res, raw := mustRaw(t, base, PathRecords, map[string]any{"chat": chat})
	if res.Outcome != OutcomeOK {
		t.Fatalf("records: %s", raw)
	}
	return res, raw
}

func TestRecordsForAChatWithNoRecordsReturnsSixEmptyArrays(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	_, raw := readRecords(t, base, "job")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sets", "groupings", "settles", "fixes", "halts", "stops"} {
		if string(got[key]) != "[]" {
			t.Fatalf("%s = %s in %s", key, got[key], raw)
		}
	}
}

func TestRecordsOfAMissingChatIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	assertRefused(t, mustPost(t, base, PathRecords, map[string]any{"chat": "nope"}), ReasonNotFound)
	assertRefused(t, mustPost(t, base, PathRecords, map[string]any{}), ReasonChatRequired)
	assertRefused(t, mustPost(t, base, PathRecord, record("nope", "halt", map[string]any{"finding": 1})), ReasonNotFound)
}

// A caller that appends each write result holds exactly what /records returns.
func TestRecordResultsAppendToWhatRecordsReturns(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, _, f2, f3 := put("finding"), put("say"), put("finding"), put("finding")
	high := put("error")

	var held wire
	set := mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f3, f1, f2}}))
	if set.Set == nil || !slices.Equal(set.Set.Findings, []int64{f1, f2, f3}) || set.Set.After != high || set.Grouping != nil {
		t.Fatalf("set = %+v", set)
	}
	held.Sets = &[]wireSet{*set.Set}
	grouping := mustRecord(t, base, record("job", "grouping", map[string]any{"turn": turn, "drops": []map[string]any{{"finding": f1, "kept": f2}}}))
	held.Groupings = &[]wireGrouping{*grouping.Grouping}
	solo, soloFinding := put("turn"), put("finding")
	single := mustRecord(t, base, record("job", "set", map[string]any{"turn": solo, "findings": []int64{soloFinding}}))
	if single.Grouping == nil || single.Grouping.Turn != solo || single.Grouping.Drops == nil || len(single.Grouping.Drops) != 0 {
		t.Fatalf("single-finding set = %+v", single)
	}
	*held.Sets = append(*held.Sets, *single.Set)
	*held.Groupings = append(*held.Groupings, *single.Grouping)
	propose := put("propose")
	settle := mustRecord(t, base, record("job", "settle", map[string]any{"finding": f2, "proposal": propose, "proposal_kind": "propose"}))
	held.Settles = &[]wireSettle{*settle.Settle}
	fixed := put("fixed")
	fix := mustRecord(t, base, record("job", "fix", map[string]any{"finding": f2, "fixed": fixed}))
	if fix.Fix.SettledAfter != settle.Settle.After {
		t.Fatalf("fix = %+v settle = %+v", fix.Fix, settle.Settle)
	}
	held.Fixes = &[]wireFix{*fix.Fix}
	halt := mustRecord(t, base, record("job", "halt", map[string]any{"finding": f3}))
	held.Halts = &[]wireHalt{*halt.Halt}
	done := put("done")
	stop := mustRecord(t, base, record("job", "stop", map[string]any{"done": done}))
	if stop.Stop == nil || stop.Stop.Done != done {
		t.Fatalf("stop = %+v", stop)
	}
	held.Stops = &[]wireStop{*stop.Stop}

	got, _ := readRecords(t, base, "job")
	want, _ := json.Marshal(held)
	have, _ := json.Marshal(wire{Sets: got.Sets, Groupings: got.Groupings, Settles: got.Settles, Fixes: got.Fixes, Halts: got.Halts, Stops: got.Stops})
	if string(want) != string(have) {
		t.Fatalf("records\n%s\nwant\n%s", have, want)
	}
}

func TestRecordReplayReturnsTheStoredRecordAndTheOriginalAfter(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, f2, f3 := put("finding"), put("finding"), put("finding")
	first := mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2, f3}}))
	if first.AlreadyStored == nil || *first.AlreadyStored {
		t.Fatalf("first = %+v", first)
	}
	put("say")
	again := mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f2, f3, f1}, "after": 1}))
	if again.AlreadyStored == nil || !*again.AlreadyStored || again.Set.After != first.Set.After {
		t.Fatalf("replay = %+v, want after %d", again.Set, first.Set.After)
	}
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1}})), ReasonKeyConflict)
}

func TestRecordIgnoresAfterSentByTheCaller(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	high := put("say")
	res := mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{}, "after": turn}))
	if res.Set.After != high {
		t.Fatalf("after = %d, want %d", res.Set.After, high)
	}
}

func TestRecordRefusesAMissingOrUnknownSelector(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	assertRefused(t, mustPost(t, base, PathRecord, map[string]any{"chat": "job"}), ReasonRecordRequired)
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "marker", nil)), ReasonRecordBad)
	assertRefused(t, mustPost(t, base, PathRecord, record("", "halt", map[string]any{"finding": 1})), ReasonChatRequired)
	assertRefused(t, mustPost(t, base, PathRecord, record("bad name", "halt", map[string]any{"finding": 1})), ReasonBadChat)
}

func TestRecordRefusesAMissingOrOutOfRangeField(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	cases := []struct {
		name   string
		record string
		fields map[string]any
		reason string
	}{
		{"set turn missing", "set", map[string]any{"findings": []int64{}}, ReasonTurnRequired},
		{"set turn zero", "set", map[string]any{"turn": 0, "findings": []int64{}}, ReasonTurnRange},
		{"set findings missing", "set", map[string]any{"turn": 1}, ReasonFindingsRequired},
		{"set findings null", "set", map[string]any{"turn": 1, "findings": nil}, ReasonFindingsShape},
		{"set findings not seqs", "set", map[string]any{"turn": 1, "findings": []any{"2"}}, ReasonFindingsShape},
		{"set finding zero", "set", map[string]any{"turn": 1, "findings": []int64{0}}, ReasonFindingsShape},
		{"grouping turn missing", "grouping", map[string]any{"drops": []any{}}, ReasonTurnRequired},
		{"grouping drops missing", "grouping", map[string]any{"turn": 1}, ReasonDropsRequired},
		{"grouping drops not objects", "grouping", map[string]any{"turn": 1, "drops": []int64{2}}, ReasonDropsShape},
		{"grouping drop without kept", "grouping", map[string]any{"turn": 1, "drops": []any{map[string]any{"finding": 2}}}, ReasonDropsShape},
		{"settle finding missing", "settle", map[string]any{"proposal": 2, "proposal_kind": "propose"}, ReasonFindingRequired},
		{"settle proposal missing", "settle", map[string]any{"finding": 2, "proposal_kind": "propose"}, ReasonProposalRequired},
		{"settle proposal float", "settle", map[string]any{"finding": 2, "proposal": 2.5, "proposal_kind": "propose"}, ReasonProposalRange},
		{"settle kind missing", "settle", map[string]any{"finding": 2, "proposal": 3}, ReasonProposalKindRequired},
		{"settle kind bad name", "settle", map[string]any{"finding": 2, "proposal": 3, "proposal_kind": "pro pose"}, ReasonProposalKindBad},
		{"fix finding missing", "fix", map[string]any{"fixed": 3}, ReasonFindingRequired},
		{"fix fixed missing", "fix", map[string]any{"finding": 2}, ReasonFixedRequired},
		{"fix fixed negative", "fix", map[string]any{"finding": 2, "fixed": -1}, ReasonFixedRange},
		{"halt finding missing", "halt", nil, ReasonFindingRequired},
		{"halt finding string", "halt", map[string]any{"finding": "2"}, ReasonFindingRange},
		{"stop done missing", "stop", nil, ReasonDoneRequired},
		{"stop done zero", "stop", map[string]any{"done": 0}, ReasonDoneRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, mustPost(t, base, PathRecord, record("job", tc.record, tc.fields)), tc.reason)
		})
	}
}

func TestSetWithARepeatedFindingIsRefusedBeforeTheStore(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, f2 := put("finding"), put("finding")
	repeat := record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2, f1}})
	assertRefused(t, mustPost(t, base, PathRecord, repeat), ReasonFindingsRepeat)
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2}}))
	assertRefused(t, mustPost(t, base, PathRecord, repeat), ReasonFindingsRepeat)
}

func TestGroupingWithARepeatedDroppedFindingIsRefusedBeforeTheStore(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, f2, f3 := put("finding"), put("finding"), put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2, f3}}))
	drops := []map[string]any{{"finding": f1, "kept": f2}, {"finding": f1, "kept": f3}}
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "grouping", map[string]any{"turn": turn, "drops": drops})), ReasonDropsRepeat)
	if got, _ := readRecords(t, base, "job"); len(*got.Groupings) != 0 {
		t.Fatalf("groupings = %+v", *got.Groupings)
	}
}

func TestRecordReferencingAMessageOfTheWrongKindIsRefusedWithTheReferenceReason(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn, say, finding := put("turn"), put("say"), put("finding")
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "set", map[string]any{"turn": say, "findings": []int64{finding}})), ReasonRecordReference)
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "set", map[string]any{"turn": turn, "findings": []int64{say}})), ReasonRecordReference)
	if got, _ := readRecords(t, base, "job"); len(*got.Sets) != 0 {
		t.Fatalf("sets = %+v", *got.Sets)
	}
}

func TestReusedFindingAndReusedFixedAreRefusedWithTheReferenceReasonNotUnavailable(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	t1, f1 := put("turn"), put("finding")
	t2, f2 := put("turn"), put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": t1, "findings": []int64{f1}}))
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "set", map[string]any{"turn": t2, "findings": []int64{f1}})), ReasonRecordReference)
	mustRecord(t, base, record("job", "set", map[string]any{"turn": t2, "findings": []int64{f2}}))
	mustRecord(t, base, record("job", "settle", map[string]any{"finding": f1, "proposal": f1, "proposal_kind": "finding"}))
	mustRecord(t, base, record("job", "settle", map[string]any{"finding": f2, "proposal": f2, "proposal_kind": "finding"}))
	fixed := put("fixed")
	mustRecord(t, base, record("job", "fix", map[string]any{"finding": f1, "fixed": fixed}))
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "fix", map[string]any{"finding": f2, "fixed": fixed})), ReasonRecordReference)
}

func TestCrossRowRulesAreRefusedWithTheirOwnReasons(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, f2, f3 := put("finding"), put("finding"), put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2, f3}}))
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "settle", map[string]any{"finding": f1, "proposal": f1, "proposal_kind": "finding"})), ReasonNotKept)
	chain := []map[string]any{{"finding": f1, "kept": f2}, {"finding": f2, "kept": f3}}
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "grouping", map[string]any{"turn": turn, "drops": chain})), ReasonKeptDropped)
	mustRecord(t, base, record("job", "grouping", map[string]any{"turn": turn, "drops": chain[:1]}))
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "halt", map[string]any{"finding": f1})), ReasonNotKept)
	dismiss := put("dismiss")
	mustRecord(t, base, record("job", "settle", map[string]any{"finding": f2, "proposal": dismiss, "proposal_kind": "dismiss"}))
	fixed := put("fixed")
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "fix", map[string]any{"finding": f2, "fixed": fixed})), ReasonFixDismissed)
}

func TestSecondHaltWithADifferentFindingIsAKeyConflict(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	f1, f2 := put("finding"), put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{f1, f2}}))
	mustRecord(t, base, record("job", "grouping", map[string]any{"turn": turn, "drops": []any{}}))
	mustRecord(t, base, record("job", "halt", map[string]any{"finding": f1}))
	res := mustPost(t, base, PathRecord, record("job", "halt", map[string]any{"finding": f2}))
	assertRefused(t, res, ReasonKeyConflict)
	if res.Reason != "transaction id reused with different content" {
		t.Fatalf("reason = %q", res.Reason)
	}
}

func TestSecondStopWithADifferentDoneIsAKeyConflictAndAReplayIsStored(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	first, second := put("done"), put("done")
	stored := mustRecord(t, base, record("job", "stop", map[string]any{"done": first}))
	if stored.AlreadyStored == nil || *stored.AlreadyStored {
		t.Fatalf("first = %+v", stored)
	}
	again := mustRecord(t, base, record("job", "stop", map[string]any{"done": first}))
	if again.AlreadyStored == nil || !*again.AlreadyStored || again.Stop.Done != first {
		t.Fatalf("replay = %+v", again)
	}
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "stop", map[string]any{"done": second})), ReasonKeyConflict)
}

func TestStopThatNamesNoDoneIsRefusedWithTheReferenceReason(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	say := put("say")
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "stop", map[string]any{"done": say})), ReasonRecordReference)
	assertRefused(t, mustPost(t, base, PathRecord, record("job", "stop", map[string]any{"done": say + 5})), ReasonRecordReference)
	if got, _ := readRecords(t, base, "job"); len(*got.Stops) != 0 {
		t.Fatalf("stops = %+v", *got.Stops)
	}
}

func TestRecordAndRecordsLeaveRevAndWakeNoWatch(t *testing.T) {
	opt, blocked := blockSignal()
	base, _, _, _ := startServer(t, opt)
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn, finding := put("turn"), put("finding")
	rev := currentRev(t, base)
	got := watchAsync(t, base, map[string]any{"cursor": finding, "limit": 10, "rev": rev, "deadline": "1s"})
	awaitBlock(t, blocked)
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{finding}}))
	noBlock(t, blocked)
	readRecords(t, base, "job")
	noBlock(t, blocked)
	if res := receive(t, got); res.Outcome != OutcomeTimeout {
		t.Fatalf("watch = %+v", res)
	}
	if now := currentRev(t, base); now != rev {
		t.Fatalf("rev = %d, want %d", now, rev)
	}
}

func TestRecordLogHoldsTheCallAndOutcomeAndNoRecordContent(t *testing.T) {
	var logs lockedLog
	base, _, _, _ := startServer(t, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	create(t, base, "job")
	conduct(t, base, "job")
	put := kindSender(t, base, "job")
	turn := put("turn")
	finding := put("finding")
	mustRecord(t, base, record("job", "set", map[string]any{"turn": turn, "findings": []int64{finding}}))
	readRecords(t, base, "job")
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "op": true, "chat": true, "outcome": true, "duration": true, "already_stored": true}
	var lines []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "op=record") {
			continue
		}
		lines = append(lines, line)
		if !strings.Contains(line, "chat=job") || !strings.Contains(line, "outcome=ok") {
			t.Fatalf("line lacks call or outcome: %s", line)
		}
		for _, field := range strings.Fields(line) {
			key, _, ok := strings.Cut(field, "=")
			if ok && !allowed[key] {
				t.Fatalf("line holds %s: %s", key, line)
			}
		}
	}
	if len(lines) != 2 {
		t.Fatalf("record log lines = %q", lines)
	}
}

func TestRecordRoutesFollowTheTokenRules(t *testing.T) {
	const token = "hstk-0a1b2c3d4e5f"
	base, _, _, _ := startServer(t, Options{Token: token})
	for _, path := range []string{PathRecord, PathRecords} {
		res, _ := authPost(t, base, path, "", []byte(`{"chat":"job"}`), []string{token})
		if res.Outcome != OutcomeRefused || res.Reason != ReasonTokenRequired {
			t.Fatalf("%s without token = %+v", path, res)
		}
		res, _ = authPost(t, base, path, "Bearer "+token, []byte(`{"chat":"job"}`), []string{token})
		if res.Outcome != OutcomeRefused || res.Reason == ReasonTokenRequired || res.Reason == ReasonTokenRejected {
			t.Fatalf("%s with token = %+v", path, res)
		}
	}
}

func TestWireRecordShapesUseEmptyArrays(t *testing.T) {
	var out wire
	recordWire("records", Result{Records: &store.Records{}}, &out)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"sets":[]`, `"groupings":[]`, `"settles":[]`, `"fixes":[]`, `"halts":[]`, `"stops":[]`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("%s lacks %s", raw, key)
		}
	}
	recordWire("record", Result{Set: &store.Set{Turn: 1}, Grouping: &store.Grouping{Turn: 1}}, &out)
	raw, err = json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"findings":[]`) || !strings.Contains(string(raw), `"drops":[]`) {
		t.Fatalf("nil lists not empty arrays: %s", raw)
	}
}
