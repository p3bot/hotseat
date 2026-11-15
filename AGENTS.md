# hotseat

## Purpose

hotseat takes the owner out of the loop on work they would otherwise drive turn by turn: reading, choosing between options, discussing changes, and applying them. Three or four agents, from different vendors where possible, each prompted to have agency, work through the task together in one conversation and reach the best outcome they can agree on. The owner sets the task and is reached only through `ask` and `done`.

A task module defines the work. Reviews are the commonest: ticket triage, ticket review, and pre-commit review. hotseat also runs design discussions, ticket decomposition, code reviews, deployments, and single-member work such as implementation. Parallel members cover more ground in one sweep than one agent run several times, and members from different vendors catch what one model misses.

The parts, in the order they were built:

- The bus is a durable, ordered log of conversations. It stores speech and knows no kind's meaning. It was built first and stays general.
- A member is an agent process. `run` starts it for one step, it publishes and exits, and a later step resumes it by session id. Members do not wait on the bus.
- `run` is the conductor: a deterministic engine that drives the task's parallel and serial turns, and stops and continues from what the bus holds. For a task whose members publish `finding`s, it also runs the finding protocol: one finding discussed at a time, settled by agreement, fixed by the finder, then a satisfaction pass.
- A chief session conducts runs and answers `ask`. `attend` and the page are how the owner watches.

Rules that follow from the purpose:

- `run` enforces mechanics: who launches, in what order, when the run stops, and how it resumes. Members decide judgement: what the work needs, whether a finding holds, and which option wins.
- New kinds of work arrive as task modules in `library/`, not as code in the bus. `run` gains a mechanism only when several tasks need the same structure, as the finding protocol serves every review.
- The conversation is speech. After a run, it shows a reader who was not there how the work went. `run`'s own bookkeeping is not speech.
- A message is identified by its `seq`. A body is never only an id.
- Findings are discussed one at a time because a fix can make a later finding obsolete.

Note: `run` still publishes its finding markers (`discuss`, `issues`, `settled`, `halted`) as messages. hotseat-vn53 moves them out of the transcript.

## Contract

`README.md`, each command's `--help`, and the package doc comments state what the program does. A behaviour they state changes in the same edit as the code. This file holds only what working on the code needs beyond them. `.agents/roles/default.md` is the start role for this module and carries the same constraints.

## Layout

Module `github.com/p3bot/hotseat`.

| Path | Owns |
| --- | --- |
| `cmd/hotseat` | `main`. Runs `cli.Execute`, then the signal code, then a reported failure, then any other error on stderr |
| `internal/bus` | Listener, routes, rules, token, `GET /health`. Opens no connections to clients |
| `internal/store` | `hotseat.db`, schema, `hotseat.db.lock`. The only writer |
| `internal/client` | One HTTP call to a running bus |
| `internal/cli` | Cobra tree, detached bus, text results |
| `internal/run` | `hotseat run`. Shells out to `start` and `tk` |
| `internal/run/fake` | Fake agent binary for the run tests |
| `internal/web` | Loopback page |

Only `internal/bus` holds a store. `client`, `run`, `attend`, and `web` call a bus that is already up. The bus launches no agents. The store directory holds `hotseat.db`, `hotseat.db.lock`, `hotseat.pid` (pid and listen address), and `hotseat.log`. Hidden `bus serve` runs only as a session leader with the readiness pipe on fd 3, and otherwise returns `bus serve is internal`.

## Checks

Go is the version on the `go` line in `go.mod`. This machine defaults to `CGO_ENABLED=1`, so every build and test sets `CGO_ENABLED=0`. The driver is `modernc.org/sqlite`, and a cgo driver is out. `go test -race` needs cgo, so it is not a check.

| Check | Command |
| --- | --- |
| Build | `CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat` |
| Test | `CGO_ENABLED=0 go test ./...` |
| Format | `gofmt -l .` prints nothing |
| Lint | `golangci-lint run` |

`.golangci.yml` is v2 with errcheck, govet, ineffassign, staticcheck, and unused. errcheck ignores `fmt.Fprint*` only. There is no issue cap.

Note: lint is not clean on main. The existing findings are unchecked `Close` errors and staticcheck `QF1006` in `internal/cli`. Leave them, and add none.

`cli.Version` is `dev` unless `-ldflags "-X main.Version=..."` sets it. The repo-root `hotseat` binary and `*.db*` files are gitignored.

## Invariants

