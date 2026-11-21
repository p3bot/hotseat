// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"strings"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// badDones are the stored bad done errors of conv.
func (h *harness) badDones(conv string) []client.Message {
	h.t.Helper()
	var out []client.Message
	for _, m := range ofKind(h.messages(conv), kindError) {
		if strings.HasSuffix(m.Body, "\n"+classBadDone) {
			out = append(out, m)
		}
	}
	return out
}

func (h *harness) wantStop(conv string, done client.Message) {
	h.t.Helper()
	if stops := h.recordsOf(conv).stops; len(stops) != 1 || stops[0].Done != done.Seq {
		h.t.Fatalf("stops %+v, want done %d", stops, done.Seq)
	}
}

func TestBackupDoneThatStoppedTheRunStillStopsAContinuationAfterTheBackupTimesOut(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha":   {Exit: 1, Sleep: "1s"},
		"bravo":   {Register: true},
		"charlie": {Register: true},
	}
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	h.pace.grace = 300 * time.Millisecond
	h.hold("bravo")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitRegistered(name, "bravo")
	h.awaitRegistered(name, "charlie")
	h.awaitStatus(name, "alpha", statusTimedOut)
	done := h.publish(name, "bravo", kindDone, "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if got := h.memberStatus(name, "bravo"); got != statusTimedOut {
		t.Fatalf("bravo %s", got)
	}
	h.wantStop(name, done)
	launched := h.launchCount()

	cfg.Conversation = name
	cfg.ConversationSet = true
	cfg.TaskSet = false
	stdout, stderr, err := h.runBounded(cfg)
	if err != nil || !strings.HasSuffix(stdout, "outcome: done\n") {
		t.Fatalf("continuation %v\n%s\n%s", err, stdout, stderr)
	}
	if n := h.launchCount(); n != launched {
		t.Fatalf("continuation launched %d members", n-launched)
	}
	h.wantStop(name, done)
	if bad := h.badDones(name); len(bad) != 0 {
		t.Fatalf("bad dones %+v", bad)
	}
}

