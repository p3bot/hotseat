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
	"time"
)

// procStartTicks is USER_HZ. /proc starttime uses that unit, and Linux fixes it at 100.
const procStartTicks = 100

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

// processStart is when pid was created. ok is false when the time cannot be read.
func processStart(pid int) (time.Time, bool) {
	fields, ok := procStatFields(pid)
	if !ok {
		return time.Time{}, false
	}
	// Fields after comm start at stat field 3.
	idx := startTimeField - 3
	if idx < 0 || idx >= len(fields) {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[idx], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	var btime int64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		btime, err = strconv.ParseInt(strings.TrimPrefix(line, "btime "), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		found = true
		break
	}
	if !found {
		return time.Time{}, false
	}
	elapsed := time.Duration(ticks) * time.Second / procStartTicks
	return time.Unix(btime, 0).Add(elapsed), true
}
