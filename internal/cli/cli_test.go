// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/store"
	"github.com/p3bot/hotseat/internal/web"
)

func TestDefaultStoreDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".local", "share", "hotseat")

	t.Run("unset", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "placeholder")
		if err := os.Unsetenv("XDG_DATA_HOME"); err != nil {
			t.Fatal(err)
		}
		got, err := defaultStoreDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != fallback {
			t.Fatalf("store = %s, want %s", got, fallback)
		}
	})
	t.Run("empty", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		got, err := defaultStoreDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != fallback {
			t.Fatalf("store = %s, want %s", got, fallback)
		}
	})
	t.Run("relative", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "relative/data")
		got, err := defaultStoreDir()
		if err != nil {
			t.Fatal(err)
		}
		if got != fallback {
			t.Fatalf("store = %s, want %s", got, fallback)
		}
	})
	t.Run("absolute", func(t *testing.T) {
		data := filepath.Join(t.TempDir(), "data")
		t.Setenv("XDG_DATA_HOME", data)
		t.Setenv("HOME", "")
		got, err := defaultStoreDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(data, "hotseat")
		if got != want {
			t.Fatalf("store = %s, want %s", got, want)
		}
	})
	t.Run("no home", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Setenv("XDG_DATA_HOME", "relative/data")
		if _, err := defaultStoreDir(); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("relative home", func(t *testing.T) {
		t.Setenv("HOME", "rel")
		t.Setenv("XDG_DATA_HOME", "")
		if _, err := defaultStoreDir(); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestRelativeHomeCreatesNothing(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", "rel")
	t.Setenv("XDG_DATA_HOME", "")
	addr := freeLoopbackAddr(t)
	err := ExecuteArgs(context.Background(), []string{"bus", "start", "--listen", addr})
	if err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("err = %v", err)
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("working directory gained %d entries", len(entries))
	}
	conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if dialErr == nil {
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		t.Fatal("listener stayed open")
	}
}

func TestOmittedStoreDoesNotCreateDirectoryWhenTokenMissing(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	home := t.TempDir()
	t.Setenv("HOME", home)
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", data)
	err := ExecuteArgs(context.Background(), []string{"bus", "start", "--listen", "192.0.2.1:9"})
	if err == nil || !strings.Contains(err.Error(), "requires a token file") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(data, "hotseat")); !os.IsNotExist(statErr) {
		t.Fatalf("default store directory was created: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(statErr) {
		t.Fatalf("home data directory was created: %v", statErr)
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("working directory gained %d entries", len(entries))
	}
}

func TestOmittedStoreUsesDataHome(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", "")
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", data)

	t.Run("failed bind creates nothing", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := ln.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		err = ExecuteArgs(context.Background(), []string{"bus", "start", "--listen", ln.Addr().String()})
		if err == nil || !strings.Contains(err.Error(), "listen on") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(data, "hotseat")); !os.IsNotExist(statErr) {
			t.Fatalf("default store directory was created: %v", statErr)
		}
	})

	t.Run("listening opens the data home", func(t *testing.T) {
		addr := freeLoopbackAddr(t)
		stopWhenListening(t, addr, func(ctx context.Context) error {
			return runBus(ctx, "", addr, "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
		})
		db := filepath.Join(data, "hotseat", store.FileName)
		if _, statErr := os.Stat(db); statErr != nil {
			t.Fatalf("database: %v", statErr)
		}
		entries, err := os.ReadDir(wd)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("working directory gained %d entries", len(entries))
		}
	})
}

func TestExplicitStoreIgnoresDataHome(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", data)
	storeDir := filepath.Join(t.TempDir(), "chosen")

	t.Run("failed bind creates nothing", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := ln.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		err = runBus(context.Background(), storeDir, ln.Addr().String(), "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "listen on") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
			t.Fatalf("store directory was created: %v", statErr)
		}
		if _, statErr := os.Stat(data); !os.IsNotExist(statErr) {
			t.Fatalf("data home was used: %v", statErr)
		}
	})

	t.Run("listening uses the explicit directory", func(t *testing.T) {
		addr := freeLoopbackAddr(t)
		stopWhenListening(t, addr, func(ctx context.Context) error {
			return runBus(ctx, storeDir, addr, "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
		})
		if _, statErr := os.Stat(filepath.Join(storeDir, store.FileName)); statErr != nil {
			t.Fatalf("database: %v", statErr)
		}
		if _, statErr := os.Stat(data); !os.IsNotExist(statErr) {
			t.Fatalf("data home was used: %v", statErr)
		}
	})
}

