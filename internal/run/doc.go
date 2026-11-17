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
// done.
//
// run keeps its decisions as decision records in the store: a set when a set
// closes, a grouping of its duplicates, a settle, a fix, and a halt. Records
// are facts keyed by message seq and hold no phase, mode, or cursor. run reads
// them once at start and keeps each one a write returns, and a continuation
// resumes from them and the transcript. The one message run publishes about a
// decision is settled, to owner, because no participant states a settle.
// Nothing reads it as state.
//
// Every prompt names the exact --re for the kind it asks for. A message whose
// re is present and is not the message run expects is rejected under its
// kind's class. A missing re is judged by the transcript windows. A protocol
// error carries the member and class in its body and the message that caused
// it in re. An error a continuation can meet again takes a transaction id
// derived from its fact, so the bus returns the stored error instead of a
// second copy.
package run
