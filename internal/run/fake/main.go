// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command fake stands in for start and tk during run tests.
// The first argument scope selects tk. Anything else is start.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type plan struct {
	ID       string          `json:"id"`
	IDExit   int             `json:"idExit"`
	IDStderr string          `json:"idStderr"`
	Body     string          `json:"body"`
	BodyExit int             `json:"bodyExit"`
	DenyPerm map[string]bool `json:"denyPerm"`
	Locator  map[string]text `json:"locator"`
	Launch   map[string]run  `json:"launch"`
	Scopes   string          `json:"scopes"`
	TkExit   int             `json:"tkExit"`
	TkStderr string          `json:"tkStderr"`
}

type text struct {
	Out    string `json:"out"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

type run struct {
	Exit     int    `json:"exit"`
	ExitOn   string `json:"exitOn"`
	Sleep    string `json:"sleep"`
	JSON     string `json:"json"`
	Stderr   string `json:"stderr"`
	Register bool   `json:"register"`
}

type line struct {
	Time  string   `json:"time"`
	Kind  string   `json:"kind"`
	Argv  []string `json:"argv"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

func main() {
	dir := os.Getenv("HOTSEAT_FAKE_DIR")
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "scope" {
		tk(dir, p, args)
		return
	}
	start(dir, p, args)
}

func tk(dir string, p plan, args []string) {
	wd, _ := os.Getwd()
	record(dir, line{Time: time.Now().UTC().Format(time.RFC3339Nano), Kind: "tk", Argv: args, Dir: wd})
	if p.TkExit != 0 {
		fmt.Fprint(os.Stderr, p.TkStderr)
		os.Exit(p.TkExit)
	}
	fmt.Fprint(os.Stdout, p.Scopes)
}

func start(dir string, p plan, args []string) {
	wd, _ := os.Getwd()
	stdin, _ := io.ReadAll(os.Stdin)
	kind := "launch"
	switch {
	case has(args, "--id"):
		kind = "id"
	case has(args, "--field"):
		kind = "field"
	case has(args, "get"):
		kind = "get"
	}
	record(dir, line{
		Time:  time.Now().UTC().Format(time.RFC3339Nano),
		Kind:  kind,
		Argv:  args,
		Dir:   wd,
		Stdin: string(stdin),
	})
	switch kind {
	case "id":
		if p.IDExit != 0 {
			fmt.Fprint(os.Stderr, p.IDStderr)
			os.Exit(p.IDExit)
		}
		fmt.Fprintln(os.Stdout, p.ID)
	case "get":
		if p.BodyExit != 0 {
			os.Exit(p.BodyExit)
		}
		fmt.Fprint(os.Stdout, p.Body)
	case "field":
		module := positional(args)
		field := flagValue(args, "--field")
		if strings.HasPrefix(field, "permission.") {
			if p.DenyPerm[module] {
				fmt.Fprintf(os.Stderr, "%s has no field %s\n", module, field)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stdout, "defined")
			return
		}
		spec := p.Locator[module]
		if spec.Out == "" && spec.Exit == 0 && !locatorSet(p, module) {
			spec.Out = "sessionId\n"
		}
		if spec.Exit != 0 {
			if spec.Stderr != "" {
				fmt.Fprint(os.Stderr, spec.Stderr)
			}
			os.Exit(spec.Exit)
		}
		fmt.Fprint(os.Stdout, spec.Out)
	default:
		launch(dir, p, string(stdin))
	}
}

func locatorSet(p plan, module string) bool {
	_, ok := p.Locator[module]
	return ok
}

func launch(dir string, p plan, stdin string) {
	name := memberName(stdin)
	spec := p.Launch[name]
	if spec.Register {
		postRegister(stdin, name)
	}
	hold := filepath.Join(dir, "hold", name)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(hold); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if spec.Sleep != "" {
		d, err := time.ParseDuration(spec.Sleep)
		if err == nil {
			time.Sleep(d)
		}
	}
	out := spec.JSON
	if out == "" {
		out = `{"sessionId":"sess-` + name + `"}`
	}
	fmt.Fprint(os.Stdout, out)
	if spec.Stderr != "" {
		fmt.Fprint(os.Stderr, spec.Stderr)
	}
	record(dir, line{Time: time.Now().UTC().Format(time.RFC3339Nano), Kind: "launch-exit", Argv: []string{name}})
	code := spec.Exit
	if spec.ExitOn != "" && !strings.Contains(stdin, spec.ExitOn) {
		code = 0
	}
	os.Exit(code)
}

func postRegister(stdin, name string) {
	bus := os.Getenv("HOTSEAT_FAKE_BUS")
	conv := between(stdin, "in conversation ", ".")
	if bus == "" || conv == "" || name == "" {
		return
	}
	body := []byte(`{"conversation":"` + conv + `","name":"` + name + `"}`)
	for i := 0; i < 50; i++ {
		req, err := http.NewRequest(http.MethodPost, "http://"+bus+"/register", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		if err := resp.Body.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		// A refusal is HTTP 200. Retry until the launched row exists.
		if resp.StatusCode == http.StatusOK && bytes.Contains(raw, []byte(`"outcome":"ok"`)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func record(dir string, item line) {
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Fprintln(os.Stderr, cerr)
		}
	}()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	defer func() {
		if uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); uerr != nil {
			fmt.Fprintln(os.Stderr, uerr)
		}
	}()
	raw, err := json.Marshal(item)
	if err != nil {
		return
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return ""
}

func positional(args []string) string {
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch {
		case a == "--field" || a == "--agent" || a == "--model" || a == "--role" || a == "--context" || a == "--permission" || a == "--output":
			skip = true
		case strings.HasPrefix(a, "-"):
		default:
			if a != "get" {
				return a
			}
		}
	}
	return ""
}

func memberName(stdin string) string {
	const mark = "You are "
	i := strings.Index(stdin, mark)
	if i < 0 {
		return ""
	}
	rest := stdin[i+len(mark):]
	name, _, _ := strings.Cut(rest, " ")
	return name
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	rest := s[i+len(start):]
	out, _, _ := strings.Cut(rest, end)
	return out
}