func TestOpenFailureReleasesListener(t *testing.T) {
	addr := freeLoopbackAddr(t)
	storePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(storePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runBus(context.Background(), storePath, addr, "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
	conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if dialErr == nil {
		if err := conn.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
		t.Fatal("listener stayed open")
	}
}

func TestBusHelpNamesDataHome(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := execute(context.Background(), []string{"bus", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("help: %v\n%s", err, stderr.String())
	}
	text := stdout.String()
	if strings.Contains(text, "store directory is required") || strings.Contains(text, "(required)") {
		t.Fatalf("help still requires --store:\n%s", text)
	}
	if !strings.Contains(text, "$XDG_DATA_HOME/hotseat") {
		t.Fatalf("help does not name the data home:\n%s", text)
	}
	for _, name := range []string{"hotseat bus start", "hotseat bus stop", "hotseat bus status", logName} {
		if !strings.Contains(text, name) {
			t.Fatalf("help missing %s:\n%s", name, text)
		}
	}
	if strings.Contains(text, "\n  hotseat bus\n") || strings.Contains(text, "\n  serve ") {
		t.Fatalf("help offers an attached bus:\n%s", text)
	}
	stdout.Reset()
	stderr.Reset()
	err = execute(context.Background(), []string{"bus", "start", "--help"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("start help: %v\n%s", err, stderr.String())
	}
	startHelp := stdout.String()
	if !strings.Contains(startHelp, "$XDG_DATA_HOME/hotseat") || !strings.Contains(startHelp, logName) {
		t.Fatalf("start help missing store or log:\n%s", startHelp)
	}
	stopOut := bytes.Buffer{}
	err = execute(context.Background(), []string{"bus", "stop", "--help"}, &stopOut, &stderr)
	if err != nil {
		t.Fatalf("stop help: %v\n%s", err, stderr.String())
	}
	if stopOut.String() == "" || strings.Contains(stopOut.String(), "--listen") || strings.Contains(stopOut.String(), "--debug") {
		t.Fatalf("stop help:\n%s", stopOut.String())
	}
}

func TestNonLoopbackDoesNotListenOrCreateStore(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	err := ExecuteArgs(context.Background(), []string{
		"bus", "start", "--store", storeDir, "--listen", "192.0.2.1:9",
	})
	if err == nil || !strings.Contains(err.Error(), "requires a token file") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
		t.Fatalf("store directory was created: %v", statErr)
	}
}

func TestLoopbackBindIsAttempted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	storeDir := t.TempDir()
	err = runBus(context.Background(), storeDir, ln.Addr().String(), "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(storeDir, store.FileName)); !os.IsNotExist(statErr) {
		t.Fatalf("database was created: %v", statErr)
	}
}

func freeLoopbackAddr(t *testing.T) string {
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

func stopWhenListening(t *testing.T, addr string, start func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- start(ctx)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
			break
		}
		select {
		case startErr := <-errCh:
			cancel()
			t.Fatalf("bus stopped before listening: %v", startErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			select {
			case <-errCh:
			case <-time.After(5 * time.Second):
			}
			t.Fatalf("bus did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("bus: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("bus did not stop")
	}
}

func TestFlagDefaults(t *testing.T) {
	cmd := newRoot()
	busCmd, _, err := cmd.Find([]string{"bus", "start"})
	if err != nil {
		t.Fatal(err)
	}
	listen, err := busCmd.Flags().GetString("listen")
	if err != nil {
		t.Fatal(err)
	}
	if listen != bus.DefaultListen {
		t.Fatalf("listen default = %s", listen)
	}
	maxBody, err := busCmd.Flags().GetInt("max-body")
	if err != nil {
		t.Fatal(err)
	}
	if maxBody != bus.DefaultMaxBody {
		t.Fatalf("max-body default = %d", maxBody)
	}
	_, class, err := bus.ResolveListen(bus.DefaultListen)
	if err != nil || class != bus.ListenLoopback {
		t.Fatalf("default listen %v %v", class, err)
	}
	webCmd, _, err := cmd.Find([]string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	pageListen, err := webCmd.Flags().GetString("listen")
	if err != nil {
		t.Fatal(err)
	}
	if pageListen != web.DefaultListen {
		t.Fatalf("web listen default = %s", pageListen)
	}
	pageBus, err := webCmd.Flags().GetString("address")
	if err != nil {
		t.Fatal(err)
	}
	if pageBus != bus.DefaultListen {
		t.Fatalf("web address default = %s", pageBus)
	}
	for _, name := range []string{"stop", "status"} {
		sub, _, err := cmd.Find([]string{"bus", name})
		if err != nil {
			t.Fatal(err)
		}
		if sub.Flags().Lookup("listen") != nil || sub.Flags().Lookup("debug") != nil || sub.Flags().Lookup("token-file") != nil {
			t.Fatalf("%s accepts a serve flag", name)
		}
		if sub.Flags().Lookup("store") == nil {
			t.Fatalf("%s has no store flag", name)
		}
	}
}

func TestSignalExit(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(t.TempDir(), "hotseat")
	build := exec.Command("go", "build", "-o", bin, "./cmd/hotseat")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	t.Run("foreground", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			fd   func(t *testing.T) *os.File
		}{
			{"closed", func(*testing.T) *os.File { return nil }},
			{"devnull", func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { f.Close() })
				return f
			}},
			{"pipe", func(t *testing.T) *os.File {
				t.Helper()
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					r.Close()
					w.Close()
				})
				return w
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				storeDir := filepath.Join(t.TempDir(), "store")
				cmd := exec.Command(bin, "bus", "serve", "--store", storeDir, "--listen", "127.0.0.1:0")
				if f := tt.fd(t); f != nil {
					cmd.ExtraFiles = []*os.File{f}
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				err := cmd.Run()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
					t.Fatalf("exit %v\n%s", err, stderr.String())
				}
				if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
					t.Fatalf("store created: %v", statErr)
				}
			})
		}
	})
	for _, tt := range []struct {
		name string
		sig  syscall.Signal
		code int
	}{
		{"interrupt", syscall.SIGINT, 130},
		{"terminate", syscall.SIGTERM, 143},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			cmd := exec.Command(bin, "bus", "serve", "--store", dir, "--listen", addr)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			cmd.ExtraFiles = []*os.File{w}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				w.Close()
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			type readyResult struct {
				rep startupReport
				err error
			}
			ready := make(chan readyResult, 1)
			go func() {
				var rep startupReport
				err := json.NewDecoder(r).Decode(&rep)
				ready <- readyResult{rep, err}
			}()
			select {
			case got := <-ready:
				if got.err != nil || !got.rep.OK {
					_ = cmd.Process.Kill()
					t.Fatalf("ready: %v %+v\n%s", got.err, got.rep, stderr.String())
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("bus did not become ready")
			}
			if err := cmd.Process.Signal(tt.sig); err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait()
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != tt.code {
				t.Fatalf("exit %v, want %d\n%s", waitErr, tt.code, stderr.String())
			}
		})
	}
}

