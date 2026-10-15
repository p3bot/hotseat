// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build darwin

package run

import (
	"time"

	"golang.org/x/sys/unix"
)

// darwinZombie is SZOMB in Darwin sys/proc.h. x/sys does not export it.
const darwinZombie int8 = 5

// zombie reports whether pid is a zombie. An unreadable status is not one.
func zombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false
	}
	return kp.Proc.P_stat == darwinZombie
}

// processStart is when pid was created. ok is false when the time cannot be read.
func processStart(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}
