// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package bus is the hotseat listener.
//
// One process stores named conversations in SQLite and blocks callers until
// a matching message exists, a deadline passes, or the conversation is closed.
// It listens on one address until it is signalled. The default is loopback
// and requires no token. A hostname is resolved once. The socket is a loopback
// address only when every answer is loopback; any other socket requires the
// shared capability token on every request. It does not launch agents and it
// does not open connections to clients.
//
// # Protocol
//
// Every operation is an HTTP POST of one JSON object. A completed call
// responds with HTTP 200 and one JSON object. The outcome field is ok,
// timeout, closed, refused, or unavailable. Read that field. A dropped
// connection has no body and is not a timeout, a refusal, or unavailable.
// Unknown JSON fields are ignored. A loopback listener accepts a request
// with no token. Any other listener requires the token on every request.
// WriteToken sets that header. A missing or wrong token is refused and
// writes nothing. The token is not a sender name and is not stored.
//
//	POST /v1/create   {"name":"job"}
//	POST /v1/publish  {"conversation","from","to","body","idempotency_key"}
//	POST /v1/read     {"conversation","cursor","limit","name"?}
//	POST /v1/wait     {"conversation","cursor","name"?,"deadline"?}
//	POST /v1/close    {"conversation"}
//	POST /v1/list     {}
//
// to is an array. [] is empty. ["all"] is the single value all. Any other
// array is an ordered list of names. Order is part of the publish identity.
// The bus does not sort or deduplicate it. A JSON string "all" is refused.
//
// deadline is a Go duration such as "30s" or "500ms". Omit it, or send "",
// to wait until a match, a close, or a dropped connection. "0s" times out
// at once when nothing already matches. A stored match still returns ok.
//
// cursor 0 means the caller has seen no message. The bus does not store it.
// limit is the maximum number of messages read returns. Wait has no limit.
//
// A publish response is written only after the message is durable. The same
// idempotency key with the same from, to, and body returns the original
// message and already_stored true, including after the conversation is
// closed, and writes nothing. The same key with any of those three different
// is refused. A different name order in to is different content.
//
// An ok named wait returns every message after the cursor through the match,
// oldest first, and match_seq. Messages between the cursor and the match are
// included. An ok unnamed wait returns only the next message, and match_seq
// is that message. A message with an empty to matches only an unnamed wait.
// A named wait includes it when it sits between the cursor and the match.
// all wakes every named waiter except the sender.
//
// Timeout returns no messages and writes nothing. Closed on a named wait
// with no match returns every message after the cursor and no match_seq.
// Closed on an unnamed wait returns no messages. A match accepted before
// the close is outcome ok, not closed.
//
// refused names the broken rule in reason and writes nothing. unavailable
// means the change could not be made durable and writes nothing.
package bus
