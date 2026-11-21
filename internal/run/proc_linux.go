// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build linux

package run

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// bootIDPath names this boot. starttime counts from boot, so a token needs both.
const bootIDPath = "/proc/sys/kernel/random/boot_id"

// startTimeField is the 1-based index of starttime in /proc/pid/stat.
const startTimeField = 22

func procStatFields(pid int) ([]string, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, false
	}
	text := string(stat)
	i := strings.LastIndex(text, ")")
	if i < 0 || i+2 >= len(text) {
		return nil, false
	}
	fields := strings.Fields(text[i+2:])
	if len(fields) == 0 {
		return nil, false
	}
	return fields, true
}

// zombie reports whether pid is a zombie. An unreadable status is not one.
func zombie(pid int) bool {
	fields, ok := procStatFields(pid)
	if !ok {
		return false
	}
	return fields[0] == "Z"
}

// startToken identifies pid's process: the boot id and the raw starttime ticks.
// ok is false when either cannot be read, as for a pid that is gone.
func startToken(pid int) (string, bool) {
	fields, ok := procStatFields(pid)
	if !ok {
		return "", false
	}
	// Fields after comm start at stat field 3.
	idx := startTimeField - 3
	if idx >= len(fields) {
		return "", false
	}
	if _, err := strconv.ParseUint(fields[idx], 10, 64); err != nil {
		return "", false
	}
	boot, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(boot))
	if id == "" {
		return "", false
	}
	return id + "/" + fields[idx], true
}
