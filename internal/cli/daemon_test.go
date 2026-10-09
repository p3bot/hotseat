// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/store"
)

func TestBareStopTargetsSkipAddressWithoutNetNS(t *testing.T) {
	storePIDs := []int{10}
	owners := []listenOwner{{pid: 20, dir: t.TempDir()}}
	got := bareStopTargets(t.TempDir(), storePIDs, false, owners)
	if len(got) != 1 || len(got[0].pids) != 1 || got[0].pids[0] != 10 {
		t.Fatalf("targets %+v", got)
	}
	got = bareStopTargets(t.TempDir(), storePIDs, true, owners)
	if len(got) != 2 {
		t.Fatalf("targets %+v", got)
	}
	var sawOther bool
	for _, target := range got {
		for _, pid := range target.pids {
			if pid == 20 {
				sawOther = true
			}
		}
	}
	if !sawOther {
		t.Fatalf("targets %+v", got)
	}
}

func TestSignalTargetsContinuesAfterKillError(t *testing.T) {
	var got []int
	denied := errors.New("operation not permitted")
	targets := []stopTarget{
		{dir: "/default", pids: []int{10}},
		{dir: "/other", pids: []int{20, 30}},
	}
	signalled, err := signalTargets(targets, func(pid int, sig syscall.Signal) error {
		if sig != syscall.SIGTERM {
			t.Fatalf("signal %d", sig)
		}
		got = append(got, pid)
		if pid == 10 {
			return denied
		}
		if pid == 30 {
			return syscall.ESRCH
		}
		return nil
	})
	if !errors.Is(err, denied) {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 3 || got[0] != 10 || got[1] != 20 || got[2] != 30 {
		t.Fatalf("signalled pids %v", got)
	}
	if len(signalled) != 1 || signalled[0].dir != "/other" || len(signalled[0].pids) != 2 || signalled[0].pids[0] != 20 || signalled[0].pids[1] != 30 {
		t.Fatalf("wait targets %+v", signalled)
	}
}

func TestSignalTargetsJoinsKillErrors(t *testing.T) {
	first := errors.New("operation not permitted")
	second := errors.New("invalid argument")
	_, err := signalTargets([]stopTarget{{dir: "/store", pids: []int{10, 20}}}, func(pid int, sig syscall.Signal) error {
		if sig != syscall.SIGTERM {
			t.Fatalf("signal %d", sig)
		}
		if pid == 10 {
			return first
		}
		return second
	})
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitAfterSignalKillErrorDoesNotWaitForLock(t *testing.T) {
	dir := t.TempDir()
	holdStoreLock(t, dir)
	denied := errors.New("operation not permitted")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := waitAfterSignal(ctx, []stopTarget{{dir: dir, pids: []int{os.Getpid(), os.Getpid() + 1}}}, func(pid int, _ syscall.Signal) error {
		if pid == os.Getpid() {
			return nil
		}
		return denied
	})
	if !errors.Is(err, denied) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitAfterSignalWaitsForLockWhenSignalled(t *testing.T) {
	dir := t.TempDir()
	holdStoreLock(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := waitAfterSignal(ctx, []stopTarget{{dir: dir, pids: []int{os.Getpid()}}}, func(int, syscall.Signal) error {
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func holdStoreLock(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, store.LockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
}

func TestListenStopError(t *testing.T) {
	if err := listenStopError(false, false); err != nil {
		t.Fatal(err)
	}
	if err := listenStopError(true, false); err != nil {
		t.Fatal(err)
	}
	err := listenStopError(false, true)
	if err == nil || !strings.Contains(err.Error(), "network namespace") || !strings.Contains(err.Error(), bus.DefaultListen) {
		t.Fatalf("err = %v", err)
	}
	err = listenStopError(true, true)
	if err == nil || !strings.Contains(err.Error(), "another process") || !strings.Contains(err.Error(), bus.DefaultListen) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcAddrDecode(t *testing.T) {
	got, err := decodeProcAddr("0100007F:1277")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:4727" {
		t.Fatalf("addr = %s", got)
	}
	got, err = decodeProcAddr("00000000000000000000000001000000:1277")
	if err != nil {
		t.Fatal(err)
	}
	if got != "[::1]:4727" {
		t.Fatalf("addr = %s", got)
	}
	if _, err := decodeProcAddr("nope"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestProcessListenAddrsRoundTrip(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		t.Run(network, func(t *testing.T) {
			host := "127.0.0.1"
			if network == "tcp6" {
				host = "::1"
			}
			ln, err := net.Listen(network, net.JoinHostPort(host, "0"))
			if err != nil {
				t.Skip(err)
			}
			defer ln.Close()
			addrs, err := processListenAddrs(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			want := ln.Addr().String()
			for _, addr := range addrs {
				if addr == want {
					return
				}
			}
			t.Fatalf("addrs %v, want %s", addrs, want)
		})
	}
}

func TestBusCommandMatchesStoreArg(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	serve := splitCmdline([]byte("hotseat\x00bus\x00serve\x00--store\x00" + link + "\x00--listen\x00127.0.0.1:1\x00"))
	if !isBusServe(serve) || !storeArgMatches(serve, real) {
		t.Fatal("serve via the link did not match the store")
	}
	other := splitCmdline([]byte("hotseat\x00bus\x00serve\x00--store\x00" + filepath.Join(base, "other") + "\x00"))
	if storeArgMatches(other, real) {
		t.Fatal("other store matched")
	}
	if isBusServe(splitCmdline([]byte("hotseat\x00bus\x00start\x00"))) {
		t.Fatal("start matched serve")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if busProcessMatches(os.Getpid(), dir) {
		t.Fatal("this process matched the store")
	}
}