- One writer. Seq order is commit order only because of it. The flock is on `hotseat.db.lock`, not the database inode, because on macOS flock shares SQLite's lock table and closing any descriptor drops every lock.
- DSN: `busy_timeout=0` (another holder fails this process instead of stalling it), `journal_mode=WAL`, `locking_mode=EXCLUSIVE` (checked after open), `foreign_keys=1`, `synchronous=FULL` (commit returns after the write is durable), `_txlock=immediate`. `SetMaxOpenConns(1)`.
- `Open` resolves the directory to an absolute path first, chmods it `0700`, refuses symlinks for the database, wal, shm, and lock, and opens the lock with `O_NOFOLLOW` and mode `0600`.
- Tables are `STRICT`. `meta.schema` is `store.SchemaVersion`, `1`. There is no migration. `Open` returns `ErrSchema` for a shape this binary did not create and leaves the file. A schema change means deleting the database.
- A write that changes nothing (idempotent publish, close of a closed conversation) returns `errNoChange`, so the transaction rolls back.
- Store sentinels are `ErrNotFound`, `ErrConflict`, `ErrHeld`, `ErrSchema`, `ErrNotRoster`, `ErrNotMember`, and `ErrLaunched`. Wrap with `%w` when the caller branches on the cause.
- Refusal text is a `bus.Reason*` constant. A new reason is a constant beside the others, plus a test whose name states the rule.
- Session ids, pids, and the token stay off read, wait, list, member list, the page, logs, status lines, and prompts.
- `from` is caller-supplied. The token does not authenticate the sender.
- The page listens on loopback only, because it holds the bus token.
- CLI stdout is `key: value`, and a value with a newline is `key <<N` and then those bytes. Usage errors go to stderr with no stdout. Commands set `SilenceUsage` and `SilenceErrors`. SIGINT and SIGTERM exit `128+signal`, read before the error path.

## Runner

The designs are `hotseat-r7gj` (runner) and `hotseat-b225` (finding discussion). Read one with `tk design get <id> --content`. The code is current.

Task modules are `tasks:hotseat/ticket/triage`, `ticket/review`, `ticket/implement`, `pre-commit/review`, and `workflow/implement`, which is the chief's workflow. Member-facing text is the roles `hotseat/member` and `hotseat/chief` and the context `hotseat/bus`. A change to that text lands in `library/` first.

| File | Owns |
| --- | --- |
| `loop.go` | Transcript watch, turns, stop on `done` |
| `launch.go` | Member processes, launch cap, handoff |
| `state.go` | Member rows, leftovers on resume, backup, prompts |
| `findings.go` | Finding protocol: launches, judging, prompts |
| `discussion.go` | Live state of one finding. No bus or process |
| `ledger.go` | Pure reading of the transcript |

- A stance is judged only by the live launch window it arrived in. A continuation never replays stances and restarts the open discussion from its finding. History is the messages stored before the continuation started, plus each leftover's messages up to its recorded exit.
- A protocol error body is `<member>\n<class>\n<seq>`. Each class is a `class*` constant in `run.go`. `run.ErrUnresolved` maps to the CLI's reported failure.
- Settled, do not reopen:
  - the roster is `name=module:model`, and there is no `HOTSEAT_MEMBERS`;
  - `mode` is the first line of a turn body, and the database has no phase, mode, or cursor column;
  - member rows replace a registration ack;
  - `run.DoneGrace` has no flag;
  - agy has no module, and `herdr` is not a dependency.
- In `internal/web`, `kindMessages`, `kindList`, and `kindConversation` are page modes, not the message field.

## Code

Every Go file starts with this header:

```go
// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
```

Dependencies stay the standard library, cobra, `golang.org/x/sys`, `modernc.org/sqlite`, and `github.com/yuin/goldmark`. `github.com/dop251/goja` runs the page script in `internal/web/page_test.go` only.

Put new code in the package that already owns the behaviour. An exported doc comment states the contract. Any further comment is one line for a non-obvious WHY, with no ticket ids, PR numbers, or bare TODO. A bad request, a conflict, or a failed write returns an error.

## Tests

`cmd/hotseat` has no tests. CLI cases build `./cmd/hotseat` with `CGO_ENABLED=0` into a temp binary. Name each test for its rule (`TestNonLoopbackDoesNotListenOrCreateStore`).

Do not add `t.Parallel`. Cases bind ports, take the store lock, and spawn processes, and several bind `127.0.0.1:4727`. Run `hotseat bus stop` before the suite.

