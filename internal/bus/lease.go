// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"context"
	"net/http"
	"time"
)

// LeaseTTL is how long a conductor lease lives after its last acquire or renew.
const LeaseTTL = 20 * time.Second

// Lease selectors for /lease.
const (
	LeaseAcquire = "acquire"
	LeaseRenew   = "renew"
	LeaseRelease = "release"
	LeaseGet     = "get"
)

// lease is one conductor's hold on a chat. Times are bus clock readings,
// so a reading from time.Now compares on its monotonic clock.
type lease struct {
	holder  string
	since   time.Time
	expires time.Time
}

// LeaseState is the lease part of a /lease result. Op is the selector.
// Held, Since, and Expires answer get. TTL answers an ok acquire or renew.
// Remaining is left on another holder's lease when an acquire is refused.
// The holder is never part of it.
type LeaseState struct {
	Op        string
	Held      bool
	Since     time.Time
	Expires   time.Time
	TTL       time.Duration
	Remaining time.Duration
}

type leaseRequest struct {
	Chat   *string `json:"chat"`
	Lease  *string `json:"lease"`
	Holder *string `json:"holder"`
}

func (s *server) handleLease(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req leaseRequest
	if !s.decode(w, r, "lease", "", start, &req) {
		return
	}
	res, err := s.svc.Lease(r.Context(), req)
	s.finish(w, "lease", deref(req.Chat), start, res, err)
}

// Lease applies one lease operation: the chat, the selector, then the
// holder for every selector but get, which ignores one sent. Leases live in
// process memory under the service lock and never touch the store, change
// rev, or wake a wait or watch. Expiry is checked here, on each call.
func (s *Service) Lease(ctx context.Context, req leaseRequest) (Result, error) {
	chat := deref(req.Chat)
	if err := requireChat(chat); err != nil {
		return asRefused(err)
	}
	if req.Lease == nil {
		return refused(ReasonLeaseRequired), nil
	}
	op := *req.Lease
	switch op {
	case LeaseAcquire, LeaseRenew, LeaseRelease, LeaseGet:
	default:
		return refused(ReasonLeaseBad), nil
	}
	var holder string
	if op != LeaseGet {
		var err error
		if holder, err = requireHolder(req.Holder); err != nil {
			return asRefused(err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	if _, err := s.store.Chat(ctx, chat); err != nil {
		return s.fail(err, "lease")
	}
	now := s.clock()
	cur, live := s.liveLease(chat, now)
	state := &LeaseState{Op: op}
	res := Result{Outcome: OutcomeOK, Lease: state}
	switch op {
	case LeaseAcquire:
		if live && cur.holder != holder {
			res = refused(ReasonConductorHeld)
			state.Remaining = cur.expires.Sub(now)
			res.Lease = state
			return res, nil
		}
		since := now
		if live {
			since = cur.since
		}
		s.leases[chat] = lease{holder: holder, since: since, expires: now.Add(LeaseTTL)}
		state.TTL = LeaseTTL
	case LeaseRenew:
		// A renew never creates a lease, so a holder that lost one cannot write again.
		if !live || cur.holder != holder {
			res = refused(ReasonLeaseNotHeld)
			res.Lease = state
			return res, nil
		}
		cur.expires = now.Add(LeaseTTL)
		s.leases[chat] = cur
		state.TTL = LeaseTTL
	case LeaseRelease:
		if live && cur.holder == holder {
			delete(s.leases, chat)
		}
	case LeaseGet:
		state.Held = live
		if live {
			state.Since = cur.since
			state.Expires = cur.expires
		}
	}
	return res, nil
}

// liveLease returns chat's lease when it has not expired at now, and forgets one that has.
// The caller holds s.mu.
func (s *Service) liveLease(chat string, now time.Time) (lease, bool) {
	cur, ok := s.leases[chat]
	if !ok {
		return lease{}, false
	}
	if !now.Before(cur.expires) {
		delete(s.leases, chat)
		return lease{}, false
	}
	return cur, true
}

// fence refuses a conductor write whose holder does not hold chat's live lease.
// The caller holds s.mu from this check through the write, so no lease change falls between them.
// A chat that does not exist is not found, since it cannot be leased.
func (s *Service) fence(ctx context.Context, chat, holder string) error {
	if cur, live := s.liveLease(chat, s.clock()); live && cur.holder == holder {
		return nil
	}
	if _, err := s.store.Chat(ctx, chat); err != nil {
		return err
	}
	return rule(ReasonLeaseWrite)
}

// requireHolder reads a required holder. Absent and null are missing. A present one uses the name grammar.
func requireHolder(holder *string) (string, error) {
	if holder == nil {
		return "", rule(ReasonHolderRequired)
	}
	if !validName(*holder) {
		return "", rule(ReasonHolderBad)
	}
	return *holder, nil
}

type wireLease struct {
	Held      *bool  `json:"held,omitempty"`
	Since     string `json:"since,omitempty"`
	Expires   string `json:"expires,omitempty"`
	TTL       *int64 `json:"ttl,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
}

// leaseWire fills the lease fields of a /lease result, a refused acquire included.
func leaseWire(res Result, out *wire) {
	st := res.Lease
	if st == nil {
		return
	}
	var w wireLease
	switch {
	case res.Outcome == OutcomeRefused && res.Reason == ReasonConductorHeld:
		w.Remaining = ptr(ceilSeconds(st.Remaining))
	case res.Outcome != OutcomeOK:
		return
	case st.Op == LeaseGet:
		w.Held = ptr(st.Held)
		if st.Held {
			w.Since = st.Since.UTC().Format(time.RFC3339Nano)
			w.Expires = st.Expires.UTC().Format(time.RFC3339Nano)
		}
	case st.Op == LeaseAcquire || st.Op == LeaseRenew:
		w.TTL = ptr(ceilSeconds(st.TTL))
	}
	out.wireLease = w
}

// ceilSeconds is d in whole seconds, rounded up.
func ceilSeconds(d time.Duration) int64 {
	return int64((d + time.Second - 1) / time.Second)
}

func ptr[T any](v T) *T { return &v }
