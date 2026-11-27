# Bus reference

The detached bus process, its store, and the HTTP protocol every client speaks. See the [README](../README.md) for an overview.

## Process

```bash
hotseat bus start
hotseat bus start --store /var/lib/hotseat
hotseat bus start --store /var/lib/hotseat --address 127.0.0.1:4727 --max-body 524288
hotseat bus start --store /var/lib/hotseat --address 192.0.2.10:4727 --token-file /run/hotseat/token
hotseat bus status
hotseat bus stop
```

`hotseat bus start` runs the bus detached from the terminal and returns after the listener is bound, the database is open, and the page is listening. It prints the pid, the listen address, the store path, and `page` as a URL. The page listens on `127.0.0.1` at the next port and calls this bus. It does not open the database. The default bus prints `page` as `http://127.0.0.1:4728`. A second bus does not start while its page port is in use. Closing the terminal does not stop the bus. The log is `hotseat.log` in the store directory. `hotseat bus status` asks a listen address whether the bus can serve. The default address is `127.0.0.1:4727`. It prints `health` as `pass`, `fail`, or `connection_failure`, and the address it contacted. When a `bus serve` process on this machine is bound to that address, it also prints that pid and the store directory that process holds. `pass` exits 0. `fail` and `connection_failure` exit 4. `hotseat bus stop` with no `--store` sends SIGTERM to the bus for the default store and to any other local `bus serve` bound to `127.0.0.1:4727`. It returns after those processes have exited and each `hotseat.db.lock` is released. A second stop, when those buses are already stopped, succeeds. A process that is not `bus serve` and holds `127.0.0.1:4727` is left running, and stop exits 1. `hotseat bus stop --store` signals only the bus for that directory.

### Store and listen address

`--store` selects the directory for `start` and for `stop`. Without `--store`, `stop` uses the default directory and also stops any other local `bus serve` on the default listen address. `status` does not take `--store`. It takes `--address`, and the default is `127.0.0.1:4727`. When `--store` is omitted the directory is `$XDG_DATA_HOME/hotseat`. An unset, empty, or relative `$XDG_DATA_HOME` uses `$HOME/.local/share/hotseat`. A relative home directory is refused and nothing is created. The database is `hotseat.db` in that directory. A database this binary will not serve, from another schema version or an earlier shape of version 1, is refused, the bus does not start, and the file is left unchanged. There is no migration. Delete `hotseat.db`, `hotseat.db-wal`, and `hotseat.db-shm` before starting the new build. This process is the only writer, and it holds `hotseat.db.lock` beside the database until it exits. The default listen address is `127.0.0.1:4727`. A loopback address accepts every operation with no token. A hostname is resolved once and the process listens on one address from that lookup: a loopback address when every answer is loopback, otherwise a non-loopback address. Any other address requires `--token-file` and does not listen when the file is missing or empty, and no database is created. A failed bind creates no database. The file holds one shared token. A trailing newline is ignored. The token is printable ASCII with no spaces. The process does not log it. Passing the file on loopback does not turn the check on. `GET /health` is unauthenticated on every address. The body is `pass` or `fail`. The default maximum body is 512 KiB. One bus runs for each store directory. A second start prints the running process and does not change it.

## Protocol

Every operation is `POST` of one JSON object to `/<operation>`. A completed call responds `200` with one JSON object. Read `outcome`. It is `ok`, `timeout`, `refused`, or `unavailable`. A dropped connection has no body and is not a timeout. Unknown JSON fields are ignored.

### Token

A loopback listener ignores credentials. Any other listener requires the token on every operation, including from a peer on the same machine. The header is `Authorization: Bearer` and the token. `WriteToken` in package bus is that header. A missing token is refused with `token is required`. A wrong token is refused with `token does not match`. Nothing is written. The token does not select `from` and is not stored.

### Health

`GET /health` is not an operation. It takes no token on any address. The body is `pass` and status 200 when the bus can read the schema it serves, and `fail` and status 503 otherwise. A trailing newline is the only other byte. The body does not carry the pid, the listen address, the store path, or the token.

### Routes

