# hotseat

One process hosts named conversations in SQLite. Callers publish into a transcript and block until a matching message exists or a deadline passes. Module `github.com/p3bot/hotseat`. The bus does not launch agents.

`hotseat run` and `hotseat attend` are the runner in design `hotseat-r7gj`. They are not in this tree until `hotseat-y7t5` and `hotseat-q8us`. See Runner before adding them.

Operator contract: `README.md` and the package doc comments. A behaviour those texts state changes in the same edit as the code.

## Layout

| Path | Owns |
| --- | --- |
| `cmd/hotseat` | `main`. Runs `cli.Execute`, then the signal code, then a status failure, then any other error on stderr |
| `internal/bus` | Listener, routes, rules, token, `GET /health`. Opens no connections to clients |
| `internal/store` | `hotseat.db`, schema, `hotseat.db.lock`. The only writer |
| `internal/client` | One HTTP call to a bus that is already running. Does not open the store |
| `internal/cli` | Cobra tree, detached bus, text results |
| `internal/web` | Loopback page. Client of a running bus. Does not open the store |

`.agents/roles/default.md` is the start role for this module and carries the same constraints.

## Checks

Go is the version on the `go` line in `go.mod`. This machine's default `CGO_ENABLED` is `1`. Every build and test sets `CGO_ENABLED=0`. The SQLite driver is `modernc.org/sqlite`. A cgo driver is out. `go test -race` needs cgo, so it is not a project check.

| Check | Command |
| --- | --- |
| Build | `CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat` |
| Test | `CGO_ENABLED=0 go test ./...` |
| Format | `gofmt -l .` prints nothing |
| Lint | `golangci-lint run` |

`cli.Version` is `dev` until `-ldflags "-X main.Version=..."` sets it. The repo-root binary `hotseat` is gitignored, as are `*.db`, `*.db-wal`, `*.db-shm`, and `*.db-journal`.

`.golangci.yml` is golangci-lint v2. Enabled linters: errcheck, govet, ineffassign, staticcheck, unused. errcheck ignores `fmt.Fprint`, `fmt.Fprintf`, and `fmt.Fprintln` only. `max-issues-per-linter: 0` and `max-same-issues: 0`, so the first finding fails the run.

NOTE: `golangci-lint run` is not a clean gate on this tree. Existing findings are unchecked `Close` errors and staticcheck `QF1006` in `internal/cli`. Leave findings that are already on main. A change adds none of its own.

## Process

`hotseat bus start` detaches and returns after the listener is bound and the database is open. It prints `pid`, `listen`, and `store`. The child is hidden `bus serve`. That command runs when it is a session leader and fd 3 is the readiness pipe. Without that session and pipe, it returns `bus serve is internal`.

| File | Role |
| --- | --- |
| `hotseat.db` | Database. `store.FileName` |
| `hotseat.db.lock` | Process flock. Separate inode from the database |
| `hotseat.pid` | Pid and listen address of the detached bus |
| `hotseat.log` | slog output of the detached bus |

The store directory is an absolute `$XDG_DATA_HOME/hotseat`. An unset, empty, or relative `$XDG_DATA_HOME` uses `$HOME/.local/share/hotseat`. A relative home is refused and nothing is created. One bus per store directory. A second start prints the running process and leaves it.

`hotseat bus status` takes `--address` (default `127.0.0.1:4727`) and has no `--store`. It calls `GET /health`. `pass` exits 0. `fail` and `connection_failure` exit 1. The error text is empty so stdout is the only report. When a local `bus serve` is bound to that address, the report includes its pid and store.

`hotseat bus stop` with no `--store` signals the default-store bus and any other local `bus serve` on `127.0.0.1:4727`. A process that is not `bus serve` and holds that port is left running, and stop exits 1. Stop waits until those processes have exited and each lock is released. A second stop of an already stopped bus succeeds.

SIGINT and SIGTERM exit `128+signal` (130 and 143). `main` reads that code before the error path, so `context.Canceled` does not become exit 1.

## Calls

Every operation is `POST` of one JSON object. A completed call is HTTP 200. Read `outcome`. Unknown JSON fields are ignored.

| Outcome | Where it appears |
| --- | --- |
| `ok`, `timeout`, `refused`, `unavailable` | Bus JSON |
| `connection_failure` | Client and CLI only. The call never completed |

`refused` names the rule in `reason` and writes nothing. `unavailable` writes nothing. A dropped connection has no body. The client retries that once, with the same body and the same token. A dropped launch whose retry is refused because the member is already launched returns the stored row. A second launch, after a reply was received, stays refused. Dial failure, cancellation, a non-200 or unexpected outcome, and a completed outcome are returned without a second attempt. Keep-alives are off so the retry cannot reuse the connection that dropped. Dial timeout is 5s. There is no request timeout. A wait blocks until the bus replies.

Routes: `/create`, `/publish`, `/read`, `/wait`, `/close`, `/list`, `/register`, `/member`, `/session`, `/members`. `GET /health` is not an operation. It takes no token on any address. The body is `pass` (200) or `fail` (503) plus a newline. Off loopback the new routes require the same bearer token. Loopback stays unauthenticated.

