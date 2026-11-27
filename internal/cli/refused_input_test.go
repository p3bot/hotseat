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

func TestOldRootCommandsAreRefused(t *testing.T) {
	for _, args := range [][]string{
		{"create", "--name", "job"},
		{"list"},
		{"help"},
		{"publish", "--conversation", "job", "--from", "a", "--kind", "say", "--body", "hi"},
		{"member", "register", "--conversation", "job", "--name", "alpha"},
		{"read", "--conversation", "job"},
		{"attend", "--conversation", "job"},
	} {
		var stdout, stderr bytes.Buffer
		err := execute(context.Background(), args, &stdout, &stderr)
		if _, reported := ReportedExit(err); err == nil || reported || stdout.Len() != 0 {
			t.Fatalf("%v err %v stdout %q stderr %q", args, err, stdout.String(), stderr.String())
		}
	}
}

func TestChatIgnoresTheOldConversationVariable(t *testing.T) {
	t.Setenv("HOTSEAT_CONVERSATION", "other")
	t.Setenv("HOTSEAT_ADDRESS", "")
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	create(t, ctx, addr, "job")
	create(t, ctx, addr, "other")
	sendAs(t, ctx, addr, "job", "alpha", "done", []string{"owner"}, "from-job", "j1")
	sendAs(t, ctx, addr, "other", "alpha", "done", []string{"owner"}, "from-other", "o1")
	out, stderr, err := runCommand(t, "chat", "attend", "job", "--address", addr)
	if err != nil || stderr != "" || !strings.Contains(out, "from-job") || strings.Contains(out, "from-other") {
		t.Fatalf("err %v %s\n%s", err, stderr, out)
	}
}

func TestOldRunInputIsAUsageErrorOnBothVerbs(t *testing.T) {
	for _, args := range [][]string{
		{"run", "review/ticket"},
		{"run", "--conversation", "job"},
		{"run", "new", "review/ticket", "--conversation", "job"},
		{"run", "new", "review/ticket", "--interactive"},
		{"run", "continue", "job", "--conversation", "job"},
		{"run", "continue", "job", "--interactive"},
	} {
		var stdout, stderr bytes.Buffer
		err := execute(context.Background(), args, &stdout, &stderr)
		if _, reported := ReportedExit(err); err == nil || reported || stdout.Len() != 0 {
			t.Fatalf("%v err %v stdout %q stderr %q", args, err, stdout.String(), stderr.String())
		}
	}
}
