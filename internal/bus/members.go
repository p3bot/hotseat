// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"context"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

// Register sets the registered time from the bus clock.
// Status becomes registered only when the row is launched.
func (s *Service) Register(ctx context.Context, conv, name string) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	if err := requireParticipant(name); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	m, err := s.store.RegisterMember(ctx, conv, name, s.now())
	if err != nil {
		return s.fail(err, "register")
	}
	return Result{Outcome: OutcomeOK, Member: &m}, nil
}

// Member reads one row when status and pid are both absent.
// A pid without a status is refused and writes nothing.
// launched inserts a row. running, exited, and timed-out update an existing row.
// pid is stored only on those writes. The result does not carry a session id.
func (s *Service) Member(ctx context.Context, conv, name string, status *string, pid int64, hasPID bool) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	if err := requireParticipant(name); err != nil {
		return asRefused(err)
	}
	if status != nil && !memberWriteStatus(*status) {
		return refused(ReasonStatusBad), nil
	}
	if status == nil && hasPID {
		return refused(ReasonPIDNeedsStatus), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	var (
		m   store.Member
		err error
	)
	switch {
	case status == nil:
		m, err = s.store.Member(ctx, conv, name)
	case *status == store.MemberLaunched:
		m, err = s.store.LaunchMember(ctx, conv, name, s.now(), pid, hasPID)
	default:
		m, err = s.store.SetMemberStatus(ctx, conv, name, *status, pid, hasPID)
	}
	if err != nil {
		return s.fail(err, "member")
	}
	return Result{Outcome: OutcomeOK, Member: &m}, nil
}

// Members returns roster rows in roster order, without session ids or pids.
func (s *Service) Members(ctx context.Context, conv string) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.store.Members(ctx, conv)
	if err != nil {
		return s.fail(err, "members")
	}
	if rows == nil {
		rows = []store.Member{}
	}
	return Result{Outcome: OutcomeOK, Members: rows}, nil
}

// Session stores, reads, or lists session ids.
// hasName false lists every member. hasSession false reads one id.
// An empty session string is refused by the caller before this runs.
func (s *Service) Session(ctx context.Context, conv, name string, hasName bool, session string, hasSession bool) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	if !hasName {
		if hasSession {
			return refused(ReasonNameRequired), nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		rows, err := s.store.Sessions(ctx, conv)
		if err != nil {
			return s.fail(err, "session")
		}
		if rows == nil {
			rows = []store.MemberSession{}
		}
		return Result{Outcome: OutcomeOK, Sessions: rows}, nil
	}
	if err := requireParticipant(name); err != nil {
		return asRefused(err)
	}
	if hasSession && session == "" {
		return refused(ReasonSessionEmpty), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	if hasSession {
		one, err := s.store.SetSession(ctx, conv, name, session)
		if err != nil {
			return s.fail(err, "session")
		}
		return Result{Outcome: OutcomeOK, Session: &one}, nil
	}
	one, err := s.store.Session(ctx, conv, name)
	if err != nil {
		return s.fail(err, "session")
	}
	return Result{Outcome: OutcomeOK, Session: &one}, nil
}

func (s *Service) now() string {
	return s.clock().UTC().Format(time.RFC3339Nano)
}

func memberWriteStatus(status string) bool {
	switch status {
	case store.MemberLaunched, store.MemberRunning, store.MemberExited, store.MemberTimedOut:
		return true
	default:
		return false
	}
}
