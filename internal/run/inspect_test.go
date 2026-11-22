// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func (h *harness) status(conv string) Status {
	h.t.Helper()
	st, err := ReadStatus(context.Background(), h.addr, "", conv)
	if err != nil {
		h.t.Fatalf("status %s: %v", conv, err)
	}
	return st
}

func TestStatusStopResultEqualsTheStopResultRunPrinted(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(h *harness) Config
		outcome string
	}{
		{
			name: "done",
			setup: func(h *harness) Config {
				bravoFinds(h, abcRoster...)
				finishes(h)
				return h.team("alpha", abcRoster...)
			},
			outcome: outcomeDone,
		},
		{
			name: "deadlock",
			setup: func(h *harness) Config {
				disputed(h)
				h.script("a3",
					on(promptStall, says(kindDeadlock, "a1", "a2", "a3", "owner")),
					stepFile{On: promptReport, Publish: []messageFile{{Kind: kindDone, To: []string{"owner"}, Body: "report"}}},
				)
				cfg := h.team("a3", "a1", "a2", "a3")
				cfg.MaxSays, cfg.MaxSaysSet = 1, true
				return cfg
			},
			outcome: outcomeDeadlock,
		},
		{
			name: "unsettled",
			setup: func(h *harness) Config {
				earlyDone(h)
				return h.team("alpha", "alpha", "bravo")
			},
			outcome: outcomeUnsettled,
		},
		{
			name: "unfixed",
			setup: func(h *harness) Config {
				h.review("alpha", "bravo")
				h.script("bravo", on(promptTurn, says(kindFinding, "alpha", "bravo"), says(kindSay, "alpha")), on(promptFix))
				h.script("alpha", on(promptDiscussion, says(kindAgree, "alpha", "bravo")), on(promptFix, says(kindDone, "owner")))
				return h.team("alpha", "alpha", "bravo")
			},
			outcome: outcomeUnsettled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			res, name := h.play(tc.setup(h))
			want := res.out.stop()
			if want == nil || res.out.outcome() != tc.outcome || want.Outcome != tc.outcome {
				t.Fatalf("run %v\n%s", res.err, res.out)
			}
			st := h.status(name)
			if !reflect.DeepEqual(st.Stop, want) {
				t.Fatalf("status %+v, run %+v", st.Stop, want)
			}
			stops := h.recordsOf(name).stops
			if len(stops) != 1 || st.Stop.Done != stops[0].Done {
				t.Fatalf("done %d, stop records %+v", st.Stop.Done, stops)
			}
			if st.State != StateClosed || st.StopError != nil || st.Discussing != nil {
				t.Fatalf("state %s stop error %+v discussing %v", st.State, st.StopError, st.Discussing)
			}
			if st.Findings-st.Settled != len(st.Stop.Unsettled) {
				t.Fatalf("findings %d settled %d unsettled %v", st.Findings, st.Settled, st.Stop.Unsettled)
			}
		})
	}
}

func TestStatusReportsTheRecordedDoneAfterTheBackupThatPostedItTimesOut(t *testing.T) {
	h := newHarness(t)
	h.keptConversation("alpha", "bravo")
	h.setMember("kept", "alpha", statusLaunched, nil)
	h.setMember("kept", "alpha", statusTimedOut, nil)
	h.setMember("kept", "bravo", statusLaunched, nil)
	h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"})
	h.setMember("kept", "bravo", statusExited, nil)
	done := h.publish("kept", "bravo", kindDone, "yes", []string{"owner"})
	out, _, err := h.runBounded(h.continueKept(""))
	if err != nil || out.outcome() != "done" {
		t.Fatalf("err %v\n%s", err, out)
	}
	if res := h.setMember("kept", "bravo", statusTimedOut, nil); res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	st := h.status("kept")
	if st.Stop == nil || st.Stop.Done != done.Seq || st.Stop.Outcome != outcomeDone {
		t.Fatalf("stop %+v, want done %d", st.Stop, done.Seq)
	}
	want := []MemberStatus{{Name: "alpha", Status: statusTimedOut}, {Name: "bravo", Status: statusTimedOut}}
	if !slices.Equal(st.Members, want) {
		t.Fatalf("members %+v", st.Members)
	}
}

