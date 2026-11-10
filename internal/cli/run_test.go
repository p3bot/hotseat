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
	for _, part := range []string{"500", "8h", "HOTSEAT_AGENTS", "interactive", "agy", "does not launch", "300", "HOTSEAT_MAX_LAUNCHES", "min_members", "below minimum", "launch cap", "HOTSEAT_MAX_SAYS", "outcome: deadlock", "outcome: unsettled", "unfixed"} {
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
