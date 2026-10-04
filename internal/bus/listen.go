// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

const (
	// DefaultListen is the loopback address used when the operator does not set one.
	DefaultListen = "127.0.0.1:4727"
	// DefaultMaxBody is the message body limit in bytes, 512 KiB.
	DefaultMaxBody = 512 * 1024
)

// ValidateListen accepts a host:port only when every address it names is loopback.
// It does not open a socket. An empty host, an unspecified address, and any
// public address are refused.
func ValidateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("listen address port must be an integer from 0 to 65535")
	}
	if host == "" {
		return errors.New("listen address is not loopback")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return errors.New("listen address is not loopback")
		}
		return nil
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("listen address is not loopback")
	}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("listen address is not loopback")
		}
	}
	return nil
}