<<<<<<< Updated upstream
`internal/cli` `TestMain` treats `HOTSEAT_FAKE_SERVE=1` as a fake listener on `HOTSEAT_FAKE_LISTEN`, for the `unshare` network-namespace case, which skips when `unshare` is missing. Leave both unset.
=======
Publish requires `kind`. It uses the participant name grammar. The vocabulary is open. Publish is acknowledged only after a durable commit. The attempt is the conversation, the sender, and `txid`. The bus requires that id and does not generate one. `kind` and `re` are part of the content, with `to` and body. The same attempt with the same kind, the same `to`, the same body, and the same `re` returns the original message and `already_stored`, including after close. A different kind, `to`, body, or `re` is refused with `ReasonKeyConflict`, including `re` present against absent. `to` order is content. The bus does not sort or dedupe it. Another sender with that `txid` stores a new message.

Publish takes an optional `re`, the `seq` of an earlier message of the same conversation that it responds to. Absent and `null` respond to nothing. A value that is not an integer greater than zero, including a string or a fraction, is `ReasonReRange`, refused by the bus before the store. `pid` uses the same parser with `ReasonPID`. A `seq` of another conversation, a missing `seq`, and the publish's own `seq` are `ReasonReTarget` and write nothing. The store returns `store.ErrReplyTarget` from the extended result code of the foreign key or check failure. Every message object from publish, read, wait, watch, and `/messages` has `re`, the integer or `null`. `hotseat publish --re <seq>` sends it, and the text result prints `re: <seq>` after `kind`, with no line when it is absent. There is no read filtered by `re`.
>>>>>>> Stashed changes

`TestReadmeDescribesDetachedBus` requires each substring below in `README.md`. The last one includes its backticks.

```
hotseat bus start
hotseat bus stop
hotseat bus status
hotseat.log
$XDG_DATA_HOME/hotseat
text result
body <<5
GET /health
does not take `--store`
http://127.0.0.1:4728
```

<<<<<<< Updated upstream
It fails when the README contains `prints one JSON object`, or a line whose trimmed text is `hotseat bus`.
=======
The test fails when the README contains `prints one JSON object`, or a line whose trimmed text is `hotseat bus`.

## Store

`Open` resolves the directory to an absolute path before it creates anything. It chmods the directory to `0700`, including a directory that already existed. Database, wal, shm, and lock symlinks are refused and left unchanged. The lock is opened with `O_NOFOLLOW` and mode `0600`.

The flock stays on `hotseat.db.lock`. It does not share the database inode. On macOS, flock shares SQLite's lock table, and closing any descriptor on the database file drops every lock on it.

DSN pragmas: `busy_timeout=0`, `journal_mode=WAL`, `locking_mode=EXCLUSIVE`, `foreign_keys=1`, `synchronous=FULL`, `_txlock=immediate`. `busy_timeout` 0 makes another holder fail this process instead of stalling it. `EXCLUSIVE` is checked after open. `FULL` means commit returns only after the write is durable. `SetMaxOpenConns(1)`.

Tables are `STRICT`. `meta.schema` is `store.SchemaVersion`, the string `1`. Statements are `CREATE TABLE IF NOT EXISTS`. `messages.seq` is `INTEGER PRIMARY KEY AUTOINCREMENT`, so a seq is never issued twice. `messages.re` is a nullable `INTEGER` with `CHECK (re < seq)` and `FOREIGN KEY (conversation, re) REFERENCES messages (conversation, seq)`. The foreign key alone accepts a self-reply, because SQLite checks it after the row exists. The check refuses it. `UNIQUE (conversation, seq)` is that parent key and serves transcript reads and list. `UNIQUE (conversation, seq, kind)` is the parent key for kind checks. There is no `messages_conversation_seq`. Seq order is commit order only because the store has one writer.

`Open` returns `store.ErrSchema` for a recorded version other than `1`, and for a version-`1` file that lacks `kind`, `re`, the binding columns, or the member table, whose `messages` primary key is not `seq` alone (read from `pragma_table_info`), or that lacks a non-partial unique index on exactly `(conversation, seq)` or on exactly `(conversation, seq, kind)` (read from `pragma_index_list` and `pragma_index_info`, matched by ordered columns, since an unnamed `UNIQUE` gets a generated name). A database created before `re` is refused. It does not alter the file and it does not serve it. There is no migration. Delete a database this binary already created before the new process opens it.

Idempotent publish, and close of an already-closed conversation, return the unexported `errNoChange` so the transaction rolls back instead of committing an empty write.