| Call | Body |
| --- | --- |
| `/create` | `{"name","task"?,"ticket"?,"seat"?,"directory"?,"prompts"?,"custom"?,"roster"?,"permission"?}` |
| `/send` | `{"chat","from","to","body","txid","kind","re"?,"holder"?}` |
| `/read` | `{"chat","cursor","limit","name"?,"kinds"?}` |
| `/wait` | `{"chat","cursor","name"?,"deadline"?,"kinds"?}` |
| `/close` | `{"chat"}` |
| `/list` | `{}` |
| `/messages` | `{"limit","before"?}` |
| `/watch` | `{"cursor"?,"chat"?,"rev"?,"limit"?,"deadline"?}` |
| `/join` | `{"chat","name"}` |
| `/member` | `{"chat","name","status"?,"pid"?,"start"?,"holder"?}` |
| `/session` | `{"chat","name"?,"session"?,"holder"?}` |
| `/members` | `{"chat"}` |
| `/record` | `{"chat","record",...,"holder"}` |
| `/records` | `{"chat"}` |
| `/lease` | `{"chat","lease","holder"?}` |

### Fields

`to` is an array. `[]` is empty. `["all"]` is the single value all. Any other array is an ordered name list and is not sorted or deduplicated. A different order is different content. `kind` uses the same character rules as a name. The bus stores that name and does not check its meaning, so `act` and any other legal name are stored. `kinds`, when present, is an array of those names. Omit it and read and wait match as they do without a kind filter. An empty array is refused. `re` is the `seq` of an earlier message of the same chat that this send responds to. Omit it, or send `null`, to respond to nothing. A value that is not an integer greater than zero, including a string or a fraction, is refused. A `seq` of another chat, a `seq` that does not exist, and the send's own `seq` are refused and write nothing. Every message object from send, read, wait, watch, and `/messages` has `re`, the integer or `null`. `deadline` is a Go duration such as `30s` or `500ms`, or an RFC3339 end time. Omit it to wait until a match or a dropped connection. A duration is measured from arrival. An end time is absolute on the bus clock. A time already past times out at once when nothing matches, and a retry sends that same time.

### Messages

An ok `/messages` returns the newest `limit` messages across every chat, highest `seq` first. Each message includes `chat`. With `before`, only messages whose `seq` is less than `before` are returned, so the lowest `seq` of one result is the next `before`. A `before` less than 1 is refused. There is no command for this route.

### Watch

`/watch` takes `cursor`, `chat`, `rev`, `limit`, and `deadline`, each optional. `limit` is required with `cursor` and is a positive integer with no upper bound; the read cap of 1000 does not apply. `chat` uses the name grammar. A name with no chat matches no message and is not refused. `deadline` follows `/wait`. An ok watch returns `messages` with `seq` greater than `cursor`, oldest first, in that chat or every chat, at most `limit`, each with `chat`. It also returns `more`, true when further matching messages follow the last one returned, and `rev`. Omit `cursor` and the watch returns no messages and waits on `rev` alone. It returns at once when a matching message exists, when `rev` is omitted, or when `rev` differs from the current counter. Otherwise it blocks until one of those holds, the deadline passes (`timeout`), the connection drops, or the bus stops. `rev` is a change counter for the chat list, kept in bus memory. It starts at a random value below 2^53 when the bus starts, so a restarted bus does not return a value a caller still holds. Compare it for equality only. It increases on a create that inserted, on a close that changed the status, and on a stored send to a chat other than the one holding the newest message. An idempotent send and a close of a closed chat change nothing and wake no watch. Omit `rev` on the first watch. There is no command for this route.

### Sequence and cursors

`seq` is assigned at commit. It increases across the whole store and within each chat, with gaps, so one chat's messages need not be numbered 1, 2, 3. A cursor is a `seq` the caller has seen. `cursor` `0` means the caller has seen no message. The bus does not store cursors or transaction ids except the txid recorded on an accepted message. The caller passes both on each call.

### Create

