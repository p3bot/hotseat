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
//	POST /create   {"name":"job","task"?,"ticket"?,"seat"?,"directory"?,"prompts"?,"custom"?,"roster"?}
//	POST /publish  {"conversation","from","to","body","txid","kind"}
//	POST /read     {"conversation","cursor","limit","name"?,"kinds"?}
//	POST /wait     {"conversation","cursor","name"?,"deadline"?,"kinds"?}
//	POST /close    {"conversation"}
//	POST /list     {}
//	POST /register {"conversation","name"}
//	POST /member   {"conversation","name","status"?,"pid"?}
//	POST /session  {"conversation","name"?,"session"?}
//	POST /members  {"conversation"}
//
// to is an array. [] is empty. ["all"] is the single value all. Any other
// array is an ordered list of names. Order is part of the publish identity.
// The bus does not sort or deduplicate it. A JSON string "all" is refused.
// kind is required and uses the same character rules as a name. The bus stores
// the name and does not check its meaning. kinds, when present, is an array
// of those names. Omit it and read and wait match without a kind filter.
// An empty array is refused.
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
// An ok create returns the conversation and its binding. Omitted binding
// fields are stored empty. prompts and roster keep the caller's order. A
// roster requires a seat that is one of the roster names. Empty roster and
// empty seat is a name-only create. A name that already exists returns that
// stored conversation and already_existed true, and writes nothing. Create
// does not insert member rows.
//
// A publish response is written only after the message is durable. It carries
// the message, including kind, and the conversation. An attempt is the
// conversation, the sender, and the transaction id. The bus requires that id
// and does not generate one. The same attempt with the same kind, to, and
// body returns the original message and already_stored true, including after
// the conversation is closed, and writes nothing. The same attempt with a
// different kind, to, or body is refused. A different name order in to is
// different content. Another sender using that transaction id stores a new
// message. Close sets status to closed and still accepts a new message. That
// publish reports status closed. Register, member, session, and members
// follow the same rules on a closed conversation.
//
// An ok named read returns messages addressed to that name whose kind is
// listed when kinds is set. The limit counts returned messages. An ok named
// wait returns every message after the cursor through the match, oldest
// first, and match_seq. With kinds, the match is the first addressed message
// of a listed kind. Messages of other kinds stay in the span. An ok unnamed
// wait returns only the next matching message, and match_seq is that
// message. A message with an empty to matches only an unnamed wait.
// A named wait includes it when it sits between the cursor and the match.
// all wakes every named waiter except the sender.
//
// register sets the registered time. The status becomes registered only when
// the row is launched. member inserts launched and updates running, exited,
// and timed-out. A pid without a status is refused and writes nothing. A call
// with neither returns the row. The member response has no session id.
// members returns name, launched time, registered time, and status, in roster
// order, with no session id and no pid. session stores and returns session
// ids for its caller. Those ids stay off read, wait, list, and members.
//
// Timeout returns no messages and writes nothing. Close does not end a wait.
//
// refused names the broken rule in reason and writes nothing. unavailable
// means the change could not be made durable and writes nothing.
package bus
