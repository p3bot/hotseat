// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chat list covers call and chat attend covers its own setup. Each row here is another command that resolves the bus itself.
// A loopback bus ignores a token, so only a variable naming a missing file shows that a command read it.
func TestEveryCommandReadsTheBusVariablesAndTheFlagWins(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	create(t, context.Background(), addr, "job")
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("loopback-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	closed := closedAddr(t)

	for _, row := range []struct {
		command string
		check   func(t *testing.T)
	}{
		{"chat status", func(t *testing.T) {
			status := func(code int, want string, flags ...string) {
				t.Helper()
				out, stderr, err := runCommand(t, append([]string{"chat", "status", "job"}, flags...)...)
				got, reported := ReportedExit(err)
				if (code == ExitOK && err != nil) || (code != ExitOK && (!reported || got != code)) || stderr != "" || !strings.Contains(out, want) {
					t.Fatalf("%v err %v stderr %q\n%s", flags, err, stderr, out)
				}
			}
			t.Setenv(envAddress, addr)
			status(ExitOK, "outcome: ok\n")
			status(ExitUnavailable, "outcome: connection_failure\n", "--address", closed)
			t.Setenv(envTokenFile, missing)
			out, _, err := runCommand(t, "chat", "status", "job")
			if _, reported := ReportedExit(err); err == nil || reported || out != "" || !strings.Contains(err.Error(), "token file") {
				t.Fatalf("missing token file err %v\n%s", err, out)
			}
			status(ExitOK, "outcome: ok\n", "--token-file", token)
		}},
		{"web", func(t *testing.T) {
			page := func(want string, flags ...string) {
				t.Helper()
				listen := freeAddr(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				errCh := make(chan error, 1)
				go func() {
					errCh <- execute(ctx, append([]string{"web", "--listen", listen}, flags...), io.Discard, io.Discard)
				}()
				waitDial(t, listen, errCh)
				// The page calls the bus on request, so the request is what shows which bus it uses.
				status, body := httpGet(t, "http://"+listen+"/chats")
				cancel()
				waitErr(t, errCh)
				if !strings.Contains(body, want) {
					t.Fatalf("%v want %q in %d\n%s", flags, want, status, body)
				}
				if want == ">job<" && status != http.StatusOK {
					t.Fatalf("%v status %d", flags, status)
				}
			}
			t.Setenv(envAddress, addr)
			page(">job<")
			page("connection_failure", "--address", closed)
			t.Setenv(envTokenFile, missing)
			// The deadline ends a page that started without reading the variable.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := execute(ctx, []string{"web", "--listen", freeAddr(t)}, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "token file") {
				t.Fatalf("missing token file err %v", err)
			}
			page(">job<", "--token-file", token)
		}},
		{"bus status", func(t *testing.T) {
			t.Setenv(envAddress, addr)
			t.Setenv(envTokenFile, missing)
			var stdout, stderr bytes.Buffer
			err := execute(context.Background(), []string{"bus", "status"}, &stdout, &stderr)
			if err != nil || !strings.Contains(stdout.String(), "health: pass\n") || !strings.Contains(stdout.String(), "listen: "+addr+"\n") {
				t.Fatalf("variable err %v\n%s%s", err, stdout.String(), stderr.String())
			}
			stdout.Reset()
			err = execute(context.Background(), []string{"bus", "status", "--address", closed}, &stdout, &stderr)
			if code, ok := ReportedExit(err); !ok || code != ExitUnavailable || !strings.Contains(stdout.String(), "listen: "+closed+"\n") {
				t.Fatalf("flag err %v\n%s%s", err, stdout.String(), stderr.String())
			}
		}},
		{"bus start", func(t *testing.T) {
			start := func(env []string, flags ...string) cmdResult {
				t.Helper()
				storeDir := filepath.Join(t.TempDir(), "store")
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := stopBus(ctx, storeDir); err != nil {
						t.Errorf("stop: %v", err)
					}
				})
				return runBinEnv(t, bin, env, append([]string{"bus", "start", "--store", storeDir}, flags...)...)
			}
			listens := func(want string, env []string, flags ...string) {
				t.Helper()
				got := start(env, flags...)
				if got.code != ExitOK || !strings.Contains(got.stdout, "listen: "+want+"\n") {
					t.Fatalf("%v %v want listen %s, code %d\n%s%s", env, flags, want, got.code, got.stdout, got.stderr)
				}
			}
			refuses := func(env []string, flags ...string) {
				t.Helper()
				got := start(env, flags...)
				if got.code != ExitFailure || got.stdout != "" || !strings.Contains(got.stderr, "token file") {
					t.Fatalf("%v %v code %d\n%s%s", env, flags, got.code, got.stdout, got.stderr)
				}
			}
			fromEnv := freeBusPageAddr(t)
			listens(fromEnv, []string{envAddress + "=" + fromEnv})
			fromFlag := freeBusPageAddr(t)
			listens(fromFlag, []string{envAddress + "=" + freeBusPageAddr(t)}, "--address", fromFlag)
			refuses([]string{envTokenFile + "=" + missing}, "--address", freeBusPageAddr(t))
			refuses([]string{envTokenFile + "=" + token}, "--address", freeBusPageAddr(t), "--token-file", missing)
		}},
		{"run", func(t *testing.T) {
			resolves := func(wantAddr, wantToken string, flags ...string) {
				t.Helper()
				for _, verb := range newRunCmd().Commands() {
					if err := verb.ParseFlags(flags); err != nil {
						t.Fatal(err)
					}
					cfg, err := runFlags{}.config(verb)
					if err != nil || cfg.Address != wantAddr || cfg.TokenFile != wantToken || !cfg.TokenFileSet {
						t.Fatalf("%s %v address %q token file %q set %t err %v", verb.Name(), flags, cfg.Address, cfg.TokenFile, cfg.TokenFileSet, err)
					}
				}
			}
			t.Setenv(envAddress, addr)
			t.Setenv(envTokenFile, missing)
			resolves(addr, missing)
			resolves(closed, token, "--address", closed, "--token-file", token)
		}},
	} {
		t.Run(row.command, func(t *testing.T) {
			// An empty variable counts as unset, so no row inherits another's.
			t.Setenv(envAddress, "")
			t.Setenv(envTokenFile, "")
			row.check(t)
		})
	}
}