Sentinels: `ErrNotFound`, `ErrConflict`, `ErrHeld`, `ErrSchema`, `ErrNotRoster`, `ErrNotMember`, `ErrLaunched`, `ErrReplyTarget`. Wrap with `%w` when the caller branches on the cause.

## Access

Loopback accepts every operation with no token. Passing `--token-file` on loopback leaves the check off. Any other socket requires the file. A missing or empty file does not listen, and no database is created. A failed bind creates no database.

`ResolveListen` resolves a hostname once. One non-loopback answer selects one non-loopback address, so the socket is the listener that requires a token. IPv4 wins inside that set. An empty host is remote. `127.0.0.1` and `::1` are different addresses.

The header is `Authorization: Bearer` plus the token. `bus.WriteToken` sets it. A missing token is `token is required`. A wrong token is `token does not match`. Nothing is written. The token is not a flag, not a sender, and not stored. Logs, the page, and prompts do not contain it.

`hotseat bus start` serves the page on `127.0.0.1` at the bus port plus one. The default is `127.0.0.1:4728`. `hotseat web` serves the same page for a bus that is already running. The page default is `127.0.0.1:4728`, the address `bus start` already binds, so a further page passes another loopback listen address. It listens on loopback only. Any other socket is closed. The page holds the bus token, so a public socket would hand that token to the network. The request host is checked before a bus call. On loopback, `localhost` with the same port is that host. An empty listen host refuses every request. The page shows messages, lists conversations, reads, and publishes. The top bar searches every conversation by name, task, and ticket, in the list order. Messages, at `/`, shows the newest 50 messages across every conversation, newest first. A conversation can appear more than once. Reaching the bottom of that group loads the next older 50. Conversations, at `/conversations`, shows an Open section and then a Closed section. Each keeps that order and shows the task, ticket, and seat. An empty section stays on the page. A conversation named `.` or `..` is shown without a link. Opening `/conversations/{name}` shows its name, status, task, ticket, seat, and directory, then the newest 50 messages, latest first. Reaching the bottom of that group loads the next older 50. Loaded groups stay on the page. The page does not ask for a cursor or a limit, and it does not keep that place. Send posts to `/conversations/{name}/messages`. A stored send redirects to the conversation. The form sends `kind`, and the transcript shows it. Send does not take `re`. A message with `re` shows it in the foot as a link to `/conversations/{name}?before=<re+1>#msg-<re>`, so the referenced message is the top of that group, including one older than the newest 50. Each message row has id `msg-<seq>`. A conversation named `.` or `..` shows `re` as text. A transcript served with `before` is a history view. Its top links to the newest group, which is the live view. A message body is rendered as Markdown. The stored text stays in the message. Markdown shows that text, and Copy writes it. A refused send leaves the form open with the posted values. The page does not create, wait, close, or manage members. `GET /events` is a Server-Sent Events stream for one view. With no view parameter it follows Messages, `conversation=<name>` follows that transcript, `view=list` follows Conversations, and `conversation=<name>&view=history` follows a history view. It starts after query `after=<seq>` or the `Last-Event-ID` header. When both are present, the header wins. `view=list` takes neither. A history stream calls `/watch` with no `cursor`, waiting on `rev` alone, sends `conversations` and `meta`, and never sends `message` or `reset`. It ignores `Last-Event-ID`. A cursor stream there would see the later messages and reset the tab off the referenced message. Each stream is its own client of the bus and calls `/watch`, not `/wait`. It sends `message` with `id: <seq>` for each message of its view, `conversations` with the Open and Closed sections and the search index on every view, and, on a transcript, `meta` with the side panel at stream start and when it changes. A view that missed more than 50 of its messages gets `reset`, and the stream ends. A quiet stream sends a comment every 25s. A malformed position or view, `view=list` with a conversation, `view=history` without a conversation or with `after`, and a conversation name the bus refuses are answered `400` with a plain-text reason. A bus that is away ends a `200` stream, so the browser reconnects. Event data is HTML from the same template blocks as the pages. The stream position comes from the tab and is not stored. Each visible tab holds one stream for its view and updates in place, except that a history view shows no new message and never reloads. A new message appears at the top of Messages or its transcript with a brief highlight, and nothing is trimmed. Conversations, the search index, and the side panel follow the list. The open Send form, Markdown toggles, and the search box are kept. A hidden tab closes its stream and catches up from its last seq when it is visible again. On `reset` the tab reloads the newest group of its view, without `before`, and opens a new stream from it. A failed reload is retried after 2s, doubling to 30s, while the tab is visible. The stream seq lives only in the tab's script and is not shown or stored. An empty transaction id is filled in the page script on submit. The server forwards the posted id. The script does not invent `kind`.

