// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

func (e *engine) taskID(query string) (string, error) {
	out, err := e.tool(e.startBin, "--quiet", "get", query, "--id")
	if err != nil {
		return "", err
	}
	id := strings.TrimRight(out, "\n")
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return "", fmt.Errorf("task id must be one line")
	}
	return id, nil
}

func (e *engine) moduleText(id string) (string, error) {
	out, err := e.tool(e.startBin, "--quiet", "get", id)
	if err != nil {
		return "", err
	}
	return out, nil
}

func (e *engine) checkPermissions() error {
	seen := make(map[string]struct{})
	for _, a := range e.roster {
		if _, ok := seen[a.module]; ok {
			continue
		}
		seen[a.module] = struct{}{}
		_, err := e.tool(e.startBin, "--quiet", "get", a.module, "--field", "permission."+e.perm)
		if err != nil {
			if commandDidNotRun(err) {
				return err
			}
			return fmt.Errorf("%s does not define permission %s", a.module, e.perm)
		}
	}
	return nil
}

// commandDidNotRun reports a failure to exec. A program that ran and exited is false.
func commandDidNotRun(err error) bool {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false
	}
	var ee *exec.Error
	if errors.As(err, &ee) {
		return true
	}
	var pe *fs.PathError
	return errors.As(err, &pe)
}

func (e *engine) locator(module string) ([]string, error) {
	out, err := e.tool(e.startBin, "--quiet", "get", module, "--field", "session_locator")
	if err != nil {
		return nil, err
	}
	return locatorKeys(out)
}

func (e *engine) launchDirectory(ticket string) (string, error) {
	if ticket == "" {
		return filepath.Abs(e.cfg.Dir)
	}
	scope, err := splitTicket(ticket)
	if err != nil {
		return "", err
	}
	out, err := e.tool(e.tkBin, "scope", "list")
	if err != nil {
		return "", err
	}
	roots, err := scopeRoots(out)
	if err != nil {
		return "", err
	}
	root, ok := roots[scope]
	if !ok || root == "" {
		return "", fmt.Errorf("scope %s has no code-root", scope)
	}
	if filepath.IsAbs(root) {
		return root, nil
	}
	return filepath.Abs(root)
}

func (e *engine) tool(bin string, args ...string) (string, error) {
	return runExternal(e.root, bin, e.cfg.Dir, e.stderr, nil, args...)
}
