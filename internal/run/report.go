// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import "time"

// Reporter receives what a run reports for its caller to print. New and Continue call it from one goroutine, at most
// once per method, and never for a run that stopped before its chat was bound. A run that bound its chat calls
// either Unheld alone, or Header and then Result.
type Reporter interface {
	// Unheld reports a run that bound its chat and stopped before it held the lease.
	Unheld()
	// Header reports the chat once it and its lease are held.
	Header(Header)
	// Result reports how a run that held its lease ended.
	Result(Result)
}

// Header is the chat a run holds and the roster it runs with.
type Header struct {
	Chat string
	Task string
	// Ticket is empty for a chat bound to no ticket.
	Ticket string
	Seat   string
	// Members is the roster, as New was given it or as Continue read it from the chat.
	Members []Member
}

// Member is one roster entry.
type Member struct {
	Name   string
	Module string
	Model  string
}

// Result is how a run that held its lease ended.
type Result struct {
	// Stop is the stop result of the done the stop record names, or nil when there is no stop record.
	// Its Outcome is that done's, which a halt after the close does not change.
	Stop *StopResult
	// StopClass is the stopped by class of an error stop, or empty.
	StopClass string
	// Outcome is done, deadlock, unsettled, or error. It is done only for a run that returns nil.
	Outcome string
	// Elapsed is the run's duration, rounded to seconds.
	Elapsed time.Duration
}

type discardReporter struct{}

func (discardReporter) Unheld()       {}
func (discardReporter) Header(Header) {}
func (discardReporter) Result(Result) {}