func TestStatusIsRunningWhileTheLeaseIsHeldThenIdleWithTheStopErrorOfTheRun(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	cfg.MaxTurns, cfg.MaxTurnsSet = 1, true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && len(launches(h.calls(), "bravo")) < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	st := h.status(name)
	if st.State != StateRunning || st.Since == "" || st.Expires == "" || st.Stop != nil || st.StopError != nil {
		t.Fatalf("running %+v", st)
	}
	h.publish(name, "alpha", kindTurn, "nope", nil)
	h.publish(name, "alpha", kindTurn, "again", nil)
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), classTurnCap) {
		t.Fatal(err)
	}
	stops := stopErrors(h.messages(name))
	if len(stops) != 1 {
		t.Fatalf("stop errors %+v", stops)
	}
	deadline = time.Now().Add(5 * time.Second)
	for st = h.status(name); st.State == StateRunning && time.Now().Before(deadline); st = h.status(name) {
		time.Sleep(50 * time.Millisecond)
	}
	if st.State != StateIdle || st.Since != "" || st.Expires != "" || st.Stop != nil {
		t.Fatalf("after the stop %+v", st)
	}
	if st.StopError == nil || *st.StopError != (StopError{Class: stopProtocol, Seq: stops[0].Seq}) {
		t.Fatalf("stop error %+v, want %s at %d", st.StopError, stopProtocol, stops[0].Seq)
	}
}

func TestStatusIsClosedOnlyWhenNoConductorHoldsTheLease(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	if st := h.status("kept"); st.State != StateIdle || st.Binding.Name != "kept" || st.Binding.Seat != "alpha" || !slices.Equal(st.Binding.Roster, abcRoster) {
		t.Fatalf("open %+v", st)
	}
	if res := h.do("close", client.CloseRequest{Conversation: "kept"}); res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	if st := h.status("kept"); st.State != StateClosed || st.Stop != nil {
		t.Fatalf("closed %+v", st)
	}
	h.engineLease("kept")
	if st := h.status("kept"); st.State != StateRunning || st.Since == "" || st.Expires == "" {
		t.Fatalf("closed and held %+v", st)
	}
}

func TestStatusCountsFindingsFromTheWholeLedgerAndNamesTheOneBeingWorkedOn(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
	f1 := h.publish("kept", "bravo", kindFinding, "one", abcRoster)
	f2 := h.publish("kept", "charlie", kindFinding, "two", abcRoster)
	dup := h.publish("kept", "charlie", kindFinding, "one again", abcRoster)
	h.publish("kept", "bravo", kindSay, "found", []string{"alpha"})
	h.publish("kept", "charlie", kindSay, "found", []string{"alpha"})
	h.closed(turn, f1, f2, dup)
	h.record("kept", client.GroupingRecord{Conversation: "kept", Turn: turn.Seq, Drops: []client.Drop{{Finding: dup.Seq, Kept: f1.Seq}}})
	h.engineLease("kept")

	type counts struct{ findings, settled, fixed, dropped int }
	check := func(step string, want counts, discussing int64) {
		t.Helper()
		st := h.status("kept")
		got := counts{st.Findings, st.Settled, st.Fixed, st.Dropped}
		var at int64
		if st.Discussing != nil {
			at = *st.Discussing
		}
		if got != want || at != discussing {
			t.Fatalf("%s: counts %+v discussing %d, want %+v discussing %d", step, got, at, want, discussing)
		}
	}
	check("grouped", counts{2, 0, 0, 1}, f1.Seq)
	h.settledOn(f1)
	check("settled change awaiting its fixed", counts{2, 1, 0, 1}, f1.Seq)
	fixed := h.publishRe("kept", "bravo", kindFixed, kindFixed, abcRoster, f1.Seq)
	check("fixed", counts{2, 1, 1, 1}, f2.Seq)
	if st := h.status("kept"); st.Newest != fixed.Seq {
		t.Fatalf("newest %d, want %d", st.Newest, fixed.Seq)
	}
	h.record("kept", client.HaltRecord{Conversation: "kept", Finding: f2.Seq})
	check("halted", counts{2, 1, 1, 1}, 0)
}

func TestStatusNamesNoFindingAfterAHaltOnAnEarlierOne(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo", "charlie"})
	f1 := h.publish("kept", "bravo", kindFinding, "one", abcRoster)
	f2 := h.publish("kept", "charlie", kindFinding, "two", abcRoster)
	h.publish("kept", "bravo", kindSay, "found", []string{"alpha"})
	h.publish("kept", "charlie", kindSay, "found", []string{"alpha"})
	h.closed(turn, f1, f2)
	h.record("kept", client.GroupingRecord{Conversation: "kept", Turn: turn.Seq})
	h.engineLease("kept")
	if st := h.status("kept"); st.Discussing == nil || *st.Discussing != f1.Seq {
		t.Fatalf("before the halt: discussing %+v, want %d", st, f1.Seq)
	}
	h.record("kept", client.HaltRecord{Conversation: "kept", Finding: f1.Seq})
	if st := h.status("kept"); st.Discussing != nil {
		t.Fatalf("after the halt: discussing %d, want none", *st.Discussing)
	}
}

