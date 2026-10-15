// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// runExternal runs bin and returns stdout.
// On success, stderr is written to w. On failure both streams are returned
// in the error, so the caller prints a match list once. --quiet on start
// keeps a successful stdout line free of notices.
func runExternal(ctx context.Context, bin, dir string, w io.Writer, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", commandFailure(bin, stdout.String(), stderr.String(), err)
	}
	if w != nil && stderr.Len() > 0 {
		if _, werr := w.Write(stderr.Bytes()); werr != nil {
			return "", werr
		}
	}
	return stdout.String(), nil
}

func commandFailure(bin, stdout, stderr string, err error) error {
	var parts []string
	if text := strings.TrimSpace(stderr); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stdout); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return fmt.Errorf("%s: %w", bin, err)
	}
	return fmt.Errorf("%s: %s", bin, strings.Join(parts, "\n"))
}
