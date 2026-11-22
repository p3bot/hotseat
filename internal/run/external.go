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
	"strconv"
	"strings"
)

func (e *engine) taskID(query string) (string, error) {
	out, err := e.tool(toolStart, "--quiet", "get", query, "--id")
	if err != nil {
		return "", err
	}
	id := strings.TrimRight(out, "\n")
	if id == "" || strings.ContainsAny(id, "\r\n") {
		return "", unusable(errors.New("task id must be one line"))
	}
	return id, nil
}

func (e *engine) moduleText(id string) (string, error) {
	out, err := e.tool(toolStart, "--quiet", "get", id)
	if err != nil {
		return "", err
	}
	return out, nil
}

// minMembers is the task's min_members. A task that omits it prints nothing, which is 1.
func (e *engine) minMembers(id string) (int, error) {
	out, err := e.tool(toolStart, "--quiet", "get", id, "--field", "min_members")
	if err != nil {
		return 0, err
	}
	text := strings.TrimSuffix(out, "\n")
	if text == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(text)
	if !allDigits(text) || err != nil || n < 1 {
		return 0, unusable(fmt.Errorf("%s min_members %q is not a positive integer", id, text))
	}
	return n, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (e *engine) checkPermissions() error {
	seen := make(map[string]struct{})
	for _, a := range e.roster {
		if _, ok := seen[a.module]; ok {
			continue
		}
		seen[a.module] = struct{}{}
		_, err := e.tool(toolStart, "--quiet", "get", a.module, "--field", "permission."+e.perm)
		if err != nil {
			if commandDidNotRun(err) {
				return err
			}
			return &commandError{text: fmt.Sprintf("%s does not define permission %s", a.module, e.perm), err: err}
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
	out, err := e.tool(toolStart, "--quiet", "get", module, "--field", "session_locator")
	if err != nil {
		return nil, err
	}
	keys, err := locatorKeys(out)
	if err != nil {
		return nil, unusable(err)
	}
	return keys, nil
}

func (e *engine) launchDirectory(ticket string) (string, error) {
	if ticket == "" {
		return filepath.Abs(e.cfg.Dir)
	}
	scope, err := splitTicket(ticket)
	if err != nil {
		return "", err
	}
	out, err := e.tool(toolTk, "scope", "list")
	if err != nil {
		return "", err
	}
	roots, err := scopeRoots(out)
	if err != nil {
		return "", unusable(err)
	}
	root, ok := roots[scope]
	if !ok || root == "" {
		return "", unusable(fmt.Errorf("scope %s has no code-root", scope))
	}
	if filepath.IsAbs(root) {
		return root, nil
	}
	return filepath.Abs(root)
}

// Tools run shells out to. Each names the prefix of its forwarded stderr lines.
const (
	toolStart = "start"
	toolTk    = "tk"
)

// tool runs start or tk and returns its stdout. Under --verbose a successful call's stderr is forwarded, each line
// prefixed with the tool name. A failed call's output stays in the error.
func (e *engine) tool(name string, args ...string) (string, error) {
	bin := e.startBin
	if name == toolTk {
		bin = e.tkBin
	}
	stdout, stderr, err := runExternal(e.root, bin, e.cfg.Dir, args...)
	if err != nil {
		return "", err
	}
	if e.cfg.Verbose {
		if err := prefixLines(e.stderr, name, stderr); err != nil {
			return "", err
		}
	}
	return string(stdout), nil
}
