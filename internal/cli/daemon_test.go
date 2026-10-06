// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

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
