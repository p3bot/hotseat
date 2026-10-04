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
)

func TestStoreFlagRequiredLeavesWorkingDirectoryAlone(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	err := ExecuteArgs(context.Background(), []string{"bus"})
	if err == nil {
		t.Fatal("expected an error")
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("working directory gained %d entries", len(entries))
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
	err = runBus(context.Background(), t.TempDir(), ln.Addr().String(), "", bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("err = %v", err)
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
