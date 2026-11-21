// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !linux && !darwin

package run

// zombie reports whether pid is a zombie. This platform has no status read.
func zombie(int) bool {
	return false
}

// startToken identifies pid's process. This platform has no token, so no process is adopted.
func startToken(int) (string, bool) {
	return "", false
}
