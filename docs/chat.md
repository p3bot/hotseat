# Chat client reference

The `hotseat chat` verbs, their text results, `chat status`, and `chat attend`. See the [README](../README.md) for an overview.

## Verbs

The root commands are `bus`, `chat`, `run`, and `web`. `chat` calls a bus that is already running, then exits. Every verb except `list` names the chat as its first argument. There is no environment fallback for that name. `--address` and `--token-file` are persistent flags on `chat`. `bus start` and `web` declare both locally, and `bus status` declares only `--address`. Each reads `HOTSEAT_ADDRESS` or `HOTSEAT_TOKEN_FILE` when its flag is unset. The flag wins. The default address is `127.0.0.1:4727`. The token is not a command-line argument. Omit the token file for loopback. Omit `--txid` and send writes `txid: ` and 10 lowercase hex characters to stderr, then sends that id. A retry without `--txid` is not idempotent. Pass `--txid` to send that value, including an empty one. The line is written before the call and flushed. A dropped call is retried once with that same id. The client does not store the id.

```bash
hotseat chat create job
hotseat chat send job --from alice --kind say --to bob --body hello
hotseat chat send job --from alice --kind say --to bob --body hello --txid 1
hotseat chat --address 192.0.2.10:4727 --token-file /run/hotseat/token send job --from alice --kind say --to bob --body hello --txid 1
hotseat chat send job --from alice --kind say --to bob --body-file - --txid 1
hotseat chat send job --from bob --kind say --to alice --body yes --re 1
hotseat chat read job
hotseat chat read job --cursor 0 --limit 50 --kind say --kind turn
hotseat chat wait job --cursor 0 --to bob --deadline 30s
hotseat chat close job
hotseat chat list
hotseat chat join job alpha
hotseat chat members job
hotseat chat status job
```

### Send, read, and wait

`--kind` is required on send. `--re <seq>` sends `re`. Omit it and the field is left out. A message with `re` prints `re: <seq>` after the `kind` line, on send, read, wait, and attend. A message without it prints no `re` line. Repeat `--kind` on read or wait to send `kinds`. Omit it and the field is left out. `--to` on read and wait takes one participant, and a repeat is a usage error. `--to` on send stays repeatable. `--body` is the message. `--body-file` reads it from a file, and `--body-file -` reads stdin, so a body can be larger than one command argument. Pass exactly one of the two. A body or a transaction id that is not valid UTF-8 is `refused` with that bus rule, and the client does not connect. `hotseat chat create` sends a name only. `hotseat chat list` and the create result print the binding, omitting empty fields, in roster order, each roster entry as `roster: name=module:model`, or `roster: name` when its module and model are empty, then `permission` when stored. The list puts the chat with the newest message first, by that message's `seq`. Chats with no message follow, ordered by name. `hotseat chat members` prints name, launched time, joined time, and status. It does not print a pid or a session id. `chat read` defaults `--cursor` to 0 and `--limit` to 100. An ok read prints `more: true` or `more: false`. `chat wait --cursor` stays required.

### Text result

Each `chat` command prints one text result and exits by outcome: `ok` 0, a usage or startup error 1, `refused` 2, `timeout` 3, `unavailable` and `connection_failure` 4. SIGINT and SIGTERM exit 128 plus the signal number. A field on one line is `name: value`. A message body is always a byte count and then those bytes, and so is any other value that contains a newline. The count keeps an empty body, more than one line, and a line that looks like a field inside the value. A usage error is a message on stderr and no stdout. A dropped call is retried once with the same arguments. Timeout is only the bus outcome. A full read page carries each message `seq`. The client does not request the next page.

```text
outcome: ok
already_stored: false
chat:
name: job
status: open
message:
seq: 1
time: 2026-10-08T07:00:00Z
from: alice
kind: say
to: bob
txid: 1
body <<5
hello
```

## Status

