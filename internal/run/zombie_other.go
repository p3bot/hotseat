// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !linux && !darwin

package run

import "time"

// zombie reports whether pid is a zombie. This platform has no status read.
func zombie(int) bool {
	return false
}

// processStart is when pid was created. This platform has no start-time read.
func processStart(int) (time.Time, bool) {
	return time.Time{}, false
}
