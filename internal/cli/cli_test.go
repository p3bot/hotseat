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
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
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
	err := ExecuteArgs(context.Background(), []string{"bus", "--listen", addr})
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
	err := ExecuteArgs(context.Background(), []string{"bus", "--listen", "192.0.2.1:9"})
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
		err = ExecuteArgs(context.Background(), []string{"bus", "--listen", ln.Addr().String()})
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
			return ExecuteArgs(ctx, []string{"bus", "--listen", addr})
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
	if !strings.Contains(text, "hotseat bus\n") && !strings.Contains(text, "  hotseat bus\n") {
		t.Fatalf("help has no bare bus example:\n%s", text)
	}
}

func TestNonLoopbackDoesNotListenOrCreateStore(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	err := ExecuteArgs(context.Background(), []string{
		"bus", "--store", storeDir, "--listen", "192.0.2.1:9",
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
	busCmd, _, err := cmd.Find([]string{"bus"})
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
			cmd := exec.Command(bin, "bus", "--store", t.TempDir(), "--listen", addr)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
				if err == nil {
					if err := conn.Close(); err != nil {
						t.Fatal(err)
					}
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					t.Fatalf("bus did not listen: %v\n%s", err, stderr.String())
				}
				time.Sleep(10 * time.Millisecond)
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
