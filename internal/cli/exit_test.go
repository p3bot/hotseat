// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func TestExitCodeNamesEveryOutcome(t *testing.T) {
	named := map[string]int{
		bus.OutcomeOK:                   ExitOK,
		bus.OutcomeRefused:              ExitRefused,
		bus.OutcomeTimeout:              ExitTimeout,
		bus.OutcomeUnavailable:          ExitUnavailable,
		client.OutcomeConnectionFailure: ExitUnavailable,
	}
	for word, code := range named {
		if got, ok := exitCodes[word]; !ok || got != code {
			t.Fatalf("%s code %d present %t", word, got, ok)
		}
	}
	if exitCode("not-an-outcome") == ExitOK {
		t.Fatal("unknown outcome exited 0")
	}
	for _, dir := range []string{"../bus", "../client"} {
		found := outcomeConstants(t, dir)
		if len(found) == 0 {
			t.Fatalf("%s: no outcome constant found", dir)
		}
		for name, word := range found {
			if _, ok := named[word]; !ok {
				t.Fatalf("%s %s = %q is not named with an exit code", dir, name, word)
			}
		}
	}
}

// outcomeConstants is every Outcome constant declared in the non-test files of dir, by name.
// It reads declarations, so a type, a lone const line, or another file cannot hide one.
// A value that is not a string literal fails the test, because it cannot be checked.
func outcomeConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	files := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				values := spec.(*ast.ValueSpec)
				for i, name := range values.Names {
					if !strings.HasPrefix(name.Name, "Outcome") {
						continue
					}
					var lit *ast.BasicLit
					if i < len(values.Values) {
						lit, _ = values.Values[i].(*ast.BasicLit)
					}
					if lit == nil || lit.Kind != token.STRING {
						t.Fatalf("%s: %s is not a string literal", path, name.Name)
					}
					word, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					found[name.Name] = word
				}
			}
		}
	}
	return found
}

func TestRootHelpListsBusChatRunAndWeb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := execute(context.Background(), []string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	got := commandNames(stdout.String())
	want := []string{"bus", "chat", "run", "web"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("commands %v\n%s", got, stdout.String())
	}
}

func commandNames(help string) []string {
	var names []string
	on := false
	for _, line := range strings.Split(help, "\n") {
		if strings.HasPrefix(line, "Available Commands:") {
			on = true
			continue
		}
		if !on {
			continue
		}
		if line == "" || strings.HasPrefix(line, "Flags:") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func TestChatExitsByOutcome(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	made := runBinCmd(t, bin, "chat", "create", "job", "--address", addr)
	if made.code != 0 {
		t.Fatalf("create %d\n%s%s", made.code, made.stdout, made.stderr)
	}
	ok := runBinCmd(t, bin, "chat", "send", "job", "--address", addr, "--from", "a", "--kind", "say", "--to", "b", "--body", "hi", "--txid", "1")
	if ok.code != 0 || !strings.Contains(ok.stdout, "outcome: ok\n") {
		t.Fatalf("send %d\n%s%s", ok.code, ok.stdout, ok.stderr)
	}
	missing := runBinCmd(t, bin, "chat", "send", "absent", "--address", addr, "--from", "a", "--kind", "say", "--body", "hi", "--txid", "1")
	if missing.code != ExitRefused || !strings.Contains(missing.stdout, "outcome: refused\n") {
		t.Fatalf("missing %d\n%s%s", missing.code, missing.stdout, missing.stderr)
	}
	waited := runBinCmd(t, bin, "chat", "wait", "job", "--address", addr, "--cursor", "1", "--deadline", "10ms")
	if waited.code != ExitTimeout || !strings.Contains(waited.stdout, "outcome: timeout\n") {
		t.Fatalf("wait %d\n%s%s", waited.code, waited.stdout, waited.stderr)
	}
	down := runBinCmd(t, bin, "chat", "list", "--address", closedAddr(t))
	if down.code != ExitUnavailable || !strings.Contains(down.stdout, "outcome: connection_failure\n") {
		t.Fatalf("down %d\n%s%s", down.code, down.stdout, down.stderr)
	}
}

func TestChatUsagePrintsNoStdout(t *testing.T) {
	bin := buildBinary(t)
	for _, args := range [][]string{
		{"chat", "send"},
		{"chat", "join", "job"},
		{"chat", "read", "job", "--to", "a", "--to", "b"},
		{"chat", "wait", "job", "--cursor", "0", "--to", "a", "--to", "b"},
	} {
		got := runBinCmd(t, bin, args...)
		if got.code != ExitFailure || got.stdout != "" || got.stderr == "" {
			t.Fatalf("%v code %d stdout %q stderr %q", args, got.code, got.stdout, got.stderr)
		}
	}
}

func TestReadDefaultsToCursorZeroLimitOneHundredAndPrintsMore(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "job")
	for i := 1; i <= 101; i++ {
		n := strconv.Itoa(i)
		sendAs(t, ctx, addr, "job", "alpha", "say", []string{"bravo"}, n, n)
	}
	page := func(first, last int64, more bool, extra ...string) {
		t.Helper()
		got := runBinCmd(t, bin, append([]string{"chat", "read", "job", "--address", addr}, extra...)...)
		if got.code != ExitOK {
			t.Fatalf("read %v %d\n%s%s", extra, got.code, got.stdout, got.stderr)
		}
		res := parseStdout(t, []byte(got.stdout))
		if res.More == nil || *res.More != more || res.Messages == nil {
			t.Fatalf("read %v want more %t\n%s", extra, more, got.stdout)
		}
		msgs := *res.Messages
		if int64(len(msgs)) != last-first+1 {
			t.Fatalf("read %v got %d messages, want seq %d through %d", extra, len(msgs), first, last)
		}
		for i, m := range msgs {
			if m.Seq != first+int64(i) {
				t.Fatalf("read %v message %d has seq %d, want %d", extra, i, m.Seq, first+int64(i))
			}
		}
	}
	page(1, 100, true)
	page(101, 101, false, "--cursor", "100")
}

func TestAddressAndTokenFileReadTheVariableAndTheFlagWins(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("loopback-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	env := []string{"HOTSEAT_ADDRESS=" + addr, "HOTSEAT_TOKEN_FILE=" + token}
	listed := runBinEnv(t, bin, env, "chat", "list")
	if listed.code != 0 || !strings.Contains(listed.stdout, "outcome: ok\n") {
		t.Fatalf("env %d\n%s%s", listed.code, listed.stdout, listed.stderr)
	}
	flag := runBinEnv(t, bin, env, "chat", "--address", closedAddr(t), "list")
	if flag.code != ExitUnavailable || !strings.Contains(flag.stdout, "connection_failure") {
		t.Fatalf("flag address %d\n%s%s", flag.code, flag.stdout, flag.stderr)
	}
	badEnv := runBinEnv(t, bin, []string{"HOTSEAT_ADDRESS=" + addr, "HOTSEAT_TOKEN_FILE=" + missing}, "chat", "list")
	if badEnv.code != ExitFailure || badEnv.stdout != "" {
		t.Fatalf("missing token file %d\n%s%s", badEnv.code, badEnv.stdout, badEnv.stderr)
	}
	won := runBinEnv(t, bin, []string{"HOTSEAT_ADDRESS=" + addr, "HOTSEAT_TOKEN_FILE=" + missing}, "chat", "--token-file", token, "list")
	if won.code != 0 || !strings.Contains(won.stdout, "outcome: ok\n") {
		t.Fatalf("flag token %d\n%s%s", won.code, won.stdout, won.stderr)
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
