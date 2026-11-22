// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// runExternal runs bin and returns its stdout and stderr.
// On failure both streams are returned in the error, so the caller prints a
// match list once. --quiet on start keeps a successful stdout line free of notices.
func runExternal(ctx context.Context, bin, dir string, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, nil, commandFailure(bin, out.String(), errOut.String(), err)
	}
	return out.Bytes(), errOut.Bytes(), nil
}

// commandFailure is a failed start or tk call: its stderr and stdout, or the exec error when both are empty.
func commandFailure(bin, stdout, stderr string, err error) error {
	var parts []string
	if text := strings.TrimSpace(stderr); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stdout); text != "" {
		parts = append(parts, text)
	}
	text := bin + ": " + err.Error()
	if len(parts) > 0 {
		text = bin + ": " + strings.Join(parts, "\n")
	}
	return &commandError{text: text, err: err}
}
