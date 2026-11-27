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

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/run"
)

func TestRunNewWithoutTaskIsAUsageErrorThatWritesNothing(t *testing.T) {
	for _, args := range [][]string{{"run", "new"}, {"run", "continue"}, {"run"}} {
		var stdout, stderr bytes.Buffer
		err := executeIO(context.Background(), args, nil, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 {
			t.Fatalf("%v err %v stdout %q stderr %q", args, err, stdout.String(), stderr.String())
		}
	}
}

func TestRunNewAgentsFlagBeatsItsEnv(t *testing.T) {
	t.Setenv("HOTSEAT_AGENTS", "alpha=hotseat/grok:m")
	var stdout, stderr bytes.Buffer
	err := executeIO(context.Background(), []string{"run", "new", "review/ticket", "--agents", "Alpha=hotseat/grok:m"}, nil, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
}

func TestRunNewSetupFlagsBeatTheirEnvAndTheEnvFillsAnAbsentFlag(t *testing.T) {
	field := map[string]func(run.Setup) string{
		"agents":       func(s run.Setup) string { return s.Agents },
		"seat":         func(s run.Setup) string { return s.Seat },
		"ticket":       func(s run.Setup) string { return s.Ticket },
		"prompts":      func(s run.Setup) string { return s.Prompts },
		"instructions": func(s run.Setup) string { return s.Instructions },
		"permission":   func(s run.Setup) string { return s.Permission },
	}
	for flag, get := range field {
		env := "HOTSEAT_" + strings.ToUpper(flag)
		t.Run(flag, func(t *testing.T) {
			t.Setenv(env, "from-env")
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{"--" + flag, "from-flag"}, "from-flag"},
				{nil, "from-env"},
			} {
				var f setupFlags
				cmd := &cobra.Command{}
				f.add(cmd)
				if err := cmd.ParseFlags(tc.args); err != nil {
					t.Fatal(err)
				}
				s, err := f.setup(cmd, "review/ticket")
				if err != nil {
					t.Fatal(err)
				}
				if got := get(s); got != tc.want || s.Task != "review/ticket" {
					t.Fatalf("%v: %s = %q task %q, want %q", tc.args, flag, got, s.Task, tc.want)
				}
			}
		})
	}
}

