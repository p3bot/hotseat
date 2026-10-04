// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import "testing"

func TestValidateListen(t *testing.T) {
	tests := []struct {
		name string
		addr string
		ok   bool
	}{
		{"default", DefaultListen, true},
		{"loopback high octet", "127.8.8.8:1", true},
		{"ipv6", "[::1]:4727", true},
		{"port zero", "127.0.0.1:0", true},
		{"localhost", "localhost:4727", true},
		{"all interfaces", "0.0.0.0:4727", false},
		{"unspecified v6", "[::]:4727", false},
		{"empty host", ":4727", false},
		{"public", "192.0.2.1:9", false},
		{"missing port", "127.0.0.1", false},
		{"port too high", "127.0.0.1:65536", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateListen(tt.addr)
			if tt.ok && err != nil {
				t.Fatalf("ValidateListen(%q) = %v", tt.addr, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("ValidateListen(%q) succeeded", tt.addr)
			}
		})
	}
}
