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

// ListenClass is whether a listen socket is loopback.
type ListenClass int

const (
	// ListenLoopback is a socket that is loopback.
	// It does not require a capability token.
	ListenLoopback ListenClass = iota + 1
	// ListenRemote is any other socket, including all interfaces.
	// The listener requires a capability token.
	ListenRemote
)

// ResolveListen returns the address to pass to net.Listen and the class of that socket.
// A literal is returned unchanged. A name is resolved once. When every address is
// loopback, the result is one loopback address. When any address is not, the result
// is one non-loopback address, so the socket is the listener that requires a token.
// It does not open a socket. A malformed address returns an error.
func ResolveListen(addr string) (bind string, class ListenClass, err error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("listen address: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return "", 0, errors.New("listen address port must be an integer from 0 to 65535")
	}
	if host == "" {
		return addr, ListenRemote, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return addr, ListenLoopback, nil
		}
		return addr, ListenRemote, nil
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return "", 0, fmt.Errorf("listen address: %w", err)
	}
	return chooseBind(ips, port)
}

// chooseBind picks one address from a single lookup.
// A non-loopback address wins so the socket is the listener that requires a token.
// IPv4 wins inside that set so a dual-stack name does not depend on answer order.
func chooseBind(ips []string, port string) (string, ListenClass, error) {
	var loopback, remote []string
	var unknown bool
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			unknown = true
			continue
		}
		if ip.IsLoopback() {
			loopback = append(loopback, s)
			continue
		}
		remote = append(remote, s)
	}
	if len(remote) > 0 {
		return net.JoinHostPort(preferIPv4(remote), port), ListenRemote, nil
	}
	if unknown || len(loopback) == 0 {
		return "", 0, errors.New("listen address has no addresses")
	}
	return net.JoinHostPort(preferIPv4(loopback), port), ListenLoopback, nil
}

func preferIPv4(ips []string) string {
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
			return s
		}
	}
	return ips[0]
}

// ListenNetwork is the network for net.Listen on a bind address from ResolveListen.
// An IPv4 address uses tcp4 and an IPv6 address uses tcp6, so a wildcard stays
// in the family that was named. A blank host uses tcp and accepts both families.
func ListenNetwork(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return "tcp"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "tcp"
	}
	if ip.To4() != nil {
		return "tcp4"
	}
	return "tcp6"
}

// AddrLoopback reports whether a bound socket is loopback.
// An address that cannot be read is not loopback, so an empty token is refused.
func AddrLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp == nil {
		return false
	}
	return tcp.IP.IsLoopback()
}
