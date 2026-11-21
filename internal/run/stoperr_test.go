// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// stopErrors are the stored errors whose body is a stop class.
func stopErrors(msgs []client.Message) []client.Message {
	var out []client.Message
	for _, m := range ofKind(msgs, kindError) {
		if strings.HasPrefix(m.Body, "stopped by ") {
			out = append(out, m)
		}
	}
	return out
}

// wantStopError fails unless conv holds exactly one stop error, from run to owner, whose body is class and whose re is re, or none for zero.
func (h *harness) wantStopError(conv, class string, re int64) {
	h.t.Helper()
	got := stopErrors(h.messages(conv))
	if len(got) != 1 {
		h.t.Fatalf("stop errors %+v, want one %q", got, class)
	}
	m := got[0]
	if m.Body != class || m.From != fromRun || len(m.To) != 1 || m.To[0] != fromOwner {
		h.t.Fatalf("stop error %+v, want %q from run to owner", m, class)
	}
	switch {
	case re == 0 && m.Re != nil:
		h.t.Fatalf("stop error re %d, want none", *m.Re)
	case re != 0 && (m.Re == nil || *m.Re != re):
		h.t.Fatalf("stop error re %v, want %d", m.Re, re)
	}
}

// wantNoStopError fails when conv holds a stop error.
func (h *harness) wantNoStopError(conv string) {
	h.t.Helper()
	if got := stopErrors(h.messages(conv)); len(got) != 0 {
		h.t.Fatalf("stop errors %+v", got)
	}
}

// answerAll returns a bus address that answers every call on path with raw and status, and forwards the rest.
func (h *harness) answerAll(path string, status int, raw string) string {
	h.t.Helper()
	return h.proxy(func(w http.ResponseWriter, p string, _ []byte) bool {
		if p != path {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, raw)
		return true
	})
}

// handEngine is an engine on conv that has acquired its lease, built by hand so a test can feed noteHalt its causes.
func (h *harness) handEngine(conv string) (*engine, context.CancelFunc) {
	h.t.Helper()
	root, signal := context.WithCancel(context.Background())
	child, cancel := context.WithCancel(root)
	held, loseHeld := context.WithCancelCause(root)
	h.t.Cleanup(signal)
	e := &engine{
		root:     root,
		child:    child,
		cancel:   cancel,
		held:     held,
		loseHeld: loseHeld,
		addr:     h.addr,
		holder:   h.engineLease(conv),
		leased:   true,
		name:     conv,
		stderr:   io.Discard,
		out:      &statusWriter{w: io.Discard},
	}
	return e, signal
}

