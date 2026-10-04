// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"net"
	"testing"
)

func TestResolveListen(t *testing.T) {
	tests := []struct {
		name  string
		addr  string
		bind  string
		class ListenClass
		ok    bool
	}{
		{"default", DefaultListen, DefaultListen, ListenLoopback, true},
		{"loopback high octet", "127.8.8.8:1", "127.8.8.8:1", ListenLoopback, true},
		{"ipv6", "[::1]:4727", "[::1]:4727", ListenLoopback, true},
		{"port zero", "127.0.0.1:0", "127.0.0.1:0", ListenLoopback, true},
		{"all interfaces", "0.0.0.0:4727", "0.0.0.0:4727", ListenRemote, true},
		{"unspecified v6", "[::]:4727", "[::]:4727", ListenRemote, true},
		{"empty host", ":4727", ":4727", ListenRemote, true},
		{"public", "192.0.2.1:9", "192.0.2.1:9", ListenRemote, true},
		{"missing port", "127.0.0.1", "", 0, false},
		{"port too high", "127.0.0.1:65536", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bind, class, err := ResolveListen(tt.addr)
			if tt.ok && (err != nil || class != tt.class || bind != tt.bind) {
				t.Fatalf("ResolveListen(%q) = %q, %v, %v", tt.addr, bind, class, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("ResolveListen(%q) = %q, %v", tt.addr, bind, class)
			}
		})
	}
}

func TestResolveListenLocalhostIsOneLoopbackLiteral(t *testing.T) {
	bind, class, err := ResolveListen("localhost:4727")
	if err != nil || class != ListenLoopback {
		t.Fatalf("ResolveListen = %q, %v, %v", bind, class, err)
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil || port != "4727" {
		t.Fatalf("bind %q %v", bind, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("bind %q is not a loopback literal", bind)
	}
}

func TestChooseBindUsesOneAnswer(t *testing.T) {
	tests := []struct {
		name  string
		ips   []string
		class ListenClass
		want  string
		ok    bool
	}{
		{"both loopback prefers v4", []string{"::1", "127.0.0.1"}, ListenLoopback, "127.0.0.1", true},
		{"v6 loopback only", []string{"::1"}, ListenLoopback, "::1", true},
		{"mixed binds the public address", []string{"127.0.0.1", "192.0.2.10"}, ListenRemote, "192.0.2.10", true},
		{"public v6", []string{"::1", "2001:db8::1"}, ListenRemote, "2001:db8::1", true},
		{"public v4 wins over later v6", []string{"2001:db8::1", "192.0.2.10"}, ListenRemote, "192.0.2.10", true},
		{"unknown beside loopback", []string{"not-an-ip", "127.0.0.1"}, 0, "", false},
		{"unknown beside public", []string{"not-an-ip", "192.0.2.1"}, ListenRemote, "192.0.2.1", true},
		{"unknown only", []string{"not-an-ip"}, 0, "", false},
		{"empty", nil, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bind, class, err := chooseBind(tt.ips, "4727")
			if !tt.ok {
				if err == nil {
					t.Fatalf("chooseBind = %q, %v", bind, class)
				}
				return
			}
			if err != nil || class != tt.class {
				t.Fatalf("chooseBind = %q, %v, %v", bind, class, err)
			}
			host, port, err := net.SplitHostPort(bind)
			if err != nil || port != "4727" {
				t.Fatalf("bind %q %v", bind, err)
			}
			if !net.ParseIP(host).Equal(net.ParseIP(tt.want)) {
				t.Fatalf("bind %q want %s", bind, tt.want)
			}
		})
	}
}

func TestListenNetworkKeepsTheNamedFamily(t *testing.T) {
	if ListenNetwork("not-an-address") != "tcp" {
		t.Fatal("malformed")
	}
	if ListenNetwork(":4727") != "tcp" {
		t.Fatal("blank host")
	}
	tests := []struct {
		addr string
		v4   bool
	}{
		{"0.0.0.0:0", true},
		{"[::ffff:0.0.0.0]:0", true},
		{"127.0.0.1:0", true},
		{"[::]:0", false},
		{"[::1]:0", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			network := ListenNetwork(tt.addr)
			ln, err := net.Listen(network, tt.addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := ln.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			})
			tcp, ok := ln.Addr().(*net.TCPAddr)
			if !ok || tcp == nil {
				t.Fatalf("addr %T", ln.Addr())
			}
			if v4 := tcp.IP.To4() != nil; v4 != tt.v4 {
				t.Fatalf("%s %s bound %s", network, tt.addr, ln.Addr())
			}
		})
	}
}

func TestAddrLoopback(t *testing.T) {
	loop, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := loop.Close(); err != nil {
			t.Errorf("close loopback: %v", err)
		}
	})
	if !AddrLoopback(loop.Addr()) {
		t.Fatalf("loopback %s", loop.Addr())
	}
	wide, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := wide.Close(); err != nil {
			t.Errorf("close wildcard: %v", err)
		}
	})
	if AddrLoopback(wide.Addr()) {
		t.Fatalf("wildcard %s", wide.Addr())
	}
	if AddrLoopback(nil) {
		t.Fatal("nil address")
	}
}