func TestBackupDoneWhileTheSeatIsAliveIsOneBadDoneAndNeverStopsTheRun(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {Exit: 1, Sleep: "1s"},
		"bravo": {Register: true},
	}
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitRegistered(name, "bravo")
	early := h.publish(name, "bravo", kindDone, "too soon", nil)
	deadline := time.Now().Add(8 * time.Second)
	for len(h.badDones(name)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	h.release("alpha")
	h.awaitStatus(name, "alpha", statusTimedOut)
	time.Sleep(300 * time.Millisecond)
	select {
	case res := <-ch:
		t.Fatalf("stopped on the ruled-out done: %v\n%s", res.err, res.stdout)
	default:
	}
	seat := h.publish(name, "alpha", kindDone, "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	bad := h.badDones(name)
	if len(bad) != 1 || bad[0].Body != errBody("bravo", classBadDone) || bad[0].Re == nil || *bad[0].Re != early.Seq ||
		bad[0].From != fromRun || bad[0].TxID != "bad done/"+seqOf(early) {
		t.Fatalf("bad dones %+v", bad)
	}
	if group := "error: bad done\nfrom: bravo\nseq: " + seqOf(early) + "\n"; strings.Count(res.stdout, group) != 1 {
		t.Fatalf("no single %q\n%s", group, res.stdout)
	}
	h.wantStop(name, seat)
}

// A continuation meets the ruled-out done with the seat timed-out and the sender the backup, which the rows alone would stop on.
func TestContinuationSkipsADoneAStoredBadDoneNamesAndStoresNoSecondError(t *testing.T) {
	h := newHarness(t)
	h.keptMembers("alpha", "bravo")
	h.setMember("kept", "alpha", statusTimedOut, nil)
	early := h.publish("kept", "bravo", kindDone, "too soon", nil)
	re := early.Seq
	h.conduct("kept", func(holder string) {
		res := h.do("publish", client.PublishRequest{
			Conversation: "kept", From: fromRun, To: []string{fromOwner}, Kind: kindError,
			Body: errBody("bravo", classBadDone), Re: &re, TxID: factTx(classBadDone, seqOf(early)), Holder: holder,
		})
		if res.Outcome != bus.OutcomeOK {
			t.Fatal(res.Reason)
		}
	})
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, h.continueKept(""))
	time.Sleep(300 * time.Millisecond)
	h.stillRunning(ch)
	seat := h.publish("kept", "alpha", kindDone, "finished", nil)
	res := h.awaitRun(ch, 10*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if bad := h.badDones("kept"); len(bad) != 1 {
		t.Fatalf("bad dones %+v", bad)
	}
	if strings.Contains(res.stdout, "error: bad done") {
		t.Fatalf("reported the stored ruling again\n%s", res.stdout)
	}
	h.wantStop("kept", seat)
}

func TestRuledOutDoneOnARetriedJudgementStoresNoSecondError(t *testing.T) {
	h := newHarness(t)
	e, stdout := h.closingEngine()
	early := h.publish("kept", "bravo", kindDone, "too soon", nil)
	for range 2 {
		e.ruledOut = make(map[int64]bool)
		stop, err := e.stops(early)
		if err != nil || stop {
			t.Fatalf("stop %v err %v", stop, err)
		}
	}
	if bad := h.badDones("kept"); len(bad) != 1 {
		t.Fatalf("bad dones %+v", bad)
	}
	if n := strings.Count(stdout.String(), "error: bad done\n"); n != 1 {
		t.Fatalf("error groups %d\n%s", n, stdout.String())
	}
}

// Every caller of the stop rule (watch, apply, resume, wind-down, report window) relies on this.
func TestRuledOutDoneStaysRuledOutWhenTheRowsWouldNowStopIt(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	early := h.publish("kept", "bravo", kindDone, "too soon", nil)
	if stop, err := e.stops(early); err != nil || stop {
		t.Fatalf("seat alive: stop %v err %v", stop, err)
	}
	h.setMember("kept", "bravo", statusLaunched, nil)
	h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"})
	h.setMember("kept", "bravo", statusExited, nil)
	e.mu.Lock()
	e.status["alpha"] = statusTimedOut
	e.mu.Unlock()
	if stop, err := e.stops(early); err != nil || stop {
		t.Fatalf("ruled-out done after the seat timed out: stop %v err %v", stop, err)
	}
	if stop, err := e.stops(h.publish("kept", "bravo", kindDone, "now", nil)); err != nil || !stop {
		t.Fatalf("a new backup done: stop %v err %v", stop, err)
	}
	if bad := h.badDones("kept"); len(bad) != 1 {
		t.Fatalf("bad dones %+v", bad)
	}
}

func TestStopRecordIsWrittenBeforeTheClose(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	done := h.publish("kept", "alpha", kindDone, "finished", nil)
	h.leaseAs("kept", bus.LeaseRelease, e.holder)
	if err := e.finishDone(done); err == nil || err == errDone {
		t.Fatalf("finishDone without the lease = %v", err)
	}
	if conv, _ := h.conv("kept"); conv.Status != "open" {
		t.Fatalf("closed with no stop record: %+v", conv)
	}
	if stops := h.recordsOf("kept").stops; len(stops) != 0 {
		t.Fatalf("stops %+v", stops)
	}
}

func TestDecidedStopKeepsItsVerdictAndSilencesLaterDones(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	h.setMember("kept", "bravo", statusLaunched, nil)
	h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"})
	h.setMember("kept", "bravo", statusExited, nil)
	setSeat := func(status string) {
		e.mu.Lock()
		e.status["alpha"] = status
		e.mu.Unlock()
	}
	setSeat(statusTimedOut)
	backup := h.publish("kept", "bravo", kindDone, "finished", nil)
	if stop, err := e.stops(backup); err != nil || !stop {
		t.Fatalf("backup done with the seat timed-out: stop %v err %v", stop, err)
	}
	setSeat(statusRunning)
	if stop, err := e.stops(backup); err != nil || !stop {
		t.Fatalf("decided done judged again on changed rows: stop %v err %v", stop, err)
	}
	if stop, err := e.stops(h.publish("kept", "bravo", kindDone, "late", nil)); err != nil || stop {
		t.Fatalf("late done: stop %v err %v", stop, err)
	}
	if bad := h.badDones("kept"); len(bad) != 0 {
		t.Fatalf("bad dones %+v", bad)
	}
}

func TestOnlyABadDoneExactlyAsRunStoresItRulesOutADone(t *testing.T) {
	re := func(seq int64) *int64 { return &seq }
	bad := func(seq int64, body, tx string) client.Message {
		return client.Message{Seq: seq, From: fromRun, To: []string{fromOwner}, Kind: kindError, Body: body, Re: re(5), TxID: tx}
	}
	tx := factTx(classBadDone, "5")
	msgs := []client.Message{
		{Seq: 4, From: "bravo", Kind: kindSay},
		{Seq: 5, From: "bravo", Kind: kindDone},
		bad(6, errBody("bravo", classBadDone), "x9"),
		bad(7, errBody("charlie", classBadDone), tx),
		{Seq: 8, From: fromRun, To: []string{fromOwner}, Kind: kindError, Body: errBody("bravo", classBadDone), Re: re(4), TxID: factTx(classBadDone, "4")},
	}
	if got := ruledOutDones(msgs); len(got) != 0 {
		t.Fatalf("rulings %v from errors run did not store", got)
	}
	msgs = append(msgs, bad(9, errBody("bravo", classBadDone), tx))
	if got := ruledOutDones(msgs); len(got) != 1 || got[0] != 5 {
		t.Fatalf("rulings %v, want [5]", got)
	}
}

func TestStoredBadDoneNeverRulesOutTheSeatsDone(t *testing.T) {
	h := newHarness(t)
	e, _ := h.closingEngine()
	seat := h.publish("kept", "alpha", kindDone, "finished", nil)
	e.noteRuledOut(seat.Seq)
	if stop, err := e.stops(seat); err != nil || !stop {
		t.Fatalf("seat done with a stored ruling: stop %v err %v", stop, err)
	}
	if bad := h.badDones("kept"); len(bad) != 0 {
		t.Fatalf("bad dones %+v", bad)
	}
}