func TestRefusedMemberWriteStopsItsSiblingsAtOnceAndSaysStoppedByRefusalBeforeTheyAreReaped(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var order []string
	addr := h.proxy(func(w http.ResponseWriter, path string, body []byte) bool {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case path == bus.PathMember && bytes.Contains(body, []byte(`"name":"bravo"`)) && bytes.Contains(body, []byte(`"status":"`+statusRunning+`"`)):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"outcome":"refused","reason":"`+bus.ReasonMemberMissing+`"}`)
			return true
		case path == bus.PathPublish && bytes.Contains(body, []byte(stopRefusal)):
			order = append(order, "stop")
		case path == bus.PathMember && bytes.Contains(body, []byte(`"name":"alpha"`)) && bytes.Contains(body, []byte(`"status":"`+statusTimedOut+`"`)):
			order = append(order, "alpha reaped")
		}
		return false
	})
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Address = addr
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusExited)
	h.awaitStatus(name, "bravo", statusExited)
	h.hold("alpha")
	h.publish(name, "alpha", kindTurn, "mode: parallel\nreview", []string{"alpha", "bravo"})
	// alpha is held until the test ends, so only the run can end it.
	res := h.awaitRun(ch, 4*time.Second)
	if res.err == nil || !refusedFor(res.err, bus.ReasonMemberMissing) || !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	if got := h.memberStatus(name, "alpha"); got != statusTimedOut {
		t.Fatalf("alpha %s", got)
	}
	h.wantStopError(name, stopRefusal, 0)
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "stop" || order[1] != "alpha reaped" {
		t.Fatalf("order %v", order)
	}
}

func TestFailedOrUnusableStartCallAfterTheLeaseSaysStoppedByCommand(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
	}{
		{"call failed", func(h *harness) {
			h.plan.BodyExit = 1
			if res := h.do("create", client.CreateRequest{
				Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha", Directory: h.dir, Roster: []string{"alpha"},
			}); res.Outcome != bus.OutcomeOK {
				h.t.Fatal(res.Reason)
			}
		}},
		{"unusable output", func(h *harness) {
			h.plan.MinMembers = "two\n"
			h.keptConversation("alpha")
		}},
		{"instructions too large to publish", func(h *harness) {
			h.plan.Body = strings.Repeat("x", bus.DefaultMaxBody+1)
			if res := h.do("create", client.CreateRequest{
				Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha", Directory: h.dir, Roster: []string{"alpha"},
			}); res.Outcome != bus.OutcomeOK {
				h.t.Fatal(res.Reason)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			stdout, stderr, err := h.runBounded(h.continueKept(""))
			if err == nil || !strings.HasSuffix(stdout, "outcome: error\n") {
				t.Fatalf("%v\n%s\n%s", err, stdout, stderr)
			}
			h.wantStopError("kept", stopCommand, 0)
		})
	}
}

func TestUnavailableOrUnreachableBusDuringTheWatchSaysStoppedByBus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		raw    string
	}{
		{"unavailable", http.StatusOK, `{"outcome":"unavailable","reason":"store is unavailable"}`},
		{"connection failure", http.StatusBadGateway, "gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			cfg := h.base()
			cfg.TicketSet = false
			cfg.Address = h.answerAll(bus.PathWait, tc.status, tc.raw)
			stdout, stderr, err := h.runBounded(cfg)
			if err == nil || !strings.HasSuffix(stdout, "outcome: error\n") {
				t.Fatalf("%v\n%s\n%s", err, stdout, stderr)
			}
			h.wantStopError(h.only().Name, stopBus, 0)
		})
	}
}

func TestInconsistentBusAnswerSaysStoppedByRun(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Address = h.answerAll(bus.PathWait, http.StatusOK, `{"outcome":"ok"}`)
	stdout, stderr, err := h.runBounded(cfg)
	if err == nil || !strings.HasSuffix(stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s\n%s", err, stdout, stderr)
	}
	h.wantStopError(h.only().Name, stopRun, 0)
}

func TestSignalWhileMembersRunSaysStoppedBySignalNotACancelledCall(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	h.hold("bravo")
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitStatus(name, "alpha", statusLaunched)
	h.awaitStatus(name, "bravo", statusLaunched)
	cancel()
	res := h.awaitRun(ch, 8*time.Second)
	if res.err == nil || !strings.HasSuffix(res.stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s", res.err, res.stdout)
	}
	h.wantStopError(name, stopSignal, 0)
}

func TestLostLeaseSaysStoppedByLeaseOnceAndLaterCausesDoNotReplaceIt(t *testing.T) {
	h := newHarness(t)
	h.keptConversation("alpha")
	e, signal := h.handEngine("kept")
	_ = e.loseLease(bus.ReasonLeaseNotHeld)
	refused := &busError{op: "member alpha", outcome: bus.OutcomeRefused, reason: bus.ReasonLeaseWrite}
	_ = e.noteHalt(refused)
	_ = e.noteHalt(&busError{op: "record", outcome: bus.OutcomeRefused, reason: bus.ReasonRecordReference})
	signal()
	_ = e.noteHalt(context.Canceled)
	_ = e.fail(refused)
	h.wantStopError("kept", stopLease, 0)
}

func TestSignalBeforeAnyCauseSaysStoppedBySignalWhateverTheError(t *testing.T) {
	h := newHarness(t)
	h.keptConversation("alpha")
	e, signal := h.handEngine("kept")
	signal()
	_ = e.noteHalt(&busError{op: "member alpha", outcome: bus.OutcomeRefused, reason: bus.ReasonMemberMissing})
	h.wantStopError("kept", stopSignal, 0)
}

func TestLaunchCapWindDownThatAStoppingDoneClosesSaysNoStopAndExitsZero(t *testing.T) {
	h := newHarness(t)
	h.pace.grace = 300 * time.Millisecond
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	cfg.MaxLaunches = 2
	cfg.MaxLaunchesSet = true
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		h.hold(n)
	}
	cctx, cancel := ctx()
	defer cancel()
	ch := h.goRun(cctx, cfg)
	name := h.waitName()
	h.awaitBody(name, classLaunchCap)
	h.stillRunning(ch)
	h.publish(name, "alpha", kindDone, "finished", nil)
	res := h.awaitRun(ch, 8*time.Second)
	if res.err != nil || !strings.HasSuffix(res.stdout, "outcome: done\n") {
		t.Fatalf("%v\n%s\n%s", res.err, res.stdout, res.stderr)
	}
	h.wantNoStopError(name)
}

func TestLaunchCapHitByABootHandoffWithNothingInFlightNamesItsStoredErrorInTheStop(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {Exit: 1, Sleep: "500ms"},
		"bravo": {Register: true},
	}
	// A slow launch cap publish leaves the wind-down without its error for a while after nothing is in flight.
	addr := h.proxy(func(_ http.ResponseWriter, path string, body []byte) bool {
		if path == bus.PathPublish && bytes.Contains(body, []byte(`"body":"`+classLaunchCap+`"`)) {
			time.Sleep(300 * time.Millisecond)
		}
		return false
	})
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Address = addr
	cfg.MaxLaunches = 2
	cfg.MaxLaunchesSet = true
	stdout, stderr, err := h.runBounded(cfg)
	if err == nil || err.Error() != classLaunchCap || !strings.HasSuffix(stdout, "outcome: error\n") {
		t.Fatalf("%v\n%s\n%s", err, stdout, stderr)
	}
	name := h.only().Name
	msgs := h.messages(name)
	wantErrors(t, msgs, errLine("alpha", classProcessExit, 0), classLaunchCap, protocolStopLine(t, msgs, classLaunchCap))
	for _, m := range ofKind(msgs, kindError) {
		if m.Body == classLaunchCap {
			h.wantStopError(name, stopProtocol, m.Seq)
		}
	}
}
