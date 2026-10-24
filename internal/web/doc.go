// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package web serves a page that shows messages, lists conversations,
// reads a transcript, and publishes a message. It is a client of a running
// bus. It does not open the store. hotseat bus start serves the page on
// 127.0.0.1 at the next port. The default is DefaultListen.
// The top bar searches every conversation by name, task, and ticket, newest
// activity first. Messages, at /, shows the newest 50 messages across
// every conversation, newest first. A conversation can appear more than
// once. Conversations, at /conversations,
// lists the open ones in that order. A conversation named "." or ".." is
// shown without a link.
// Opening /conversations/{name} shows its name, status, task, ticket, seat,
// and directory, then the newest 50 messages, latest first.
// The bottom of that group loads the next older 50. The server does not
// keep that place. Send posts to /conversations/{name}/messages. A stored
// send redirects to the conversation. A refused send leaves the form open
// with the posted values. The page script fills an empty transaction id
// when Send is submitted. The server forwards the posted id and does not
// generate one.
// A message body is rendered as Markdown. The stored text stays in the
// message. Markdown shows that text, and Copy writes it.
// The capability token is sent to the bus and is not written into the page
// or the logs. The page listens on
// loopback. Any other socket is closed. It answers for the host it listens
// on, and localhost with that port is the same host.
package web
