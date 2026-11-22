// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/run"
)

func TestRunWithoutTaskWritesNothing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := executeIO(context.Background(), []string{"run"}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "a task is required") || stdout.Len() != 0 {
		t.Fatalf("err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
}

func TestRunHelpDescribesRails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := executeIO(context.Background(), []string{"run", "--help"}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, part := range []string{"500", "8h", "HOTSEAT_AGENTS", "interactive", "agy", "does not launch", "300", "HOTSEAT_MAX_LAUNCHES", "min_members", "below minimum", "launch cap", "HOTSEAT_MAX_SAYS", "outcome: deadlock", "outcome: unsettled", "unfixed", "--verbose", "HOTSEAT_VERBOSE", "elapsed rounded to seconds", "bus port plus one"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %s\n%s", part, text)
		}
	}
	if strings.Contains(text, "Gemini") {
		t.Fatal("help names Gemini")
	}
}

func TestRunFlagBeatsAgentEnv(t *testing.T) {
	t.Setenv("HOTSEAT_AGENTS", "alpha=hotseat/grok:m")
	var stdout, stderr bytes.Buffer
	err := executeIO(context.Background(), []string{"run", "review/ticket", "--agents", "Alpha=hotseat/grok:m"}, nil, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
}

func TestMaxLaunchesFlagBeatsItsEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
		want string
	}{
		{"env alone", "0", nil, "max launches"},
		{"flag over env", "0", []string{"--max-launches", "5"}, "lowercase"},
		{"flag below one", "5", []string{"--max-launches", "0"}, "max launches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOTSEAT_MAX_LAUNCHES", tc.env)
			args := append([]string{"run", "review/ticket", "--seat", "Bad"}, tc.args...)
			var stdout, stderr bytes.Buffer
			err := executeIO(context.Background(), args, nil, &stdout, &stderr)
			if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v stdout %q", err, stdout.String())
			}
		})
	}
}

func TestMaxLaunchesEnvSetsTheCapWhenTheFlagIsAbsent(t *testing.T) {
	t.Setenv("HOTSEAT_MAX_LAUNCHES", "7")
	cfg, err := runFlags{maxLaunches: run.DefaultMaxLaunches}.config(newRunCmd(), []string{"review/ticket"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MaxLaunchesSet || cfg.MaxLaunches != 7 {
		t.Fatalf("max launches %d set %v", cfg.MaxLaunches, cfg.MaxLaunchesSet)
	}
}

func TestMaxSaysFlagBeatsItsEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
		want string
	}{
		{"env alone", "0", nil, "max says"},
		{"flag over env", "0", []string{"--max-says", "5"}, "lowercase"},
		{"flag below one", "5", []string{"--max-says", "0"}, "max says"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOTSEAT_MAX_SAYS", tc.env)
			args := append([]string{"run", "review/ticket", "--seat", "Bad"}, tc.args...)
			var stdout, stderr bytes.Buffer
			err := executeIO(context.Background(), args, nil, &stdout, &stderr)
			if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v stdout %q", err, stdout.String())
			}
		})
	}
}