Publish requires `kind`. It uses the participant name grammar. The vocabulary is open. Publish is acknowledged only after a durable commit. The attempt is the conversation, the sender, and `txid`. The bus requires that id and does not generate one. `kind` is part of the content, with `to` and body. The same attempt with the same kind, the same `to`, and the same body returns the original message and `already_stored`, including after close. A different kind, `to`, or body is refused. `to` order is content. The bus does not sort or dedupe it. Another sender with that `txid` stores a new message.

`read` and `wait` take an optional `kinds` array. Omit it and the address rule alone stands. A present empty array is refused. A named wait still returns the span through the match, including other kinds. A named read returns a message when it is addressed to that name and its kind is listed. The limit counts returned messages.

Create stores the binding on the conversation: `task`, `ticket`, `seat`, `directory`, `prompts`, `custom`, and `roster`. Omitted fields are empty. `prompts` and `roster` keep the caller's order. A roster requires a seat that is one of the roster names. Empty roster and empty seat is the name-only create. Create and list return the binding. JSON objects include the keys. `hotseat create` stays name-only. Empty binding fields are omitted in the text result.

A member row is one roster name. It holds launched time, registered time, status, session id, and pid. Status is `launched`, `registered`, `running`, `exited`, or `timed-out`. `/register` sets the registered time. The status becomes `registered` only when the row is `launched`. `/member` inserts `launched` and updates `running`, `exited`, and `timed-out`. A pid without a status is refused and writes nothing. A call with neither returns the row. `/members` is `hotseat member list` and returns name, launched time, registered time, and status, in roster order. It does not return a session id or a pid. `/session` stores and returns session ids. There is no CLI for that route. Session ids and pids stay off read, wait, list, member list, the page, and the bus log. Create does not insert a row for `owner` or for a roster name.

The CLI mints an omitted id as 10 lowercase hex characters, prints `txid: <id>` to stderr, flushes, then sends it. A dropped CLI call retries that same id. Pass `--txid` to send that value. `--kind` is required on publish. `hotseat read` and `hotseat wait` take repeatable `--kind` and omit `kinds` when the flag is absent. `--body` and `--body-file` are exclusive. `--body-file -` reads stdin. A body whose text is `-` is `--body -`. Invalid UTF-8 in a publish body or txid is `refused` in the client and is not posted. JSON would otherwise replace the bytes and store a different message.

`cursor` `0` means the caller has seen nothing. The bus does not store cursors. `deadline` is a Go duration (`30s`, `500ms`) from arrival, or an RFC3339 end time on the bus clock. Omit it to wait until a match or a dropped connection. `0s` and a time already past time out at once when nothing matches. A stored match still returns `ok`.

Close sets `closed` and still accepts a later publish. A member write after close is stored. Close does not end a wait. `["all"]` wakes every named waiter except the sender. Empty `to` matches only an unnamed wait. A named read skips messages that are not addressed to that name. `from` must not be `all`.

Names are 1 to 64 characters from ASCII letters, digits, `.`, `_`, and `-`. Exact refusal strings are the `bus.Reason*` constants. A new reason is a constant beside the others, plus a test whose name states the rule.

CLI stdout is line text. A field is `key: value`. A message body, and any value that contains a newline, is `key <<N` and then those bytes. The count keeps an empty body, a multi-line body, and a line that looks like a field. Usage errors go to stderr and print no stdout. Cobra commands set `SilenceUsage` and `SilenceErrors`.

