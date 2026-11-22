// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// States of a conversation as Status reports it.
const (
	StateRunning = "running"
	StateIdle    = "idle"
	StateClosed  = "closed"
)

// Status is what the bus says about one conversation, read by ReadStatus.
// It holds no session id, pid, start token, lease holder, token, or decision record.
type Status struct {
	// Binding is the conversation as /list returned it.
	Binding client.Conversation
	// State is StateRunning while a conductor holds the lease, else StateClosed once the conversation is closed,
	// else StateIdle. It is a fact, not a judgement of how a run ended.
	State string
	// Since and Expires are the lease's times, set only while State is StateRunning.
	Since   string
	Expires string
	// Members are the stored member rows, as stored.
	Members []MemberStatus
	// Newest is the highest seq of any kind, or zero for an empty transcript.
	Newest int64
	// Findings, Settled, Fixed, and Dropped count from the ledger over every message and record.
	Findings int
	Settled  int
	Fixed    int
	Dropped  int
	// Discussing is the first unresolved kept finding whose discussion has started, set only while State is
	// StateRunning and no halt names it.
	Discussing *int64
	// Stop is the stop result of the done the stop record names, set only when a stop record exists.
	Stop *StopResult
	// StopError is the newest stop error after the recorded done, or the newest one when there is no stop record.
	StopError *StopError
}

// MemberStatus is one member row's name and stored status.
type MemberStatus struct {
	Name   string
	Status string
}

// StopResult is the stop result of a recorded done, from the one rule run reports its own stop result by.
type StopResult struct {
	Done      int64
	Unsettled []int64
	Unfixed   []int64
	// Outcome is done, deadlock, or unsettled.
	Outcome string
}

// StopError is a stop error: its class and its seq.
type StopError struct {
	Class string
	Seq   int64
}

// ReadStatus reads one conversation from a running bus and interprets it. It makes only read calls, in the order
// lease get, list, members, records, and read to the end, and writes nothing. A call that answers other than ok,
// including a connection failure, returns an error that FailedCall reports.
func ReadStatus(ctx context.Context, address, token, conversation string) (Status, error) {
	var call caller = func(ctx context.Context, op string, body any) (client.Result, error) {
		res, err := client.Do(ctx, address, op, token, body)
		if err != nil {
			return res, err
		}
		return res, outcomeErr(op, body, res)
	}
	lease, err := call(ctx, "lease", client.LeaseRequest{Conversation: conversation, Lease: bus.LeaseGet})
	if err != nil {
		return Status{}, err
	}
	if lease.Held == nil {
		return Status{}, errors.New("lease get: ok with no held")
	}
	list, err := call(ctx, "list", struct{}{})
	if err != nil {
		return Status{}, err
	}
	binding, ok := pickBinding(list, conversation)
	if !ok {
		return Status{}, fmt.Errorf("list: no conversation %s", conversation)
	}
	rows, err := readMembers(ctx, call, conversation)
	if err != nil {
		return Status{}, err
	}
	// Records first: a record's references and after are stored seqs, so the transcript read next holds them all.
	recRes, err := call(ctx, "records", client.RecordsRequest{Conversation: conversation})
	if err != nil {
		return Status{}, err
	}
	msgs, _, err := readAll(ctx, call, conversation, 0, nil)
	if err != nil {
		return Status{}, err
	}
	var recs records
	recs.add(recRes)

	st := Status{Binding: binding, State: StateIdle}
	switch {
	case *lease.Held:
		st.State = StateRunning
		st.Since, st.Expires = lease.Since, lease.Expires
	case binding.Status == bus.ConversationClosed:
		st.State = StateClosed
	}
	for _, m := range rows {
		st.Members = append(st.Members, MemberStatus{Name: m.Name, Status: m.Status})
	}
	for _, m := range msgs {
		st.Newest = max(st.Newest, m.Seq)
	}
	l := buildLedger(binding.Roster, binding.Seat, rows, msgs, recs)
	st.countFindings(l)
	if seq, ok := l.underWay(); ok && st.State == StateRunning {
		st.Discussing = &seq
	}
	stop, stopped := recs.stopped()
	if stopped {
		st.Stop = l.stopResultOf(stop.Done)
	}
	for _, m := range msgs {
		if isStopError(m) && (!stopped || m.Seq > stop.Done) && (st.StopError == nil || m.Seq > st.StopError.Seq) {
			st.StopError = &StopError{Class: m.Body, Seq: m.Seq}
		}
	}
	return st, nil
}

// countFindings fills the finding counts from the whole ledger. A dismissal counts as settled and is never fixed.
func (st *Status) countFindings(l ledger) {
	for _, f := range l.findings() {
		st.Findings++
		if s, settled := l.settledOf(f.Seq); settled {
			st.Settled++
			if _, fixed := l.fixOf(f.Seq); changes(s) && fixed {
				st.Fixed++
			}
		}
	}
	st.Dropped = len(l.droppedAll())
}

func pickBinding(res client.Result, name string) (client.Conversation, bool) {
	if res.Conversations == nil {
		return client.Conversation{}, false
	}
	for _, conv := range *res.Conversations {
		if conv.Name == name {
			return conv, true
		}
	}
	return client.Conversation{}, false
}

// FailedCall is the outcome and reason of the bus call err reports, when err is a call that answered other than ok.
func FailedCall(err error) (client.Result, bool) {
	var be *busError
	if !errors.As(err, &be) {
		return client.Result{}, false
	}
	return client.Result{Outcome: be.outcome, Reason: be.reason}, true
}
