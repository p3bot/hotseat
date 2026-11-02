// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
)

const (
	busPortFloor = 1024
	// Below the default ephemeral start on macOS (49152).
	fixedEphemeralLow = 10000
	portRangeFile     = "/proc/sys/net/ipv4/ip_local_port_range"
)

// busPorts is the walk of one test process. Tests in this package run serially, so it needs no lock.
var busPorts = sync.OnceValues(func() (*portPairs, error) {
	low, err := ephemeralLow()
	if err != nil {
		return nil, err
	}
	return newBusPortPairs(low, rand.Int())
})

// newBusPortPairs walks the bus test range and keeps clear of the default bus and page.
func newBusPortPairs(low, start int) (*portPairs, error) {
	reserved, err := defaultBusPort()
	if err != nil {
		return nil, err
	}
	return newPortPairs(busPortFloor, low, reserved, start)
}

func defaultBusPort() (int, error) {
	_, port, err := net.SplitHostPort(bus.DefaultListen)
	if err != nil {
		return 0, fmt.Errorf("default listen: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf("default listen port: %w", err)
	}
	return n, nil
}

// freeBusPageAddr returns a loopback address for a bus with its page at the next port.
// Both ports are below the ephemeral range, and no two returned addresses share a port.
func freeBusPageAddr(t *testing.T) string {
	t.Helper()
	pairs, err := busPorts()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := pairs.claim()
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func ephemeralLow() (int, error) {
	if runtime.GOOS != "linux" {
		return fixedEphemeralLow, nil
	}
	text, err := os.ReadFile(portRangeFile)
	if err != nil {
		return 0, fmt.Errorf("ephemeral port range: %w", err)
	}
	return parseEphemeralLow(string(text))
}

func parseEphemeralLow(text string) (int, error) {
	fields := strings.Fields(text)
	if len(fields) != 2 {
		return 0, fmt.Errorf("ephemeral port range %q is not two ports", text)
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("ephemeral port range %q: %w", text, err)
	}
	if _, err := strconv.Atoi(fields[1]); err != nil {
		return 0, fmt.Errorf("ephemeral port range %q: %w", text, err)
	}
	return low, nil
}

// portPairs hands out each two-port slot between floor and the ephemeral low bound at most once.
// Whole slots keep two pairs from sharing a port, including after the walk wraps.
type portPairs struct {
	floor, low int
	reserved   int
	slots      int
	next, left int
}

// newPortPairs walks from the slot at start, modulo the slot count.
// It skips any slot that touches reserved or the port after it, the default bus and page.
func newPortPairs(floor, low, reserved, start int) (*portPairs, error) {
	slots := (low - floor) / 2
	if slots < 1 {
		return nil, fmt.Errorf("no room for bus and page ports between floor %d and ephemeral low bound %d", floor, low)
	}
	return &portPairs{floor: floor, low: low, reserved: reserved, slots: slots, next: start % slots, left: slots}, nil
}

func (p *portPairs) take() (int, bool) {
	for p.left > 0 {
		port := p.floor + 2*p.next
		p.next = (p.next + 1) % p.slots
		p.left--
		if port > p.reserved+1 || port+1 < p.reserved {
			return port, true
		}
	}
	return 0, false
}

func (p *portPairs) claim() (string, error) {
	var skipped error
	for {
		port, ok := p.take()
		if !ok {
			exhausted := fmt.Errorf("no free bus and page ports left between floor %d and ephemeral low bound %d", p.floor, p.low)
			if skipped != nil {
				return "", fmt.Errorf("%w: last refusal: %w", exhausted, skipped)
			}
			return "", exhausted
		}
		err := probePair(port)
		if err == nil {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
		}
		// Only a held or forbidden port is cured by another pair.
		if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, syscall.EACCES) {
			return "", err
		}
		skipped = err
	}
}

// probePair binds the bus port on every interface because some tests serve it on 0.0.0.0.
func probePair(port int) error {
	busLn, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	pageLn, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1)))
	if err == nil {
		err = pageLn.Close()
	}
	return errors.Join(err, busLn.Close())
}