`from` is caller-supplied. The token does not authenticate the sender.

## Code

Every Go file starts with the MPL-2.0 header used by the others:

```go
// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
```

Format with gofmt. Dependencies stay the standard library, cobra, `golang.org/x/sys`, `modernc.org/sqlite`, and `github.com/yuin/goldmark`. `github.com/dop251/goja` runs the page script in `internal/web/page_test.go` only. It stays off the request path.

Put new code in the package that already owns the behaviour. An exported doc comment states the contract. A further comment is one line for a non-obvious WHY. Comments do not carry ticket ids, PR numbers, or a bare TODO. A bad request, a conflict, or a failed write returns an error.

## Tests

`cmd/hotseat` has no tests. CLI cases build `./cmd/hotseat` with `CGO_ENABLED=0` into a temp binary. The suite command is in Checks.

Name the test for the rule (`TestNonLoopbackDoesNotListenOrCreateStore`). Do not add `t.Parallel`. Cases bind ports, take the store lock, and spawn processes. Several bind `127.0.0.1:4727`. Stop a bus on that port before the suite (`hotseat bus stop`).

`internal/cli` `TestMain` treats `HOTSEAT_FAKE_SERVE=1` as a fake listener on `HOTSEAT_FAKE_LISTEN` and exits. That pair is for the `unshare` network-namespace case. Leave both unset for a normal run. The test skips when `unshare` is missing.

## Runner

Design `hotseat-r7gj` (Hotseat conversation runner). Read it with `tk design get hotseat-r7gj --content`. The bus design `hotseat-ppyt` is decomposed and its tickets are done. This tree stores `kind`, the conversation binding, and member rows. `hotseat run` and `hotseat attend` are in this tree.

Settled, and in this tree:

- `kind` is a message field. `mode` stays the first line of a turn body. The database does not gain a phase, a mode column, or a cursor.
- Member rows hold registration, status, pid, and session id. Speech stays in the transcript. Registration replaces ack.
- Session ids stay out of the transcript, `member list`, logs, status lines, and prompts. An id works on the machine that stored it.
- Schema version stays `1`. Delete a database this binary already created. Do not add a migration.
- The bus process still does not launch agents. The web page shows message `kind`, renders the body as Markdown, and does not manage members.
- `hotseat run` is a client. It does not start the bus and does not open the database. It shells out to `start` and `tk`. A missing task resolver, prompt, session-id locator, or ticket code-root is a change in that program. Hotseat does not grow its own.
- `hotseat attend` is a client. It waits as `owner` with kinds `ask`, `error`, and `done`, prints only that match, and waits again until a `done` addressed to `owner`. It does not start the bus, open the database, publish, or write a member row. The cursor stays in the process. `--cursor` defaults to 0.
- Roster is `--agents` / `HOTSEAT_AGENTS` as `name=module:model`. There is no `HOTSEAT_MEMBERS`. The flag wins over the matching `HOTSEAT_*` variable. A resume launches only when that roster is passed again. Without it, the resume continues and does not launch. The turn cap defaults to 500 `turn` messages. The member process limit defaults to 8 hours. Interactive mode disables that process limit and keeps the cap. The launch cap, `--max-launches` / `HOTSEAT_MAX_LAUNCHES`, defaults to 300 member processes in one `run` process, of every launch kind, and starts from zero on a continuation.
- A task's `min_members` (empty is 1) is the smallest roster `run` accepts. A smaller roster creates nothing. A continuation checks the stored roster only after a stored stopping `done` is handled, and after dead leftovers are settled. A member failure that leaves fewer live (not `timed-out`) members, or a launch past the cap, publishes `error` with class `below minimum` or `launch cap` and winds down: no further launch or handoff, running members finish, a `turn` is not acted on, and `run` exits with `outcome: error` and the conversation open once none is in flight. A stopping `done` still closes. A member `run` stops itself is not a failure, and a resume that cannot launch skips the floor.
- A `turn` launches a recipient unless it has already answered. Inside the member's slot, `run` reads the transcript, and a `say` or `finding` from that member after the turn, with the turn's sender named in `to`, means it answered and is not launched. `all`, an `ask`, and a `say` to someone else do not count. Live runs and `--conversation` share this rule. A timed-out member is not launched.
- After a stopping `done`, `run` closes the conversation, starts no further launch, and stops other running members at once. The `done` sender, launched by this run or left from an earlier one, gets up to 30 seconds (`run.DoneGrace`, no flag) to exit before it is killed, so `run` can exit up to 30 seconds after the `done`. A zero exit in that period is `exited` with the session id stored. A non-zero exit, the process limit, or a missing session id in that period is `timed-out` and publishes `error`, with no handoff. A kill at the end of the period is `timed-out` with no `error`. A leftover sender is `exited` only when a session id is already stored, otherwise `timed-out`, and publishes no `error`.
- Instruction bodies over the 512 KiB maximum are refused before create. Naming, seat backup, and the turn loop are in the design. Follow that text.
- Finding discussion is design `hotseat-b225`. A `finding` from a recipient of a set that has not finished opens a discussion, unless it is stored while another set's discussions are open. When the set's recipients have exited or are `timed-out`, `run` reports each live recipient with no `say` or `finding` to the turn's sender as `no reply`, publishes `discuss` (body: the turn's `seq`), and, for two or more findings, launches the acting seat once for `aggregate` (one line per issue: kept finding, then dropped duplicates). `run` publishes `issues` (body: the turn's `seq`, then the groups), or each finding alone with `no aggregate` or `bad aggregate`. A dropped finding is not discussed, fixed, or unresolved. `run` runs the kept findings' discussions one at a time in `seq` order, then the follow-up, whose prompt carries `fixed: <n>`, `no reply:`, and `dropped:`. Stances are `propose`, `dismiss`, `agree`, `object`, and `stand`, one per discussion launch. A speaker whose launch ends `timed-out` gets no `no stance` or `extra stance`, its `pass` is ignored, and a single stance it stored is still judged. A finding settles when every live member but the proposer agrees, and `run` publishes `settled` (body: the finding's `seq`, a space, the proposal's `seq`). The finder applies a settled change and publishes `fixed`. A missed fix goes to the acting seat once. `--max-says` / `HOTSEAT_MAX_SAYS` (default 4) is each member's discussion launches per finding. A stall launches the acting seat for `extend` or `deadlock`. A seat whose stall launch ends `timed-out` with no single decision hands the stall to the backup, with no `no decision`. Each `extend` counts toward `--max-turns`, including one that refreshes nobody. An accepted `deadlock` publishes `halted` (body: the finding's `seq`) and a report launch. Markers are from `run` with an empty `to`. A seat timeout during a set's discussions holds the handoff until they finish.
- `internal/run/discussion.go` is the live state of one finding and has no bus or process. `internal/run/ledger.go` is a pure reading of the transcript: set windows, valid findings, issues and dropped duplicates, unanswered recipients, each discussion's start and asks, settles, fixes, and the stop result. `findings.go` launches and judges. A stance is judged only by the live launch window it arrived in. A continuation never replays one, and restarts the open discussion from its finding. It treats as history only the messages stored before it started and those a leftover stored up to its recorded exit. A stray from its own launches is judged.
- The stop result counts the messages stored before the stopping `done`. An unsettled finding or an unfixed change exits non-zero with `outcome: deadlock` (a `halted` exists) or `outcome: unsettled`, which also publishes one `unsettled` error. `run.ErrUnresolved` maps to the CLI's reported failure. Protocol error bodies are `<member>\n<class>\n<seq>`, and each class is a `class*` constant in `internal/run/run.go`.
- agy (Antigravity CLI) has no hotseat module. `herdr` is not a dependency.

Settled for the runner, and absent here:

- The chief task, the ticket-review task, and the design task. `run` publishes the module text and executes `turn`s.

`internal/web` uses `kindMessages`, `kindList`, and `kindConversation` for page modes. Those names are not the message field.
>>>>>>> Stashed changes

## Scope

Tickets and designs live in the tk scope `hotseat`, outside this tree. `tk pulse --scope hotseat` shows the board. Mutators self-commit in that scope. Commit a ticket body edit or `tk create` with `tk sync --scope hotseat`, and do not push the scope with host git. `tk doctor` takes no scope flag.

Do not commit this repository unless asked. Messages are scoped commits, `<scope>: <description>` with an optional body and no `feat` or `fix` prefix. Scopes in use include `bus`, `cli`, `client`, `store`, `run`, `web`, `hotseat`, and `agents`.