func TestMaxSaysEnvSetsTheBudgetWhenTheFlagIsAbsent(t *testing.T) {
	t.Setenv("HOTSEAT_MAX_SAYS", "7")
	cfg, err := runFlags{maxSays: run.DefaultMaxSays}.config(newRunCmd(), []string{"review/ticket"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MaxSaysSet || cfg.MaxSays != 7 {
		t.Fatalf("max says %d set %v", cfg.MaxSays, cfg.MaxSaysSet)
	}
	if run.DefaultMaxSays != 4 {
		t.Fatalf("default %d", run.DefaultMaxSays)
	}
}

func TestUnresolvedRunExitsOneWithNoStderrLine(t *testing.T) {
	if err := runExit(fmt.Errorf("stop: %w", run.ErrUnresolved)); !ReportedFailure(err) || err.Error() != "" {
		t.Fatalf("unresolved %v", err)
	}
	other := errors.New(run.DefaultPermission)
	if err := runExit(other); err != other || ReportedFailure(err) {
		t.Fatalf("other %v", err)
	}
	if err := runExit(nil); err != nil {
		t.Fatalf("nil %v", err)
	}
}

func TestRunPageIsTheBusPortPlusOneOnlyForALoopbackBus(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want string
	}{
		{"127.0.0.1:4727", "127.0.0.1:4728"},
		{"[::1]:5000", "127.0.0.1:5001"},
		{"LocalHost:4727", "127.0.0.1:4728"},
		{"192.0.2.10:4727", ""},
		{"0.0.0.0:4727", ""},
		{":4727", ""},
		{"bus.invalid:4727", ""},
		{"127.0.0.1", ""},
		{"127.0.0.1:65535", ""},
	} {
		if got := runPage(tc.addr); got != tc.want {
			t.Fatalf("runPage(%s) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestRunConfigLeavesAMalformedAddressForTheBusCall(t *testing.T) {
	cmd := newRunCmd()
	if err := cmd.ParseFlags([]string{"--address", "nohostport"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := runFlags{address: "nohostport"}.config(cmd, []string{"review/ticket"})
	if err != nil || cfg.Address != "nohostport" || runPage(cfg.Address) != "" {
		t.Fatalf("cfg address %q err %v", cfg.Address, err)
	}
}

func TestRunReportPrintsTheHeaderWithMembersTicketAndPage(t *testing.T) {
	var buf bytes.Buffer
	r := &runReport{lw: lineWriter{w: &buf}, page: "127.0.0.1:4728"}
	r.Header(run.Header{
		Conversation: "p3bot-e734-ticket-triage", Task: "tasks:hotseat/ticket/triage", Ticket: "p3bot-e734", Seat: "grok",
		Members: []run.Member{{Name: "grok", Module: "hotseat/grok", Model: "grok-4"}, {Name: "opus", Module: "hotseat/claude", Model: "opus"}},
	})
	want := `conversation: p3bot-e734-ticket-triage
task: tasks:hotseat/ticket/triage
ticket: p3bot-e734
seat: grok
member: grok=hotseat/grok:grok-4
member: opus=hotseat/claude:opus
page: http://127.0.0.1:4728/conversations/p3bot-e734-ticket-triage
`
	if buf.String() != want || r.lw.err != nil {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestRunReportLeavesOutAnEmptyTicketAndANonLoopbackPage(t *testing.T) {
	var buf bytes.Buffer
	r := &runReport{lw: lineWriter{w: &buf}}
	r.Header(run.Header{Conversation: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha", Roster: []string{"alpha", "bravo"}})
	want := "conversation: kept\ntask: tasks:hotseat/review/ticket\nseat: alpha\nroster: alpha\nroster: bravo\n"
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestRunReportPrintsTheResultBlockInOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  run.Result
		want string
	}{
		{"done", run.Result{Stop: &run.StopResult{Done: 57, Outcome: "done"}, Outcome: "done", Elapsed: 18*time.Minute + 42*time.Second},
			"done: 57\noutcome: done\nelapsed: 18m42s\n"},
		{"unsettled", run.Result{Stop: &run.StopResult{Done: 50, Unsettled: []int64{41, 44}, Unfixed: []int64{30}, Outcome: "unsettled"}, Outcome: "unsettled", Elapsed: time.Second},
			"done: 50\nunsettled: 41\nunsettled: 44\nunfixed: 30\noutcome: unsettled\nelapsed: 1s\n"},
		{"halt after the close", run.Result{Stop: &run.StopResult{Done: 9, Outcome: "done"}, StopClass: "stopped by lease", Outcome: "error"},
			"done: 9\nstop: stopped by lease\noutcome: error\nelapsed: 0s\n"},
		{"error stop", run.Result{StopClass: "stopped by command", Outcome: "error", Elapsed: 3 * time.Second},
			"stop: stopped by command\noutcome: error\nelapsed: 3s\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &runReport{lw: lineWriter{w: &buf}}
			r.Result(tc.res)
			if buf.String() != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", buf.String(), tc.want)
			}
		})
	}
}

func TestRunReportUnheldIsOutcomeErrorAlone(t *testing.T) {
	var buf bytes.Buffer
	r := &runReport{lw: lineWriter{w: &buf}, page: "127.0.0.1:4728"}
	r.Unheld()
	if buf.String() != "outcome: error\n" {
		t.Fatalf("got %q", buf.String())
	}
}
