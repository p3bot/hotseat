// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build darwin

package run

import (
	"fmt"

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

// startToken identifies pid's process by its kinfo_proc start time.
// ok is false when the time cannot be read, as for a pid that is gone.
func startToken(pid int) (string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", false
	}
	tv := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), true
}
