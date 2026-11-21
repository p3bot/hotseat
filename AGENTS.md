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

`run` keeps its decisions as decision records behind `/record` and `/records`: set, grouping, settle, fix, halt, and stop. A record is a fact keyed by message `seq` and holds no phase, mode, or cursor. This reverses `hotseat-b225`'s "the transcript is the only durable state" and amends `hotseat-r7gj`'s no-phase rule. `run` publishes no marker. Its one message about a decision is `settled`, to `owner` with `re` the agreed proposal, because no participant states a settle, and nothing reads it as state. A continuation needs the records as well as the transcript, so a dump of messages is not a full backup of a run.

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
- Tables are `STRICT`. `meta.schema` is `store.SchemaVersion`, `1`. There is no migration. `Open` returns `ErrSchema` for a shape this binary did not create and leaves the file. The version-1 shape includes the members `start` column, the host start token stored beside `pid`, and the decision record tables `sets`, `set_findings`, `groupings`, `drops`, `settles`, `fixes`, `halts`, and `stops`. A schema change means deleting the database.
- A write that changes nothing (idempotent publish, a record replay, close of a closed conversation) returns `errNoChange`, so the transaction rolls back.
- Store sentinels are `ErrNotFound`, `ErrConflict`, `ErrHeld`, `ErrSchema`, `ErrNotRoster`, `ErrNotMember`, `ErrLaunched`, `ErrReplyTarget`, `ErrRecordReference`, `ErrKeptDropped`, `ErrNotKept`, and `ErrFixDismissed`. Wrap with `%w` when the caller branches on the cause.
- Refusal text is a `bus.Reason*` constant. A new reason is a constant beside the others, plus a test whose name states the rule.
- Session ids, pids, member start tokens, the token, and lease holders stay off read, wait, list, member list, `/register`, the page, logs, status lines, and prompts. Pids and start tokens appear only on a one-row `/member` read or write. A holder is also off `/lease` `get`. Decision records stay off read, wait, watch, list, member list, the page, and logs.
- Conductor leases live in `Service` memory under `s.mu`, never in the store. A fenced write (`/record`, `/member` with a status, `/session` with a session, `/publish` with a holder) checks the lease and writes under that one lock. A launch has no fence of its own: `recordLaunch` writes a fenced member row right after `cmd.Start` and kills the process group when it is refused. That is the only member a lost lease kills. `run` then waits for the members it launched and stops watching the ones it adopted, and the next holder adopts them by start token.
- `from` is caller-supplied. The token does not authenticate the sender.
- The page listens on loopback only, because it holds the bus token.
- CLI stdout is `key: value`, and a value with a newline is `key <<N` and then those bytes. Usage errors go to stderr with no stdout. Commands set `SilenceUsage` and `SilenceErrors`. SIGINT and SIGTERM exit `128+signal`, read before the error path.

## Runner

The designs are `hotseat-r7gj` (runner), `hotseat-b225` (finding discussion), and `hotseat-k759` (replies and decision records). Read one with `tk design get <id> --content`. The code is current.

Task modules are `tasks:hotseat/ticket/triage`, `ticket/review`, `ticket/implement`, `pre-commit/review`, and `workflow/implement`, which is the chief's workflow. Member-facing text is the roles `hotseat/member` and `hotseat/chief` and the context `hotseat/bus`. A change to that text lands in `library/` first.

| File | Owns |
| --- | --- |
| `loop.go` | Transcript watch, turns, stop on `done` |
| `lease.go` | Conductor lease: holder, acquire, renewal, release, lost-lease stop that leaves members running |
| `launch.go` | Member processes, launch cap, handoff |
| `state.go` | Member rows, process identity, leftovers on resume, backup, prompts |
| `proc_*.go` | Per-platform zombie check and start token |
| `findings.go` | Finding protocol: launches, judging, prompts |
| `discussion.go` | Live state of one finding. No bus or process |
| `ledger.go` | Pure reading of the transcript and the decision records |
| `stop.go` | Stop errors: the `stop*` classes, typed causes, and the one stop error `noteHalt` publishes |

- A stance is judged only by the live launch window it arrived in. A continuation never replays stances and restarts the open discussion from its finding. History is the messages stored before the continuation started, plus each leftover's messages up to its recorded exit.
- A protocol error body is `<member>\n<class>`, or `<class>` alone, and its `re` is the message that caused it. `bad finding`, `no reply`, `bad turn`, `bad done`, and `unsettled` take a transaction id from `factTx`, so a continuation stores no second copy. Each class is a `class*` constant in `run.go`. The stop classes are a separate set, `stop*` constants in `stop.go`. `run.ErrUnresolved` maps to the CLI's reported failure.
- A settled change is fixed by its fix record, or else by the first creditable `fixed`: after the settle's `after`, `re` naming the finding, from the finder, the seat, or the seat's backup. The stop result and continuation read this one ledger rule. A record end at `after` includes the message at that `seq`.
- Prompts give the exact `--re` for the kind they ask for. A present `re` other than the one `run` expects rejects the message, and a missing `re` falls back to the `seq` windows. `re` adds a check and never replaces a launch window.
- Settled, do not reopen:
  - the roster is `name=module:model`, and there is no `HOTSEAT_MEMBERS`;
  - `mode` is the first line of a turn body, and the database has no phase, mode, or cursor column. Decision records are facts keyed by message `seq`, not progress;
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

`internal/cli` `TestMain` treats `HOTSEAT_FAKE_SERVE=1` as a fake listener on `HOTSEAT_FAKE_LISTEN`, for the `unshare` network-namespace case, which skips when `unshare` is missing. Leave both unset.

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

It fails when the README contains `prints one JSON object`, or a line whose trimmed text is `hotseat bus`.

## Scope

Tickets and designs live in the tk scope `hotseat`, outside this tree. `tk pulse --scope hotseat` shows the board. Mutators self-commit in that scope. Commit a ticket body edit or `tk create` with `tk sync --scope hotseat`, and do not push the scope with host git. `tk doctor` takes no scope flag.

Do not commit this repository unless asked. Messages are scoped commits, `<scope>: <description>` with an optional body and no `feat` or `fix` prefix. Scopes in use include `bus`, `cli`, `client`, `store`, `run`, `web`, `hotseat`, and `agents`.
