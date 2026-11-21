// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	// renewEvery is how often run renews its lease. Three renewals can fail inside bus.LeaseTTL.
	renewEvery = 5 * time.Second
	// releaseLimit bounds the exit release, which runs after the run context may have ended.
	releaseLimit = 5 * time.Second
)

// errLeaseLost stops a run whose conductor lease is gone. Every write it made before was made under the lease.
var errLeaseLost = errors.New("conductor lease lost")

// pace is run's timing. Only tests pass anything but defaultPace.
type pace struct {
	// grace is how long the sender of the stopping done may keep running.
	grace time.Duration
	// renew is the lease renewal interval.
	renew time.Duration
	// sleep is the single wait on a conversation another conductor holds.
	sleep func(ctx context.Context, d time.Duration) error
}

func defaultPace() pace {
	return pace{grace: DoneGrace, renew: renewEvery, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// mintHolder returns a fresh lease holder: 16 random bytes as 32 lowercase hex characters.
func mintHolder() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// acquire takes the conversation lease and starts its renewal. With wait set, a conversation
// another conductor holds is waited on once, for what is left on that lease: a dead holder's
// lease runs out and the second acquire succeeds, and a live holder's renewal refuses it again.
func (e *engine) acquire(wait bool) error {
	res, err := e.lease(e.root, bus.LeaseAcquire)
	if wait && refusedFor(err, bus.ReasonConductorHeld) {
		d := bus.LeaseTTL
		if res.Remaining != nil {
			d = min(time.Duration(*res.Remaining)*time.Second, bus.LeaseTTL)
		}
		if err := e.pace.sleep(e.root, d); err != nil {
			return err
		}
		_, err = e.lease(e.root, bus.LeaseAcquire)
	}
	if err != nil {
		return err
	}
	e.leased = true
	// The renewal outlives the engine's child context: the done sender's grace and the
	// members recorded after a cancel still make fenced writes.
	ctx, stop := context.WithCancel(e.root)
	done := make(chan struct{})
	e.leaseEnd = func() {
		stop()
		<-done
	}
	go e.renew(ctx, done)
	return nil
}

// renew keeps the lease until ctx ends. A lease that is not held stops the engine, as does any
// other refusal or a call the bus could not complete. A transport failure is reported and retried
// at the next tick, since fencing stops the run if the lease runs out meanwhile.
func (e *engine) renew(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	tick := time.NewTicker(e.pace.renew)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		req := client.LeaseRequest{Conversation: e.name, Lease: bus.LeaseRenew, Holder: e.holder}
		res, err := client.Do(ctx, e.addr, "lease", e.token, req)
		if ctx.Err() != nil {
			return
		}
		fail := &busError{op: callLabel("lease", req), outcome: res.Outcome, reason: res.Reason}
		switch {
		case err != nil:
			_ = e.noteHalt(fmt.Errorf("%s: %w", fail.op, err))
			return
		case res.Outcome == client.OutcomeConnectionFailure:
			if werr := e.show(fail); werr != nil {
				_ = e.noteHalt(werr)
				return
			}
		case res.Outcome == bus.OutcomeOK:
		case res.Outcome == bus.OutcomeRefused && res.Reason == bus.ReasonLeaseNotHeld:
			_ = e.loseLease(res.Reason)
			return
		default:
			_ = e.noteHalt(fail)
			return
		}
	}
}

// endLease stops the renewal and releases the lease, so a later get reports it free at once.
// It runs once drive has returned. A lost lease is not released, and its members are left for the next holder to record.
// A failed release is reported and leaves the run's result alone, because the lease runs out by itself within bus.LeaseTTL.
func (e *engine) endLease() {
	if e.leaseEnd == nil {
		return
	}
	e.leaseEnd()
	e.leaseEnd = nil
	if e.leaseLost() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.root), releaseLimit)
	defer cancel()
	if _, err := e.lease(ctx, bus.LeaseRelease); err != nil {
		_ = e.show(err)
	}
}

// lease makes one lease call as this run's holder. Any outcome but ok is an error.
func (e *engine) lease(ctx context.Context, op string) (client.Result, error) {
	return e.call(ctx, "lease", client.LeaseRequest{Conversation: e.name, Lease: op, Holder: e.holder})
}

// haltOnLostLease stops the engine when res refuses a conductor write for want of the lease, and returns that stop.
// A paused run learns here that its lease ran out.
func (e *engine) haltOnLostLease(res client.Result) error {
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonLeaseWrite {
		return nil
	}
	return e.loseLease(res.Reason)
}

// loseLease stops the engine for a lost lease. held ends first, so whatever sees the stop also sees the loss
// and leaves every member process running for the next holder to adopt.
func (e *engine) loseLease(reason string) error {
	e.loseHeld(errLeaseLost)
	return e.noteHalt(fmt.Errorf("%w: %s", errLeaseLost, reason))
}

// leaseLost reports whether this run lost its lease. A signal alone is not a loss.
func (e *engine) leaseLost() bool {
	return errors.Is(context.Cause(e.held), errLeaseLost)
}