An ok create returns the chat, including its binding. Omitted binding fields are stored empty. JSON always includes the keys: empty text is `""` and an empty list is `[]`. `prompts` and `roster` keep the caller's order. `roster` is an array of `{"name","module"?,"model"?}` objects. Any other key, or an element that is not such an object, is refused. A roster name uses the name grammar. `module`, `model`, and `permission` are strings the bus stores and returns as given, and an omitted one is `""`. A duplicate roster name is refused. A roster requires a `seat` that is one of those names. A seat with an empty roster is refused. Empty roster and empty seat is a name-only create. An empty string inside `prompts` is refused. A name that already exists returns that chat, its stored binding, and `"already_existed": true`, and writes nothing. Create does not insert member rows.

### Send

An ok send returns the message, including `seq` and `kind`, and `chat` with its binding, only after it is durable. `kind` is required. An attempt is the chat, the sender, and `txid`. The bus requires that id and does not generate one. The same attempt with the same `kind`, the same `to`, the same body, and the same `re` returns that message and `"already_stored": true`, including after close, and writes nothing. The same attempt with a different `kind`, `to`, body, or `re` is refused and writes nothing. A `re` present on one and absent on the other is different. Another sender with that `txid` stores a new message. Close sets `status` to `closed` and still accepts a new message. That send returns `status` `closed`. Send, read, wait, and the member routes follow the same rules on a closed chat.

### Read and wait

An ok named read returns messages addressed to that name. `kinds` keeps only listed kinds. An ok unnamed read with `kinds` returns only listed kinds. The limit counts returned messages. A read `limit` is an integer from 1 to 1000: one below 1 is refused with `limit must be a positive integer`, and one above 1000 with `limit must be at most 1000`, because even a full page of 512 KiB bodies is large. An ok read returns `more`. It is `true` when, after the page is full, a further message passes the same `name` and `kinds` filter. That message is not returned. It is `false` when the read reaches the end of the chat. An ok named wait returns every message after the cursor through the first addressed message of a listed kind, and `match_seq`. Omit `kinds` and the match is address alone. Messages of other kinds, and messages addressed to other names, stay in that span. An ok unnamed wait returns only the next message, or, when `kinds` is set, the next message of a listed kind. Timeout returns no messages. Close does not end a wait.

### Members and sessions

`/join` sets the joined time from the bus clock. The status becomes `joined` only when the row is `launched`. `running`, `exited`, `timed-out`, and `joined` keep their status. The name must be a roster name that already has a row. `/join` returns the public row: name, status, launched time, and joined time, with no pid and no `start`. `/member` with `status` `launched` inserts that row. A second `launched` is refused. `running`, `exited`, and `timed-out` update an existing row. `pid`, when sent, is an integer greater than zero and is stored only with a status. A pid without a status is refused and writes nothing. `start` is the caller's start token for that pid, 1 to 128 printable ASCII characters with no spaces, which the bus stores beside the pid without interpreting it. A `start` without a `pid` is refused and writes nothing. A write with a `pid` replaces the stored `start`, and one with a `pid` and no `start` clears it. `/member` with no `status` and no `pid` returns that row, including `pid` and `start`, and does not return a session id. `/members` returns name, launched time, joined time, and status, in roster order, with no session id, no pid, and no `start`. `/session` stores or returns session ids for the caller of that route. There is no command for it. Session ids, pids, and start tokens are absent from read, wait, list, `/join`, and `/members`, and `hotseat chat join` prints none of them.

### Decision records

`/record` and `/records` hold `hotseat run`'s decision records, insert-only facts about messages that `run` reads back on a continuation. There is no command for either route. Records are absent from read, wait, watch, list, `/members`, and the page, and neither route changes `rev` or wakes a wait or watch. The bus log holds each call and its outcome and no record content. `record` selects the write, and each seq is an integer greater than zero:

| `record` | Fields | Key |
| --- | --- | --- |
| `set` | `turn`, `findings` (may be `[]`) | turn |
| `grouping` | `turn`, `drops` (`[{"finding","kept"}]`, may be `[]`) | turn |
| `settle` | `finding`, `proposal`, `proposal_kind` | finding |
| `fix` | `finding`, `fixed` | finding |
| `halt` | `finding` | chat |
| `stop` | `done` | chat |

