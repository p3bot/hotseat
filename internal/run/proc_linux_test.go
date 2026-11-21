// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build linux

package run

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestStartTokenIsTheBootIDAndRawStartTicks(t *testing.T) {
	boot, err := os.ReadFile(bootIDPath)
	if err != nil {
		t.Skip(err)
	}
	token, ok := startToken(os.Getpid())
	if !ok {
		t.Fatal("no token for this process")
	}
	id, ticks, found := strings.Cut(token, "/")
	if !found || id != strings.TrimSpace(string(boot)) {
		t.Fatalf("token %q", token)
	}
	if _, err := strconv.ParseUint(ticks, 10, 64); err != nil {
		t.Fatalf("ticks %q: %v", ticks, err)
	}
	if again, _ := startToken(os.Getpid()); again != token {
		t.Fatalf("token changed from %q to %q", token, again)
	}
}

func TestStartTokenOfAPidNoProcessCanHaveIsNotRead(t *testing.T) {
	raw, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Skip(err)
	}
	// Linux assigns pids below pid_max, so pid_max itself never names a process.
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := startToken(pid); ok {
		t.Fatalf("token %q for pid %d", token, pid)
	}
	if id := launchedProc(pid); id.pid != pid || id.start != "" {
		t.Fatalf("launched %+v", id)
	}
}
