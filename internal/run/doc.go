// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package run drives one conversation on a bus that is already listening.
// It shells out to start and tk. It does not start the bus and it does not
// open the database.
//
// A finding a member publishes during a turn set opens a discussion. run
// schedules the speakers, settles a finding only when every live member but
// the proposer agrees, launches the finder to apply a settled change, and
// reports the stop result from the messages and records before the stopping
// done. The stopping done is the seat's, or the backup's while the seat is
// timed-out. Any other done stored before the stop is stored once as a bad
// done error and never stops the run afterwards.
//
// run keeps its decisions as decision records in the store: a set when a set
// closes, a grouping of its duplicates, a settle, a fix, a halt, and a stop
// naming the stopping done, written before the close. Records are facts
// keyed by message seq and hold no phase, mode, or cursor. run reads them once
// at start and keeps each one a write returns, and a continuation resumes from
// them and the transcript. The one message run publishes about a decision is
// settled, to owner, because no participant states a settle. Nothing reads it
// as state.
//
// run holds the conversation's conductor lease from before it acts on the
// conversation until it exits. It mints a random holder at start, renews the
// lease in the background, and sends the holder on every record, member
// status, and session write and every publish. A refused renewal, or a write
// refused for want of the lease, stops the run without killing a member: it
// waits for the members it launched, stops watching the members it adopted,
// writes nothing more, and leaves every member for the next holder to adopt.
// A continuation of a conversation another conductor holds waits once for
// that lease to run out.
//
// A run that held its lease and stops on an error publishes one error to
// owner whose body is its stop class, stopped by protocol, refusal, command,
// bus, lease, signal, or run, before it waits for any member or releases the
// lease. Each cause is typed where it enters run, and the first one is the
// stop. Every error after a signal is its fallout, so that stop is the signal.
//
// A member row stores the pid and the host's start token for that process,
// read right after the launch. A continuation adopts a live process only when
// its token equals the stored one, so a reused pid is never adopted and no
// host time is compared with a bus time.
//
// Only the holder enforces the process limit. A run that lost its lease or
// died enforces none, and a continuation gives each process it adopts the
// full limit, counted from adoption.
//
// Every prompt names the exact --re for the kind it asks for. A message whose
// re is present and is not the message run expects is rejected under its
// kind's class. A missing re is judged by the transcript windows. A protocol
// error carries the member and class in its body and the message that caused
// it in re. An error a continuation can meet again takes a transaction id
// derived from its fact, so the bus returns the stored error instead of a
// second copy.
//
// ReadStatus is the read-only side for hotseat status. It builds the same
// ledger from bus reads with the same builder run uses, and reports the stop
// result of a recorded done by the same rule, so the two cannot disagree.
package run
