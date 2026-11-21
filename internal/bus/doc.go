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
//	POST /publish  {"conversation","from","to","body","txid","kind","re"?,"holder"?}
//	POST /read     {"conversation","cursor","limit","name"?,"kinds"?}
//	POST /wait     {"conversation","cursor","name"?,"deadline"?,"kinds"?}
//	POST /watch    {"cursor"?,"conversation"?,"rev"?,"limit"?,"deadline"?}
//	POST /close    {"conversation"}
//	POST /list     {}
//	POST /messages {"limit","before"?}
//	POST /register {"conversation","name"}
//	POST /member   {"conversation","name","status"?,"pid"?,"start"?,"holder"?}
//	POST /session  {"conversation","name"?,"session"?,"holder"?}
//	POST /members  {"conversation"}
//	POST /record   {"conversation","record",...,"holder"}
//	POST /records  {"conversation"}
//	POST /lease    {"conversation","lease","holder"?}
//
// to is an array. [] is empty. ["all"] is the single value all. Any other
// array is an ordered list of names. Order is part of the publish identity.
// The bus does not sort or deduplicate it. A JSON string "all" is refused.
// kind is required and uses the same character rules as a name. The bus stores
// the name and does not check its meaning. kinds, when present, is an array
// of those names. Omit it and read and wait match without a kind filter.
// An empty array is refused.
//
// re is the seq of an earlier message of the same conversation that this
// publish responds to. Omit it, or send null, to respond to nothing. A value
// that is not an integer greater than zero is refused. A seq of another
// conversation, a seq that does not exist, and the publish's own seq are
// refused and write nothing. Every message object from publish, read, wait,
// watch, and /messages has re, the integer or null.
//
// deadline is a Go duration such as "30s" or "500ms", or an RFC3339 end time.
// Omit it, or send "", to wait until a match or a dropped connection.
// A duration is measured from arrival. An end time is absolute on the bus
// clock, and a time already past times out at once when nothing matches.
// "0s" does the same. A stored match still returns ok. A retry sends the
// same end time, so time already spent stays spent.
//
// seq is assigned at commit. It increases across the whole store and within
// each conversation, with gaps. A cursor is a seq the caller has seen.
// cursor 0 means the caller has seen no message. The bus does not store it.
// limit is the maximum number of messages read returns. Wait has no limit.
// /messages uses limit the same way and returns those newest messages
// across every conversation, highest seq first. Each message includes
// conversation. With before, only messages whose seq is less than before
// are returned, so the lowest seq of one result is the next before. A
// before less than 1 is refused.
//
// /watch returns messages with seq greater than cursor, oldest first, in
// conversation or every conversation, at most limit, each with
// conversation. limit is required with cursor. A conversation name with no
// conversation matches no message and is not refused. more is true when
// further matching messages follow the last one returned. rev is the current
// change counter. Omit cursor and the watch returns no messages. A watch
// returns at once when a matching message exists, when rev is omitted, or
// when rev differs from the counter. Otherwise it blocks until one of those
// holds, the deadline passes, the connection drops, or the process stops.
//
// rev lives in process memory and starts at a random value below 2^53, so a
// restarted process does not return a value a caller still holds. Compare it
// for equality only. It increases on a create that inserted, on a close that
// changed the status, and on a stored publish to a conversation other than
// the one holding the newest message, which is the only way a publish changes
// list order. An idempotent publish and a close of a closed conversation
// change nothing and wake no watch.
//
// /list returns every conversation. The one whose newest message has the
// highest seq is first. Conversations with no message follow, by name.
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
// and does not generate one. The same attempt with the same kind, to, body,
// and re returns the original message and already_stored true, including
// after the conversation is closed, and writes nothing. The same attempt with
// a different kind, to, body, or re is refused, including re present against
// absent. A different name order in to is different content. Another sender
// using that transaction id stores a new message. Close sets status to closed
// and still accepts a new message. That publish reports status closed.
// Register, member, session, and members follow the same rules on a closed
// conversation.
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
// the row is launched. It returns the public row: name, status, launched time,
// and registered time, with no pid and no start. member inserts launched and
// updates running, exited, and timed-out. A pid without a status, or a start
// without a pid, is refused and writes nothing. start is the caller's opaque
// start token for that pid, stored beside it and replaced or cleared with it.
// A call with neither status nor pid returns the row, including pid and start.
// The member response has no session id. members returns name, launched time,
// registered time, and status, in roster order, with no session id, no pid,
// and no start. session stores and returns session ids for its caller. Those
// ids stay off read, wait, list, and members.
//
// record writes one of hotseat run's decision records and records reads them.
// Neither has a command. Records are absent from read, wait, watch, list,
// members, and the page. Neither route changes rev or wakes a wait or watch.
// The log holds each call and its outcome and no record content. record
// selects the write and its fields, each seq an integer greater than zero:
//
//	set       {"turn","findings"}               findings may be []
//	grouping  {"turn","drops":[{"finding","kept"}]}
//	settle    {"finding","proposal","proposal_kind"}
//	fix       {"finding","fixed"}
//	halt      {"finding"}
//	stop      {"done"}
//
// A set with exactly one finding also writes its grouping with no drops, and
// returns it beside the set. The result is the stored record in the shape
// records uses, and already_stored. after is the conversation's highest seq
// when the record commits. The bus assigns it and ignores one sent. A fix
// carries settled_after, its settle's after, and has no after. A stop holds
// only done, the seq of the done message that stopped the run. A write that
// matches the stored record on its key, ignoring after, returns it and
// already_stored true and writes nothing. Findings and drops compare as sets.
// A write that differs on the same key is refused with the transaction id
// conflict reason. The key is the turn for a set and a grouping, the finding
// for a settle and a fix, and the conversation for a halt and a stop. A
// repeated seq in findings and a repeated finding in drops are refused. Every
// reference, kind, CHECK, or uniqueness failure is one reason: a missing
// message or record, a message of the wrong kind such as a stop whose seq is
// not a done, a finding in another set, a drop that keeps itself or names
// another set, a proposal_kind that differs from the stored message, and a
// fixed used by another fix. Three rules have their own reasons: a kept
// finding is not itself dropped, a settle or a halt names a kept finding of a
// set that has a grouping, and a fix belongs to a settle whose proposal is not
// a dismiss. records returns sets, groupings, settles, fixes, halts, and
// stops, each in key order, and an empty list as [].
//
// lease is the conductor lease, a claim that one conductor drives a
// conversation. It knows nothing about run, kinds, or records. lease selects
// acquire, renew, release, or get, the conversation must exist, and every
// selector but get requires holder, a name. acquire succeeds when no live
// lease exists or this holder has it, and is otherwise refused with
// remaining, the whole seconds left, rounded up. renew extends this holder's
// live lease and never creates one. release removes this holder's lease, and
// releasing one it does not hold changes nothing. get returns held, and when
// held, since and expires, and never the holder. An ok acquire or renew
// returns ttl in seconds. LeaseTTL is the time to live, measured on the bus's
// monotonic clock and checked on each call. Leases live in process memory,
// never touch the store, change no rev, and wake no wait or watch. A closed
// conversation can still be leased. The log holds each call and its selector
// and never the holder.
//
// A record write, a member write with a status, and a session write with a
// session are conductor writes. Each requires holder, and the holder must hold
// the conversation's live lease or the write is refused and stores nothing,
// including a replay of a stored record. A publish that carries holder is
// fenced the same way, and one without it is not checked. The lease check and
// the write happen under one lock. Reads, register, and close ignore the lease.
//
// Timeout returns no messages and writes nothing. Close does not end a wait.
//
// refused names the broken rule in reason and writes nothing. unavailable
// means the change could not be made durable and writes nothing.
package bus