func TestBusPortPairsNeverTouchDefaultBusOrPage(t *testing.T) {
	reserved, err := defaultBusPort()
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := newBusPortPairs(fixedEphemeralLow, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		port, ok := pairs.take()
		if !ok {
			break
		}
		count++
		if port <= reserved+1 && port+1 >= reserved {
			t.Fatalf("pair %d touches default bus %d or its page", port, reserved)
		}
	}
	if count == 0 {
		t.Fatal("walk returned no pairs")
	}
}

func TestFreeBusPageAddrStaysBelowEphemeralWithoutReuse(t *testing.T) {
	low, err := ephemeralLow()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for range 20 {
		addr := freeBusPageAddr(t)
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		if host != "127.0.0.1" {
			t.Fatalf("host %s", host)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatal(err)
		}
		if port < busPortFloor {
			t.Fatalf("port %d is below the floor", port)
		}
		for _, p := range []int{port, port + 1} {
			if p >= low {
				t.Fatalf("pair %d reaches ephemeral low bound %d", port, low)
			}
			if seen[p] {
				t.Fatalf("port %d returned twice", p)
			}
			seen[p] = true
		}
	}
}

func TestPortPairsWalkFromStartWithoutReuse(t *testing.T) {
	for _, tt := range []struct {
		name  string
		floor int
		low   int
		start int
		want  []int
	}{
		{"wraps once", 2000, 2006, 1, []int{2002, 2004, 2000}},
		{"start beyond the slot count", 2000, 2006, 5, []int{2004, 2000, 2002}},
		{"odd width drops the partial slot", 2000, 2003, 0, []int{2000}},
		{"skips the default bus and page", 4724, 4732, 0, []int{4724, 4730}},
		{"slot ending just below the default", 4725, 4729, 0, []int{4725}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pairs, err := newPortPairs(tt.floor, tt.low, 4727, tt.start)
			if err != nil {
				t.Fatal(err)
			}
			got := []int{}
			for {
				port, ok := pairs.take()
				if !ok {
					break
				}
				got = append(got, port)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPortPairsRefuseWhenNoRoomBelowEphemeral(t *testing.T) {
	for _, low := range []int{80, 1024, 1025} {
		_, err := newPortPairs(busPortFloor, low, 4727, 0)
		if err == nil || !strings.Contains(err.Error(), "no room") {
			t.Fatalf("low %d err %v", low, err)
		}
	}
	if _, err := newPortPairs(busPortFloor, 1026, 4727, 0); err != nil {
		t.Fatal(err)
	}
}

func TestEphemeralLowRequiresTwoNumericPorts(t *testing.T) {
	low, err := parseEphemeralLow("32768\t60999\n")
	if err != nil || low != 32768 {
		t.Fatalf("low %d err %v", low, err)
	}
	for _, text := range []string{"", "32768", "x 60999", "32768 x", "1 2 3"} {
		if _, err := parseEphemeralLow(text); err == nil {
			t.Fatalf("%q parsed", text)
		}
	}
}

func TestClaimRejectsPairHeldOnEitherPort(t *testing.T) {
	_, portText, err := net.SplitHostPort(freeBusPageAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		hold string
	}{
		{"page on loopback", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1))},
		{"bus on another address", net.JoinHostPort("127.0.0.2", portText)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			held, err := net.Listen("tcp", tt.hold)
			if err != nil {
				t.Skipf("cannot hold %s: %v", tt.hold, err)
			}
			defer func() {
				if err := held.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			}()
			pairs, err := newPortPairs(port, port+2, 4727, 0)
			if err != nil {
				t.Fatal(err)
			}
			addr, err := pairs.claim()
			if !errors.Is(err, syscall.EADDRINUSE) || !strings.Contains(err.Error(), "no free bus and page ports") {
				t.Fatalf("addr %q err %v", addr, err)
			}
		})
	}
}

func TestClaimReturnsBindFailureAnotherPairCannotCure(t *testing.T) {
	// Port 65536 is invalid, so the bind fails whatever this machine holds.
	pairs, err := newPortPairs(65536, 65538, 4727, 0)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := pairs.claim()
	if err == nil || !strings.Contains(err.Error(), "invalid port") || strings.Contains(err.Error(), "no free") {
		t.Fatalf("addr %q err %v", addr, err)
	}
}