`turn` is a `turn` message, each finding a `finding`, `proposal` the finding itself (`proposal_kind` `finding`) or a later `propose` or `dismiss`, `fixed` a `fixed` message after the settle, and `done` a `done` message. A set with exactly one finding also writes its grouping with no drops and returns it beside the set. An ok record returns the stored record in the shape `/records` uses, and `already_stored`. `after` is the chat's highest `seq` when the record commits. The bus assigns it and ignores an `after` the caller sends. A fix has `settled_after`, its settle's `after`, instead, and a stop holds only `done`. A write that matches the stored record on its key, ignoring `after`, returns that record and `"already_stored": true` and writes nothing, so a retry after another message was sent still matches. Findings and drops compare as sets. A write that differs on the same key is refused with `transaction id reused with different content`. A repeated seq in `findings`, and a repeated `finding` in `drops`, are refused. A missing message or record, a message of the wrong kind, a finding already in another set, a drop that keeps itself or names another set, a `proposal_kind` that differs from the stored message, and a `fixed` used by another fix are one reason, `record breaks a reference, kind, or uniqueness rule`. Three rules have their own reasons: a kept finding is not itself dropped, a settle or a halt names a kept finding of a set that has a grouping, and a fix belongs to a settle whose proposal is not a `dismiss`. An ok `/records` returns `sets`, `groupings`, `settles`, `fixes`, `halts`, and `stops`, each in key order, and an empty list is `[]`.

### Conductor lease

`/lease` is the conductor lease: a claim that one conductor drives a chat now. It knows nothing about `run`, kinds, or records. `lease` selects the operation, and the chat must exist, or the call is refused with `chat not found`. An absent `lease` is refused with `lease is required`, and any value but these four with `lease must be acquire, renew, release, or get`. Every operation but `get` requires `holder`. An absent `holder` is refused with `holder is required`, and one outside the name grammar, `""` included, with `holder must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'`. `get` ignores a `holder` sent with it.

| `lease` | Effect |
| --- | --- |
| `acquire` | Succeeds when no live lease exists or this holder has the live lease. Refused with `chat has a conductor` when another holder's lease is live, with `remaining`, the whole seconds left on it, rounded up |
| `renew` | Extends this holder's live lease. Never creates one. Refused with `lease is not held` when no live lease exists or another holder has it |
| `release` | Removes this holder's lease. Releasing a lease this holder does not hold succeeds and changes nothing |
| `get` | Returns `held`, and when held, `since` and `expires` on the bus clock. Never returns the holder |

An ok `acquire` or `renew` returns `ttl`, the time to live in seconds, which is 20 and has no flag. Expiry is measured on the bus's monotonic clock and checked on each call. Leases live in bus memory, so a restarted bus holds none. They never touch the database, do not change `rev`, and do not wake a wait or watch. A closed chat can still be leased. There is no command for this route. The bus log holds each lease call, its selector, and its outcome, and never the holder.

### Fenced writes

A conductor write is a `/record` write, a `/member` write with a `status`, and a `/session` write with a `session`. Each requires `holder`, refused as on `/lease` when absent or malformed, and the holder must hold the chat's live lease. Otherwise the write is refused with `write requires the chat lease` and stores nothing, including a replay of a stored record, so a conductor that lost its lease learns it on its next write. `/send` takes an optional `holder`, checked and fenced by the same rules. A send without one is not checked, so members and `hotseat chat send` are unchanged. The lease check and the write happen under one lock, so no lease change falls between them. Reads ignore `holder`: `/records`, `/members`, a `/member` read, and a `/session` read. `/join` and `/close` are not fenced. A bus that fences refuses the conductor writes of an older `hotseat run` that sends no holder, so upgrade the bus between runs. The holder is absent from every read, `get`, the page, the log, and every status line.

### Outcomes

`refused` names the broken rule in `reason` and writes nothing. `unavailable` means the change could not be made durable and writes nothing.

```bash
curl -s localhost:4727/create -d '{"name":"job"}'
curl -s localhost:4727/send -d '{"chat":"job","from":"alice","to":["bob"],"body":"hello","txid":"1","kind":"say"}'
curl -s localhost:4727/wait -d '{"chat":"job","cursor":0,"name":"bob","deadline":"30s"}'
```
