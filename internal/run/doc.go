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
// reports the stop result from the transcript. The transcript is the only
// durable state: run records the end of each set, each settle, and each
// accepted deadlock as marker messages, and a continuation restarts the open
// discussion from them.
package run