func TestDefaultListenAcceptsLoopbackWithoutToken(t *testing.T) {
	probe, err := net.Listen("tcp", bus.DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runBus(ctx, dir, bus.DefaultListen, "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	}()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("bus stopped: %v", err)
		default:
		}
		body := bytes.NewReader([]byte(`{"name":"job"}`))
		req, err := http.NewRequest(http.MethodPost, "http://"+bus.DefaultListen+bus.PathCreate, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			time.Sleep(15 * time.Millisecond)
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); cerr != nil && readErr == nil {
			readErr = cerr
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d %s", resp.StatusCode, raw)
		}
		var out struct {
			Outcome string `json:"outcome"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.Outcome != "ok" {
			t.Fatalf("outcome = %s", raw)
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatal("test sent a token")
		}
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("bus did not stop")
		}
	}
	t.Fatalf("default listener did not answer: %v", last)
}

type cmdResult struct {
	stdout string
	stderr string
	code   int
}

func runBinCmd(t *testing.T, bin string, args ...string) cmdResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return cmdResult{stdout: stdout.String(), stderr: stderr.String()}
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("%v: %v\n%s", args, err, stderr.String())
	}
	return cmdResult{stdout: stdout.String(), stderr: stderr.String(), code: exitErr.ExitCode()}
}

func parseBusReport(t *testing.T, text string) (pid int, listen, storeDir string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "pid: "):
			n, err := strconv.Atoi(strings.TrimPrefix(line, "pid: "))
			if err != nil {
				t.Fatalf("pid %q: %v", line, err)
			}
			pid = n
		case strings.HasPrefix(line, "listen: "):
			listen = strings.TrimPrefix(line, "listen: ")
		case strings.HasPrefix(line, "store: "):
			storeDir = strings.TrimPrefix(line, "store: ")
		}
	}
	if pid <= 0 || listen == "" || storeDir == "" {
		t.Fatalf("report %q", text)
	}
	return pid, listen, storeDir
}

func TestBusWithoutSubcommandDoesNothing(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	err := ExecuteArgs(context.Background(), []string{"bus"})
	if err == nil {
		t.Fatal("bare bus succeeded")
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("working directory gained %d entries", len(entries))
	}
	if _, statErr := os.Stat(filepath.Join(home, "data")); !os.IsNotExist(statErr) {
		t.Fatalf("data home was created: %v", statErr)
	}
}

func TestStartRefusesTakenAddressWithoutStore(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	storeDir := filepath.Join(t.TempDir(), "store")
	err = ExecuteArgs(context.Background(), []string{"bus", "start", "--store", storeDir, "--listen", ln.Addr().String()})
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
		t.Fatalf("store directory was created: %v", statErr)
	}
}

func TestReadmeDescribesDetachedBus(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(wd, "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(text)
	for _, name := range []string{"hotseat bus start", "hotseat bus stop", "hotseat bus status", "hotseat.log", "$XDG_DATA_HOME/hotseat", "text result", "body <<5"} {
		if !strings.Contains(body, name) {
			t.Fatalf("readme missing %s", name)
		}
	}
	if strings.Contains(body, "prints one JSON object") {
		t.Fatal("readme shows a JSON object as command output")
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "hotseat bus" {
			t.Fatal("readme tells the operator to run an attached bus")
		}
	}
}

func TestDetachedBus(t *testing.T) {
	bin := buildBinary(t)

	t.Run("bare", func(t *testing.T) {
		wd := t.TempDir()
		home := t.TempDir()
		cmd := exec.Command(bin, "bus")
		cmd.Dir = wd
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_DATA_HOME="+filepath.Join(home, "data"))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("exit %v\n%s", err, stderr.String())
		}
		entries, err := os.ReadDir(wd)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("working directory gained %d entries", len(entries))
		}
		if _, statErr := os.Stat(filepath.Join(home, "data")); !os.IsNotExist(statErr) {
			t.Fatalf("data home was created: %v", statErr)
		}
		missing := filepath.Join(t.TempDir(), "missing")
		status := runBinCmd(t, bin, "bus", "status", "--store", missing)
		if status.code != 1 || !strings.Contains(status.stderr, "not running") {
			t.Fatalf("status %d\n%s%s", status.code, status.stdout, status.stderr)
		}
		if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
			t.Fatalf("status created the store: %v", statErr)
		}
		stopped := runBinCmd(t, bin, "bus", "stop", "--store", missing)
		if stopped.code != 0 {
			t.Fatalf("stop %d\n%s", stopped.code, stopped.stderr)
		}
		if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
			t.Fatalf("stop created the store: %v", statErr)
		}
	})

	t.Run("lifecycle", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		const secret = "hstk-lifecycle-token"
		tokenPath := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenPath, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stopBus(ctx, storeDir)
		})
		addr := freeLoopbackAddr(t)
		other := freeLoopbackAddr(t)
		started := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", addr)
		if started.code != 0 {
			t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
		}
		if strings.Contains(started.stdout+started.stderr, secret) {
			t.Fatal("start output contains the token")
		}
		pid, listen, gotStore := parseBusReport(t, started.stdout)
		if listen != addr || gotStore != storeDir {
			t.Fatalf("listen %s store %s", listen, gotStore)
		}
		if !pidAlive(pid) {
			t.Fatal("daemon exited")
		}
		for _, args := range [][]string{
			{"--listen", "not-an-address"},
			{"--token-file", filepath.Join(t.TempDir(), "missing")},
			{"--max-body", "0"},
		} {
			cmdArgs := append([]string{"bus", "start", "--store", storeDir}, args...)
			againBad := runBinCmd(t, bin, cmdArgs...)
			if againBad.code != 0 {
				t.Fatalf("start %v %d\n%s%s", args, againBad.code, againBad.stdout, againBad.stderr)
			}
			badPID, badListen, _ := parseBusReport(t, againBad.stdout)
			if badPID != pid || badListen != addr || countBuses(storeDir) != 1 {
				t.Fatalf("start %v pid %d listen %s buses %d", args, badPID, badListen, countBuses(storeDir))
			}
		}
		self, err := readProcStat(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		daemon, err := readProcStat(pid)
		if err != nil {
			t.Fatal(err)
		}
		if daemon.sid == self.sid || daemon.sid != pid || daemon.tty != 0 {
			t.Fatalf("session self %d daemon sid %d pid %d tty %d", self.sid, daemon.sid, pid, daemon.tty)
		}
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil || !sameFile(cwd, storeDir) {
			t.Fatalf("cwd %s err %v", cwd, err)
		}
		for _, fd := range []string{"1", "2"} {
			target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd))
			if err != nil || !sameFile(target, filepath.Join(storeDir, logName)) {
				t.Fatalf("fd %s -> %s (%v)", fd, target, err)
			}
		}
		stdin, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/0", pid))
		if err != nil || stdin != "/dev/null" {
			t.Fatalf("stdin %s (%v)", stdin, err)
		}
		if _, err := os.Stat(filepath.Join(storeDir, store.FileName)); err != nil {
			t.Fatal(err)
		}
		if countBuses(storeDir) != 1 {
			t.Fatalf("buses %d", countBuses(storeDir))
		}
		created := callClient(t, bin, "create", "--address", addr, "--name", "job")
		if created.Outcome != bus.OutcomeOK {
			t.Fatalf("create %+v", created)
		}

		again := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", other, "--debug", "--token-file", tokenPath)
		if again.code != 0 {
			t.Fatalf("second start %d\n%s%s", again.code, again.stdout, again.stderr)
		}
		if strings.Contains(again.stdout+again.stderr, secret) {
			t.Fatal("second start output contains the token")
		}
		againPID, againListen, _ := parseBusReport(t, again.stdout)
		if againPID != pid || againListen != addr {
			t.Fatalf("second start pid %d listen %s", againPID, againListen)
		}
		if countBuses(storeDir) != 1 || !pidAlive(pid) {
			t.Fatal("second start changed the process")
		}
		conn, err := net.DialTimeout("tcp", other, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Fatal("second listen address accepted a connection")
		}
		logBefore := readLog(t, storeDir)
		if bytes.Contains(logBefore, []byte("level=DEBUG")) || bytes.Contains(logBefore, []byte(secret)) {
			t.Fatalf("log %s", logBefore)
		}
		record, err := os.ReadFile(filepath.Join(storeDir, recordName))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(record, []byte(secret)) {
			t.Fatal("process record contains the token")
		}

		stopped := runBinCmd(t, bin, "bus", "stop", "--store", storeDir)
		if stopped.code != 0 {
			t.Fatalf("stop %d\n%s%s", stopped.code, stopped.stdout, stopped.stderr)
		}
		if pidAlive(pid) {
			t.Fatal("process still alive")
		}
		held, err := lockHeld(storeDir)
		if err != nil || held {
			t.Fatalf("lock held %v %v", held, err)
		}
		againStop := runBinCmd(t, bin, "bus", "stop", "--store", storeDir)
		if againStop.code != 0 {
			t.Fatalf("second stop %d\n%s", againStop.code, againStop.stderr)
		}
		down := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
		if down.code != 1 || !strings.Contains(down.stderr, "not running") {
			t.Fatalf("status %d\n%s%s", down.code, down.stdout, down.stderr)
		}
		missed := callClient(t, bin, "create", "--address", addr, "--name", "next")
		if missed.Outcome != client.OutcomeConnectionFailure {
			t.Fatalf("create after stop %+v", missed)
		}
		kept := readLog(t, storeDir)
		if !bytes.Contains(kept, logBefore) {
			t.Fatal("log lost the first run")
		}

		debugged := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", addr, "--debug")
		if debugged.code != 0 {
			t.Fatalf("debug start %d\n%s%s", debugged.code, debugged.stdout, debugged.stderr)
		}
		debugPID, debugListen, _ := parseBusReport(t, debugged.stdout)
		if debugPID == pid || debugListen != addr {
			t.Fatalf("debug pid %d listen %s", debugPID, debugListen)
		}
		debugLog := readLog(t, storeDir)
		if !bytes.Contains(debugLog, kept) || !bytes.Contains(debugLog, []byte("level=DEBUG")) {
			t.Fatalf("debug log %s", debugLog)
		}
		if strings.Contains(debugged.stdout+debugged.stderr, secret) || bytes.Contains(debugLog, []byte(secret)) {
			t.Fatal("debug output contains the token")
		}

		status := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
		if status.code != 0 {
			t.Fatalf("status %d\n%s%s", status.code, status.stdout, status.stderr)
		}
		statusPID, statusListen, statusStore := parseBusReport(t, status.stdout)
		if statusPID != debugPID || statusListen != addr || statusStore != storeDir {
			t.Fatalf("status pid %d listen %s store %s", statusPID, statusListen, statusStore)
		}

		if err := syscall.Kill(debugPID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			dead := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
			if dead.code == 1 && strings.Contains(dead.stderr, "not running") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("status after kill %d\n%s%s", dead.code, dead.stdout, dead.stderr)
			}
			time.Sleep(20 * time.Millisecond)
		}
		revived := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", addr)
		if revived.code != 0 {
			t.Fatalf("restart %d\n%s%s", revived.code, revived.stdout, revived.stderr)
		}
		revivedPID, revivedListen, _ := parseBusReport(t, revived.stdout)
		if revivedPID == debugPID || revivedListen != addr || !pidAlive(revivedPID) {
			t.Fatalf("revived pid %d listen %s", revivedPID, revivedListen)
		}
		if callClient(t, bin, "create", "--address", addr, "--name", "revived").Outcome != bus.OutcomeOK {
			t.Fatal("create after restart failed")
		}
	})

	t.Run("symlink path", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		link := filepath.Join(base, "via-link")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stopBus(ctx, real)
		})
		addr := freeLoopbackAddr(t)
		started := runBinCmd(t, bin, "bus", "start", "--store", link, "--listen", addr)
		if started.code != 0 {
			t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
		}
		pid, listen, _ := parseBusReport(t, started.stdout)
		if listen != addr || !pidAlive(pid) {
			t.Fatalf("pid %d listen %s", pid, listen)
		}
		status := runBinCmd(t, bin, "bus", "status", "--store", real)
		if status.code != 0 {
			t.Fatalf("status %d\n%s%s", status.code, status.stdout, status.stderr)
		}
		statusPID, statusListen, statusStore := parseBusReport(t, status.stdout)
		if statusPID != pid || statusListen != addr || !sameFile(statusStore, real) {
			t.Fatalf("status pid %d listen %s store %s", statusPID, statusListen, statusStore)
		}
		again := runBinCmd(t, bin, "bus", "start", "--store", real, "--listen", freeLoopbackAddr(t))
		if again.code != 0 {
			t.Fatalf("second start %d\n%s%s", again.code, again.stdout, again.stderr)
		}
		againPID, againListen, _ := parseBusReport(t, again.stdout)
		if againPID != pid || againListen != addr || countBuses(real) != 1 {
			t.Fatalf("second start pid %d listen %s buses %d", againPID, againListen, countBuses(real))
		}
		stopped := runBinCmd(t, bin, "bus", "stop", "--store", real)
		if stopped.code != 0 {
			t.Fatalf("stop %d\n%s", stopped.code, stopped.stderr)
		}
		if pidAlive(pid) {
			t.Fatal("process still alive")
		}
		held, err := lockHeld(real)
		if err != nil || held {
			t.Fatalf("lock held %v %v", held, err)
		}
		down := runBinCmd(t, bin, "bus", "status", "--store", link)
		if down.code != 1 || !strings.Contains(down.stderr, "not running") {
			t.Fatalf("status after stop %d\n%s%s", down.code, down.stdout, down.stderr)
		}
	})

	t.Run("foreign lock", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		st, err := store.Open(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := writeRecord(storeDir, os.Getpid(), "127.0.0.1:1"); err != nil {
			t.Fatal(err)
		}
		addr := freeLoopbackAddr(t)
		started := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", addr)
		if started.code == 0 || !strings.Contains(started.stderr, "held") {
			t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
		}
		stopped := runBinCmd(t, bin, "bus", "stop", "--store", storeDir)
		if stopped.code != 0 {
			t.Fatalf("stop %d\n%s", stopped.code, stopped.stderr)
		}
		if _, err := st.List(context.Background()); err != nil {
			t.Fatal(err)
		}
		if countBuses(storeDir) != 0 {
			t.Fatal("start launched a bus while the lock was held")
		}
	})

	t.Run("resolved listen", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stopBus(ctx, storeDir)
		})
		_, port, err := net.SplitHostPort(freeLoopbackAddr(t))
		if err != nil {
			t.Fatal(err)
		}
		requested := net.JoinHostPort("localhost", port)
		bind, class, err := bus.ResolveListen(requested)
		if err != nil || class != bus.ListenLoopback || bind == requested {
			t.Fatalf("resolve %s class %v err %v", bind, class, err)
		}
		started := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", requested)
		if started.code != 0 {
			t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
		}
		pid, listen, _ := parseBusReport(t, started.stdout)
		if listen != bind {
			t.Fatalf("listen %s, resolved %s", listen, bind)
		}
		args, err := processArgs(pid)
		if err != nil {
			t.Fatal(err)
		}
		if got := argValue(args, "--listen"); got != bind {
			t.Fatalf("child listen %s", got)
		}
	})

	t.Run("missing record", func(t *testing.T) {
		storeDir := filepath.Join(t.TempDir(), "store")
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = stopBus(ctx, storeDir)
		})
		addr := freeLoopbackAddr(t)
		started := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", addr)
		if started.code != 0 {
			t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
		}
		pid, listen, _ := parseBusReport(t, started.stdout)
		if err := os.Remove(filepath.Join(storeDir, recordName)); err != nil {
			t.Fatal(err)
		}
		status := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
		if status.code != 0 {
			t.Fatalf("status %d\n%s%s", status.code, status.stdout, status.stderr)
		}
		statusPID, statusListen, statusStore := parseBusReport(t, status.stdout)
		if statusPID != pid || statusListen != listen || !sameFile(statusStore, storeDir) {
			t.Fatalf("status pid %d listen %s store %s", statusPID, statusListen, statusStore)
		}
		again := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", freeLoopbackAddr(t))
		if again.code != 0 {
			t.Fatalf("second start %d\n%s%s", again.code, again.stdout, again.stderr)
		}
		againPID, againListen, _ := parseBusReport(t, again.stdout)
		if againPID != pid || againListen != listen || countBuses(storeDir) != 1 {
			t.Fatalf("second start pid %d listen %s buses %d", againPID, againListen, countBuses(storeDir))
		}
		stopped := runBinCmd(t, bin, "bus", "stop", "--store", storeDir)
		if stopped.code != 0 {
			t.Fatalf("stop %d\n%s%s", stopped.code, stopped.stdout, stopped.stderr)
		}
		if pidAlive(pid) {
			t.Fatal("process still alive")
		}
		held, err := lockHeld(storeDir)
		if err != nil || held {
			t.Fatalf("lock held %v %v", held, err)
		}
		down := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
		if down.code != 1 || !strings.Contains(down.stderr, "not running") {
			t.Fatalf("status after stop %d\n%s%s", down.code, down.stdout, down.stderr)
		}
	})
}

func callClient(t *testing.T, bin string, args ...string) client.Result {
	t.Helper()
	got := runBinCmd(t, bin, args...)
	if got.code != 0 {
		t.Fatalf("%v exit %d\n%s%s", args, got.code, got.stdout, got.stderr)
	}
	return parseStdout(t, []byte(got.stdout))
}

func readLog(t *testing.T, storeDir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(storeDir, logName))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func countBuses(storeDir string) int {
	pids, err := busPIDs(storeDir)
	if err != nil {
		return 0
	}
	return len(pids)
}