`hotseat chat status <name>` answers what is happening with one chat from bus reads alone. It makes only read calls, `/lease` `get` first, then `/list`, `/members`, `/records`, and `/read` to the end of the transcript, and writes nothing. The records are read before the transcript, so the transcript holds every message a record names. When every read is `ok` it exits zero whatever the state. A read that answers otherwise prints only that call's `outcome` and `reason` and exits by that outcome, so a missing chat is `outcome: refused` with `reason: chat not found` and exit 2, and an unreachable bus is `outcome: connection_failure` and exit 4.

```text
outcome: ok
chat: job
task: tasks:hotseat/ticket/review
ticket: foo-x234
seat: grok
roster: grok=hotseat/grok:grok-4
roster: opus=hotseat/claude:opus
permission: auto
state: running
since: 2026-11-18T08:47:13Z
expires: 2026-11-18T08:47:33Z
member: grok
member_status: running
member: opus
member_status: exited
newest: 57
findings: 4
settled: 2
fixed: 1
dropped: 1
discussing: 41
```

The binding lines omit empty fields, as `list` does, and the roster is in roster order, each entry as on `list`, then `permission` when stored. `state` is `running` while a conductor holds the lease, else `closed` once the chat is closed, else `idle`. It is a fact, not a judgement of how a run ended. `since` and `expires` follow only `running`, and `expires` is for a reader to compare. Member rows print as stored, with no pid liveness check. `newest` is the highest `seq` of any kind. The counts read every message and record: `findings` is the kept findings, `dropped` the duplicates an aggregate dropped, `settled` the kept findings with a settle, dismissals included, and `fixed` the settled changes with a fix. A dismissal is never fixed. `discussing` follows only `running`. Findings are worked one at a time in `seq` order, and `discussing` is the first kept finding whose discussion has started and that has no settle, or settled on a change with no `fixed`. It is not printed when a halt names that finding, since a halt ends every discussion, and a later finding is never reported in its place. A task with no findings prints each count as `0`.

When a stop record exists, `done: <seq>` names the done it records, then `unsettled` and `unfixed` lines and `run_outcome` (`done`, `deadlock`, or `unsettled`), from the same rule and the same cut before that done as `run`'s stop result, so the two print the same lines. The counts read the whole ledger, so they can differ from the stop lines when a finding, settle, or `fixed` is stored after that done. `status` never decides which done stopped a run, so with no stop record it prints neither. When a `stopped by` error from `run` to `owner` is newer than the recorded done, or there is no stop record, the newest one prints as `stop: <class>` and `stop_seq: <seq>`. `run_outcome` is data, not an exit status. Session ids, pids, start tokens, the lease holder, the token, and decision records stay off the output. `hotseat bus status` reports the bus process instead.

## Attend

`hotseat chat attend <name>` waits as `owner` on one chat and prints `ask`, `error`, and `done`. It calls a bus that is already running. It does not open the database, launch a process, send, or write a member row. It does not resume a chief. A `say` is not printed. A join is a member row, not a message, so it never appears. Joins are `hotseat chat members`.

The chat name is the first argument. `--cursor` is the last finished sequence. The default is `0`, which reads from the start. The cursor stays in this process. The next wait uses the match sequence just printed. A new process reads from sequence 0 unless `--cursor` is set. `--address` and `HOTSEAT_ADDRESS` default to `127.0.0.1:4727`. `--token-file` and `HOTSEAT_TOKEN_FILE` follow the other `chat` verbs. The flag wins. The token value is not printed.

Each printed match is one text result: the chat name, `match_seq`, and the message (`from`, `kind`, `re` when it has one, `to` in stored order, and the body). The bus span may contain other kinds. A `say` earlier in that span is not printed. An `ask` or an `error` is printed and attend waits again. There is no deadline. A `done` addressed to `owner` is printed and attend exits zero, including a `done` from a name other than the acting seat. A `done` that is not addressed to `owner` does not end attend. A failed call exits by the outcome table: refused 2, timeout 3, unavailable and connection_failure 4. The text result stays on stdout. A usage error prints no stdout and exits 1.

```bash
hotseat chat attend job
hotseat chat attend job --cursor 4
```
