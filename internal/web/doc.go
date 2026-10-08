// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package web serves a page that lists conversations, reads a transcript,
// and publishes a message. It is a client of a running bus. It does not
// open the store. The cursor comes from the page on each request and is not
// kept. The page script fills an empty transaction id when Publish is
// submitted. The server forwards the posted id and does not generate one.
// The capability token is sent to the bus and is not written into the page
// or the logs. The page listens on
// loopback. Any other socket is closed. It answers for the host it listens
// on, and localhost with that port is the same host.
package web
