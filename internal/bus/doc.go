// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package bus is the hotseat listener.
//
// One process stores named conversations in SQLite and blocks callers until
// a matching message exists or a deadline passes.
// It listens on one address until it is signalled. The default is loopback
// and requires no token. A hostname is resolved once. The socket is a loopback
// address only when every answer is loopback; any other socket requires the
// shared capability token on every operation. GET /health is not an operation
// and answers pass or fail with no token, on every address. The process does
// not launch agents and it does not open connections to clients.
//
// # Protocol
//
// Every operation is an HTTP POST of one JSON object. A completed call
// responds with HTTP 200 and one JSON object. The outcome field is ok,
// timeout, refused, or unavailable. Read that field. A dropped
// connection has no body and is not a timeout, a refusal, or unavailable.
// Unknown JSON fields are ignored. A loopback listener accepts a request
// with no token. Any other listener requires the token on every operation.
// WriteToken sets that header. A missing or wrong token is refused and
// writes nothing. The token is not a sender name and is not stored.
// GET /health is not an operation. It reads the schema this process serves
// and answers pass or fail with no token, on every address.
//
//	POST /create   {"name":"job"}
//	POST /publish  {"conversation","from","to","body","txid"}
//	POST /read     {"conversation","cursor","limit","name"?}
//	POST /wait     {"conversation","cursor","name"?,"deadline"?}
//	POST /close    {"conversation"}
//	POST /list     {}
//
// to is an array. [] is empty. ["all"] is the single value all. Any other
// array is an ordered list of names. Order is part of the publish identity.
// The bus does not sort or deduplicate it. A JSON string "all" is refused.
//
// deadline is a Go duration such as "30s" or "500ms", or an RFC3339 end time.
// Omit it, or send "", to wait until a match or a dropped connection.
// A duration is measured from arrival. An end time is absolute on the bus
// clock, and a time already past times out at once when nothing matches.
// "0s" does the same. A stored match still returns ok. A retry sends the
// same end time, so time already spent stays spent.
//
// cursor 0 means the caller has seen no message. The bus does not store it.
// limit is the maximum number of messages read returns. Wait has no limit.
//
// An ok create returns the conversation. A name that already exists returns
// that conversation, its status, and already_existed true, and writes nothing.
//
// A publish response is written only after the message is durable. It carries
// the message and the conversation name and status. An attempt is the
// conversation, the sender, and the transaction id. The bus requires that id
// and does not generate one. The same attempt with the same to and body
// returns the original message and already_stored true, including after the
// conversation is closed, and writes nothing. The same attempt with a
// different to or body is refused. A different name order in to is different
// content. Another sender using that transaction id stores a new message.
// Close sets status to closed and still accepts a new message. That publish
// reports status closed.
//
// An ok named wait returns every message after the cursor through the match,
// oldest first, and match_seq. Messages between the cursor and the match are
// included. An ok unnamed wait returns only the next message, and match_seq
// is that message. A message with an empty to matches only an unnamed wait.
// A named wait includes it when it sits between the cursor and the match.
// all wakes every named waiter except the sender.
//
// Timeout returns no messages and writes nothing. Close does not end a wait.
//
// refused names the broken rule in reason and writes nothing. unavailable
// means the change could not be made durable and writes nothing.
package bus