func TestRunNewRefusesAnEmptySeatOrPermissionFlagAndDefaultsAnEmptyVariable(t *testing.T) {
	for _, flag := range []string{"seat", "permission"} {
		env := "HOTSEAT_" + strings.ToUpper(flag)
		t.Run(flag, func(t *testing.T) {
			t.Setenv(env, "from-env")
			var stdout, stderr bytes.Buffer
			err := executeIO(context.Background(), []string{"run", "new", "review/ticket", "--" + flag, ""}, nil, &stdout, &stderr)
			if err == nil || err.Error() != "--"+flag+" is empty" || stdout.Len() != 0 {
				t.Fatalf("err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
			}
			t.Setenv("HOTSEAT_SEAT", "")
			t.Setenv("HOTSEAT_PERMISSION", "")
			var f setupFlags
			cmd := &cobra.Command{}
			f.add(cmd)
			s, err := f.setup(cmd, "review/ticket")
			if err != nil || s.Seat != "" || s.Permission != "" {
				t.Fatalf("empty variable: setup %+v err %v", s, err)
			}
		})
	}
}

func TestRunContinueRefusesEverySetupFlag(t *testing.T) {
	for _, flag := range []string{"--agents", "--seat", "--ticket", "--prompts", "--instructions", "--permission"} {
		var stdout, stderr bytes.Buffer
		err := executeIO(context.Background(), []string{"run", "continue", "job", flag, "x"}, nil, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%s err %v stdout %q", flag, err, stdout.String())
		}
	}
}

func TestRunNewAcceptsATimeoutOfZero(t *testing.T) {
	cfg, err := runFlags{}.config(runVerb(t, "new", "--timeout", "0"))
	if err != nil || !cfg.TimeoutSet || cfg.Timeout != 0 {
		t.Fatalf("timeout %v set %v err %v", cfg.Timeout, cfg.TimeoutSet, err)
	}
	t.Setenv("HOTSEAT_TIMEOUT", "0")
	cfg, err = runFlags{}.config(runVerb(t, "continue"))
	if err != nil || !cfg.TimeoutSet || cfg.Timeout != 0 {
		t.Fatalf("env timeout %v set %v err %v", cfg.Timeout, cfg.TimeoutSet, err)
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
			args := append([]string{"run", "new", "review/ticket", "--seat", "Bad"}, tc.args...)
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
	cfg, err := runFlags{maxLaunches: run.DefaultMaxLaunches}.config(runVerb(t, "new"))
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
			args := append([]string{"run", "new", "review/ticket", "--seat", "Bad"}, tc.args...)
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
	cfg, err := runFlags{maxSays: run.DefaultMaxSays}.config(runVerb(t, "continue"))
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
	err := runExit(fmt.Errorf("stop: %w", run.ErrUnresolved))
	code, ok := ReportedExit(err)
	if !ok || code != ExitFailure || err.Error() != "" {
		t.Fatalf("unresolved %v", err)
	}
	other := errors.New(run.DefaultPermission)
	err = runExit(other)
	_, ok = ReportedExit(err)
	if err != other || ok {
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
	cfg, err := runFlags{}.config(runVerb(t, "continue", "--address", "nohostport"))
	if err != nil || cfg.Address != "nohostport" || runPage(cfg.Address) != "" {
		t.Fatalf("cfg address %q err %v", cfg.Address, err)
	}
}

func TestRunReportPrintsTheHeaderWithMembersTicketAndPage(t *testing.T) {
	var buf bytes.Buffer
	r := &runReport{lw: lineWriter{w: &buf}, page: "127.0.0.1:4728"}
	r.Header(run.Header{
		Chat: "p3bot-e734-ticket-triage", Task: "tasks:hotseat/ticket/triage", Ticket: "p3bot-e734", Seat: "grok",
		Members: []run.Member{{Name: "grok", Module: "hotseat/grok", Model: "grok-4"}, {Name: "opus", Module: "hotseat/claude", Model: "opus"}},
	})
	want := `chat: p3bot-e734-ticket-triage
task: tasks:hotseat/ticket/triage
ticket: p3bot-e734
seat: grok
member: grok=hotseat/grok:grok-4
member: opus=hotseat/claude:opus
page: http://127.0.0.1:4728/chats/p3bot-e734-ticket-triage
`
	if buf.String() != want || r.lw.err != nil {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestRunReportLeavesOutAnEmptyTicketAndANonLoopbackPage(t *testing.T) {
	var buf bytes.Buffer
	r := &runReport{lw: lineWriter{w: &buf}}
	r.Header(run.Header{Chat: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha", Members: []run.Member{{Name: "alpha", Module: "hotseat/grok", Model: "g1"}}})
	want := "chat: kept\ntask: tasks:hotseat/review/ticket\nseat: alpha\nmember: alpha=hotseat/grok:g1\n"
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

// runVerb is the run verb name under its run group, with flags parsed, so the group's persistent flags resolve.
func runVerb(t *testing.T, name string, flags ...string) *cobra.Command {
	t.Helper()
	for _, verb := range newRunCmd().Commands() {
		if verb.Name() == name {
			if err := verb.ParseFlags(flags); err != nil {
				t.Fatal(err)
			}
			return verb
		}
	}
	t.Fatalf("no run verb %s", name)
	return nil
}

func TestEmptyTokenFileIsRequiredBeforeTheChatIsBound(t *testing.T) {
	bin := buildBinary(t)
	addr := "127.0.0.1:1"
	for _, args := range [][]string{
		{"run", "new", "review/ticket", "--address", addr, "--token-file", ""},
		{"run", "continue", "job", "--address", addr, "--token-file", ""},
	} {
		got := runBinCmd(t, bin, args...)
		if got.code != ExitFailure || got.stdout != "" || !strings.Contains(got.stderr, "token file is required") || strings.Contains(got.stderr, "is a directory") {
			t.Fatalf("%v code %d\n%s%s", args, got.code, got.stdout, got.stderr)
		}
	}
	chat := runBinCmd(t, bin, "chat", "status", "job", "--address", addr, "--token-file", "")
	if chat.code == ExitOK || !strings.Contains(chat.stderr, "token file is required") || strings.Contains(chat.stderr, "is a directory") {
		t.Fatalf("chat code %d\n%s%s", chat.code, chat.stdout, chat.stderr)
	}
	for _, env := range [][]string{nil, replaceEnv("HOTSEAT_TOKEN_FILE=")} {
		got := runBinEnv(t, bin, env, "run", "new", "review/ticket", "--address", addr)
		if strings.Contains(got.stderr, "token file is required") || strings.Contains(got.stderr, "is a directory") {
			t.Fatalf("env %v\n%s%s", env, got.stdout, got.stderr)
		}
	}
}

func TestRunContinueRefusesAMissingChatAndAChatCreateChatBeforeTheLease(t *testing.T) {
	addr, stop := startBus(t)
	t.Cleanup(stop)
	bin := buildBinary(t)
	missing := runBinCmd(t, bin, "run", "continue", "missing", "--address", addr)
	if missing.code != ExitFailure || missing.stdout != "" || !strings.Contains(missing.stderr, "chat missing does not exist") {
		t.Fatalf("code %d\n%s%s", missing.code, missing.stdout, missing.stderr)
	}
	if st := runBinCmd(t, bin, "chat", "status", "missing", "--address", addr); st.code != ExitRefused {
		t.Fatalf("status code %d\n%s", st.code, st.stdout)
	}
	if res := runBinCmd(t, bin, "chat", "create", "job", "--address", addr); res.code != ExitOK {
		t.Fatalf("create %d\n%s%s", res.code, res.stdout, res.stderr)
	}
	job := runBinCmd(t, bin, "run", "continue", "job", "--address", addr)
	if job.code != ExitFailure || job.stdout != "" || !strings.Contains(job.stderr, "chat job has an incomplete setup: no task; no seat; no roster; no permission") {
		t.Fatalf("code %d\n%s%s", job.code, job.stdout, job.stderr)
	}
	st := runBinCmd(t, bin, "chat", "status", "job", "--address", addr)
	if st.code != ExitOK || !strings.Contains(st.stdout, "state: idle\n") || !strings.Contains(st.stdout, "newest: 0\n") {
		t.Fatalf("status %d\n%s", st.code, st.stdout)
	}
}
