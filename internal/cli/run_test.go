// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
	for _, part := range []string{"500", "8h", "HOTSEAT_AGENTS", "interactive", "agy", "does not launch"} {
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