`TestReadmeDescribesDetachedBus` reads `README.md` and requires each substring below. The last one includes the backtick characters around `--store`.

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
```

The test fails when the README contains `prints one JSON object`, or a line whose trimmed text is `hotseat bus`.

## Store

`Open` resolves the directory to an absolute path before it creates anything. It chmods the directory to `0700`, including a directory that already existed. Database, wal, shm, and lock symlinks are refused and left unchanged. The lock is opened with `O_NOFOLLOW` and mode `0600`.

The flock stays on `hotseat.db.lock`. It does not share the database inode. On macOS, flock shares SQLite's lock table, and closing any descriptor on the database file drops every lock on it.

DSN pragmas: `busy_timeout=0`, `journal_mode=WAL`, `locking_mode=EXCLUSIVE`, `foreign_keys=1`, `synchronous=FULL`, `_txlock=immediate`. `busy_timeout` 0 makes another holder fail this process instead of stalling it. `EXCLUSIVE` is checked after open. `FULL` means commit returns only after the write is durable. `SetMaxOpenConns(1)`.

Tables are `STRICT`. `meta.schema` is `store.SchemaVersion`, the string `1`. Statements are `CREATE TABLE IF NOT EXISTS`.

`Open` returns `store.ErrSchema` for a recorded version other than `1`, and for a version-`1` file that lacks `kind`, the binding columns, or the member table. It does not alter the file and it does not serve it. There is no migration. Delete a database this binary already created before the new process opens it.

Idempotent publish, and close of an already-closed conversation, return the unexported `errNoChange` so the transaction rolls back instead of committing an empty write.

Sentinels: `ErrNotFound`, `ErrConflict`, `ErrHeld`, `ErrSchema`, `ErrNotRoster`, `ErrNotMember`, `ErrLaunched`. Wrap with `%w` when the caller branches on the cause.

## Access

Loopback accepts every operation with no token. Passing `--token-file` on loopback leaves the check off. Any other socket requires the file. A missing or empty file does not listen, and no database is created. A failed bind creates no database.

`ResolveListen` resolves a hostname once. One non-loopback answer selects one non-loopback address, so the socket is the listener that requires a token. IPv4 wins inside that set. An empty host is remote. `127.0.0.1` and `::1` are different addresses.

The header is `Authorization: Bearer` plus the token. `bus.WriteToken` sets it. A missing token is `token is required`. A wrong token is `token does not match`. Nothing is written. The token is not a flag, not a sender, and not stored. Logs, the page, and prompts do not contain it.

The page default is `127.0.0.1:4728`. It listens on loopback only. Any other socket is closed. The page holds the bus token, so a public socket would hand that token to the network. The request host is checked before a bus call. On loopback, `localhost` with the same port is that host. An empty listen host refuses every request. The page lists, reads, and publishes. The publish form sends `kind`, and the publish result and the transcript show it. The page does not create, wait, close, or manage members. An empty transaction id is filled in the page script on submit. The server forwards the posted id. The script does not invent `kind`.

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

Format with gofmt. Dependencies stay the standard library, cobra, `golang.org/x/sys`, and `modernc.org/sqlite`. `github.com/dop251/goja` runs the page script in `internal/web/page_test.go` only. It stays off the request path.

Put new code in the package that already owns the behaviour. An exported doc comment states the contract. A further comment is one line for a non-obvious WHY. Comments do not carry ticket ids, PR numbers, or a bare TODO. A bad request, a conflict, or a failed write returns an error.

## Tests

`cmd/hotseat` has no tests. CLI cases build `./cmd/hotseat` with `CGO_ENABLED=0` into a temp binary. The suite command is in Checks.

Name the test for the rule (`TestNonLoopbackDoesNotListenOrCreateStore`). Do not add `t.Parallel`. Cases bind ports, take the store lock, and spawn processes. Several bind `127.0.0.1:4727`. Stop a bus on that port before the suite (`hotseat bus stop`).

`internal/cli` `TestMain` treats `HOTSEAT_FAKE_SERVE=1` as a fake listener on `HOTSEAT_FAKE_LISTEN` and exits. That pair is for the `unshare` network-namespace case. Leave both unset for a normal run. The test skips when `unshare` is missing.

## Runner

Design `hotseat-r7gj` (Hotseat conversation runner). Read it with `tk design get hotseat-r7gj --content`. The bus design `hotseat-ppyt` is decomposed and its tickets are done. This tree stores `kind`, the conversation binding, and member rows. `hotseat run` and `hotseat attend` are not in this tree until `hotseat-y7t5` and `hotseat-q8us`.

Settled, and in this tree:

- `kind` is a message field. `mode` stays the first line of a turn body. The database does not gain a phase, a mode column, or a cursor.
- Member rows hold registration, status, pid, and session id. Speech stays in the transcript. Registration replaces ack.
- Session ids stay out of the transcript, `member list`, logs, status lines, and prompts. An id works on the machine that stored it.
- Schema version stays `1`. Delete a database this binary already created. Do not add a migration.
- The bus process still does not launch agents. The web page shows message `kind` and does not manage members.

Settled for the runner, and absent here:

- `hotseat run` and `hotseat attend` are clients. They do not start the bus and do not open the database.
- Shell out to `start` and `tk`. A missing task resolver, prompt, session-id locator, or ticket code-root is a change in that program. Hotseat does not grow its own.
- Roster is `--agents` / `HOTSEAT_AGENTS` as `name=module:model`. There is no `HOTSEAT_MEMBERS`. The flag wins over the matching `HOTSEAT_*` variable. The turn cap defaults to 500 `turn` messages. The member process limit defaults to 8 hours. Interactive mode disables that process timeout and keeps the cap.
- Instruction bodies over the 512 KiB maximum are refused before create. Naming, seat backup, and the turn loop are in the design. Follow that text.
- Copilot is out. agy (Antigravity CLI) has no hotseat module in this work. `herdr` is not a dependency.

`internal/web` uses `kindList` and `kindConversation` for page modes. Those names are not the message field.

## Scope

Tickets and designs live in the tk scope `hotseat`, outside this git tree. The scope is tk-driven. `tk pulse --scope hotseat` shows the board.

Mutators self-commit inside that scope. Do not push it with host git. A ticket body edit or `tk create` is committed with `tk sync --scope hotseat`. `tk doctor` takes no scope flag.

Do not commit this repository unless asked. Messages are Scoped Commits: `<scope>: <description>`, optional body, no `feat` or `fix` prefix. Scopes already used here include `bus`, `cli`, `client`, `hotseat`, and `context`.