func TestStatusCountsADismissalAsSettledAndNeverFixed(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	turn := h.publish("kept", "alpha", kindTurn, "mode: parallel\nreview", []string{"bravo"})
	f := h.publish("kept", "bravo", kindFinding, "one", abcRoster)
	h.publish("kept", "bravo", kindSay, "found", []string{"alpha"})
	h.closed(turn, f)
	h.record("kept", client.GroupingRecord{Conversation: "kept", Turn: turn.Seq})
	dismiss := h.publishRe("kept", "charlie", kindDismiss, kindDismiss, abcRoster, f.Seq)
	h.record("kept", client.SettleRecord{Conversation: "kept", Finding: f.Seq, Proposal: dismiss.Seq, ProposalKind: kindDismiss})
	h.publishRe("kept", "bravo", kindFixed, kindFixed, abcRoster, f.Seq)
	h.engineLease("kept")
	st := h.status("kept")
	if st.Findings != 1 || st.Settled != 1 || st.Fixed != 0 || st.Discussing != nil {
		t.Fatalf("%+v", st)
	}
}

func TestStatusShowsOnlyAStopErrorNewerThanTheRecordedDone(t *testing.T) {
	h := newHarness(t)
	h.keptConversation(abcRoster...)
	stopError := func(class string) client.Message {
		t.Helper()
		res := h.do("publish", client.PublishRequest{Conversation: "kept", From: fromRun, To: []string{fromOwner}, Body: class, TxID: class, Kind: kindError})
		if res.Outcome != bus.OutcomeOK || res.Message == nil {
			t.Fatal(res.Reason)
		}
		return *res.Message
	}
	older := stopError(stopBus)
	if st := h.status("kept"); st.StopError == nil || *st.StopError != (StopError{Class: stopBus, Seq: older.Seq}) {
		t.Fatalf("no stop record: %+v", st.StopError)
	}
	done := h.publish("kept", "alpha", kindDone, "done", []string{"owner"})
	h.record("kept", client.StopRecord{Conversation: "kept", Done: done.Seq})
	st := h.status("kept")
	if st.StopError != nil || st.Stop == nil || st.Stop.Done != done.Seq || st.Stop.Outcome != outcomeDone {
		t.Fatalf("older than the done: stop %+v stop error %+v", st.Stop, st.StopError)
	}
	h.publish("kept", "alpha", kindError, stopSignal, []string{"owner"})
	newer := stopError(stopSignal)
	if st := h.status("kept"); st.StopError == nil || *st.StopError != (StopError{Class: stopSignal, Seq: newer.Seq}) || st.Newest != newer.Seq {
		t.Fatalf("newer than the done: %+v newest %d", st.StopError, st.Newest)
	}
}

func TestStatusOfAMissingConversationIsTheRefusalOfItsFirstRead(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var paths []string
	addr := h.proxy(func(_ http.ResponseWriter, path string, _ []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, path)
		return false
	})
	_, err := ReadStatus(context.Background(), addr, "", "missing")
	res, ok := FailedCall(err)
	if !ok || res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonNotFound {
		t.Fatalf("err %v result %+v", err, res)
	}
	mu.Lock()
	defer mu.Unlock()
	if slices.Compare(paths, []string{bus.PathLease}) != 0 {
		t.Fatalf("paths %v", paths)
	}
}

func TestStatusOfAnUnreachableBusIsAConnectionFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadStatus(context.Background(), addr, "", "kept")
	if res, ok := FailedCall(err); !ok || res.Outcome != client.OutcomeConnectionFailure {
		t.Fatalf("err %v result %+v", err, res)
	}
	if _, ok := FailedCall(errors.New("other")); ok {
		t.Fatal("a plain error is a failed call")
	}
}

func TestStatusMakesOnlyReadCallsLeaseGetFirstAndRecordsBeforeTheTranscript(t *testing.T) {
	h := newHarness(t)
	bravoFinds(h, abcRoster...)
	finishes(h)
	_, name := h.play(h.team("alpha", abcRoster...))
	var mu sync.Mutex
	var paths []string
	var leaseBodies [][]byte
	addr := h.proxy(func(_ http.ResponseWriter, path string, body []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, path)
		if path == bus.PathLease {
			leaseBodies = append(leaseBodies, body)
		}
		return false
	})
	if _, err := ReadStatus(context.Background(), addr, "", name); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{bus.PathLease, bus.PathList, bus.PathMembers, bus.PathRecords, bus.PathRead}
	if got := slices.Compact(slices.Clone(paths)); !slices.Equal(got, want) {
		t.Fatalf("paths %v, want %v with read repeated per page", paths, want)
	}
	if len(leaseBodies) != 1 || !bytes.Contains(leaseBodies[0], []byte(`"lease":"`+bus.LeaseGet+`"`)) {
		t.Fatalf("lease calls %q", leaseBodies)
	}
}
