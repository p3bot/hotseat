// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/run"
)

func TestStatusOfAMissingChatPrintsTheBusRefusalAndExitsTwo(t *testing.T) {
	addr, stop := startBus(t)
	t.Cleanup(stop)
	res := runBinCmd(t, buildBinary(t), "chat", "status", "missing", "--address", addr)
	want := "outcome: refused\nreason: " + bus.ReasonNotFound + "\n"
	if res.code != ExitRefused || res.stderr != "" || res.stdout != want {
		t.Fatalf("code %d stderr %q\n%s", res.code, res.stderr, res.stdout)
	}
}

func TestStatusOfAnUnreachableBusPrintsConnectionFailureAndExitsFour(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runCommand(t, "chat", "status", "job", "--address", addr)
	res, perr := parseResult([]byte(stdout))
	if code, ok := ReportedExit(err); !ok || code != ExitUnavailable || stderr != "" || perr != nil || res.Outcome != client.OutcomeConnectionFailure {
		t.Fatalf("err %v stderr %q\n%s", err, stderr, stdout)
	}
}

func TestStatusUsageErrorPrintsNoStdout(t *testing.T) {
	stdout, stderr, err := runCommand(t, "chat", "status")
	if _, ok := ReportedExit(err); err == nil || ok || stdout != "" || stderr != "" {
		t.Fatalf("err %v stderr %q stdout %q", err, stderr, stdout)
	}
}

func TestStatusOfAnIdleChatExitsZeroWithItsBindingCountsAndNewestStopError(t *testing.T) {
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	createRoster(t, ctx, addr, "job", "alpha")
	stopped := sendAs(t, ctx, addr, "job", "run", "error", []string{"owner"}, "stopped by bus", "s1")
	stdout, stderr, err := runCommand(t, "chat", "status", "job", "--address", addr)
	seq := strconv.FormatInt(stopped.Seq, 10)
	want := "outcome: ok\nchat: job\nseat: alpha\nroster: alpha\nstate: idle\nnewest: " + seq +
		"\nfindings: 0\nsettled: 0\nfixed: 0\ndropped: 0\nstop: stopped by bus\nstop_seq: " + seq + "\n"
	if err != nil || stderr != "" || stdout != want {
		t.Fatalf("err %v stderr %q\n%s", err, stderr, stdout)
	}
}

func TestStatusLinesFollowTheDocumentedOrder(t *testing.T) {
	discussing := int64(41)
	st := run.Status{
		Binding: client.Chat{
			Name: "job", Status: "open", Task: "tasks:hotseat/ticket/triage", Ticket: "p3bot-e734",
			Seat: "grok", Directory: "/tmp/x", Roster: []client.RosterEntry{{Name: "grok"}, {Name: "opus"}},
		},
		State:      run.StateRunning,
		Since:      "2026-11-18T08:47:13Z",
		Expires:    "2026-11-18T09:05:41Z",
		Members:    []run.MemberStatus{{Name: "grok", Status: "running"}, {Name: "opus", Status: "exited"}},
		Newest:     57,
		Findings:   4,
		Settled:    2,
		Fixed:      1,
		Dropped:    1,
		Discussing: &discussing,
		Stop:       &run.StopResult{Done: 50, Unsettled: []int64{41, 44}, Unfixed: []int64{30}, Outcome: "unsettled"},
		StopError:  &run.StopError{Class: "stopped by signal", Seq: 56},
	}
	var b bytes.Buffer
	if err := writeStatus(&b, st); err != nil {
		t.Fatal(err)
	}
	want := `outcome: ok
chat: job
task: tasks:hotseat/ticket/triage
ticket: p3bot-e734
seat: grok
roster: grok
roster: opus
state: running
since: 2026-11-18T08:47:13Z
expires: 2026-11-18T09:05:41Z
member: grok
member_status: running
member: opus
member_status: exited
newest: 57
findings: 4
settled: 2
fixed: 1
dropped: 1
discussing: 41
done: 50
unsettled: 41
unsettled: 44
unfixed: 30
run_outcome: unsettled
stop: stopped by signal
stop_seq: 56
`
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}
