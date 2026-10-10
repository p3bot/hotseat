# hotseat

One process hosts named conversations. Callers publish into a transcript and block until a matching message exists or a deadline passes.

```bash
hotseat bus start
hotseat bus start --store /var/lib/hotseat
hotseat bus start --store /var/lib/hotseat --listen 127.0.0.1:4727 --max-body 524288
hotseat bus start --store /var/lib/hotseat --listen 192.0.2.10:4727 --token-file /run/hotseat/token
hotseat bus status
hotseat bus stop
```

`hotseat bus start` runs the bus detached from the terminal and returns after the listener is bound and the database is open. It prints the pid, the listen address, and the store path. Closing the terminal does not stop the bus. The log is `hotseat.log` in the store directory. `hotseat bus status` asks a listen address whether the bus can serve. The default address is `127.0.0.1:4727`. It prints `health` as `pass`, `fail`, or `connection_failure`, and the address it contacted. When a `bus serve` process on this machine is bound to that address, it also prints that pid and the store directory that process holds. `pass` exits 0. `fail` and `connection_failure` exit 1. `hotseat bus stop` with no `--store` sends SIGTERM to the bus for the default store and to any other local `bus serve` bound to `127.0.0.1:4727`. It returns after those processes have exited and each `hotseat.db.lock` is released. A second stop, when those buses are already stopped, succeeds. A process that is not `bus serve` and holds `127.0.0.1:4727` is left running, and stop exits 1. `hotseat bus stop --store` signals only the bus for that directory.

`--store` selects the directory for `start` and for `stop`. Without `--store`, `stop` uses the default directory and also stops any other local `bus serve` on the default listen address. `status` does not take `--store`. It takes `--address`, and the default is `127.0.0.1:4727`. When `--store` is omitted the directory is `$XDG_DATA_HOME/hotseat`. An unset, empty, or relative `$XDG_DATA_HOME` uses `$HOME/.local/share/hotseat`. A relative home directory is refused and nothing is created. The database is `hotseat.db` in that directory. This process is the only writer, and it holds `hotseat.db.lock` beside the database until it exits. The default listen address is `127.0.0.1:4727`. A loopback address accepts every operation with no token. A hostname is resolved once and the process listens on one address from that lookup: a loopback address when every answer is loopback, otherwise a non-loopback address. Any other address requires `--token-file` and does not listen when the file is missing or empty, and no database is created. A failed bind creates no database. The file holds one shared token. A trailing newline is ignored. The token is printable ASCII with no spaces. The process does not log it. Passing the file on loopback does not turn the check on. `GET /health` is unauthenticated on every address. The body is `pass` or `fail`. The default maximum body is 512 KiB. One bus runs for each store directory. A second start prints the running process and does not change it.

## Protocol

Every operation is `POST` of one JSON object to `/<operation>`. A completed call responds `200` with one JSON object. Read `outcome`. It is `ok`, `timeout`, `refused`, or `unavailable`. A dropped connection has no body and is not a timeout. Unknown JSON fields are ignored.

A loopback listener ignores credentials. Any other listener requires the token on every operation, including from a peer on the same machine. The header is `Authorization: Bearer` and the token. `WriteToken` in package bus is that header. A missing token is refused with `token is required`. A wrong token is refused with `token does not match`. Nothing is written. The token does not select `from` and is not stored.

`GET /health` is not an operation. It takes no token on any address. The body is `pass` and status 200 when the bus can read the schema it serves, and `fail` and status 503 otherwise. A trailing newline is the only other byte. The body does not carry the pid, the listen address, the store path, or the token.

| Call | Body |
| --- | --- |
| `/create` | `{"name","task"?,"ticket"?,"seat"?,"directory"?,"prompts"?,"custom"?,"roster"?}` |
| `/publish` | `{"conversation","from","to","body","txid","kind"}` |
| `/read` | `{"conversation","cursor","limit","name"?,"kinds"?}` |
| `/wait` | `{"conversation","cursor","name"?,"deadline"?,"kinds"?}` |
| `/close` | `{"conversation"}` |
| `/list` | `{}` |
| `/register` | `{"conversation","name"}` |
| `/member` | `{"conversation","name","status"?,"pid"?}` |
| `/session` | `{"conversation","name"?,"session"?}` |
| `/members` | `{"conversation"}` |

`to` is an array. `[]` is empty. `["all"]` is the single value all. Any other array is an ordered name list and is not sorted or deduplicated. A different order is different content. `kind` uses the same character rules as a name. The bus stores that name and does not check its meaning, so `act` and any other legal name are stored. `kinds`, when present, is an array of those names. Omit it and read and wait match as they do without a kind filter. An empty array is refused. `deadline` is a Go duration such as `30s` or `500ms`, or an RFC3339 end time. Omit it to wait until a match or a dropped connection. A duration is measured from arrival. An end time is absolute on the bus clock. A time already past times out at once when nothing matches, and a retry sends that same time.

`cursor` `0` means the caller has seen no message. The bus does not store cursors or transaction ids except the txid recorded on an accepted message. The caller passes both on each call.

An ok create returns the conversation, including its binding. Omitted binding fields are stored empty. JSON always includes the keys: empty text is `""` and an empty list is `[]`. `prompts` and `roster` keep the caller's order. A roster name uses the name grammar. A duplicate roster name is refused. A roster requires a `seat` that is one of those names. A seat with an empty roster is refused. Empty roster and empty seat is a name-only create. An empty string inside `prompts` is refused. A name that already exists returns that conversation, its stored binding, and `"already_existed": true`, and writes nothing. Create does not insert member rows.

An ok publish returns the message, including `seq` and `kind`, and `conversation` with its binding, only after it is durable. `kind` is required. An attempt is the conversation, the sender, and `txid`. The bus requires that id and does not generate one. The same attempt with the same `kind`, the same `to`, and the same body returns that message and `"already_stored": true`, including after close, and writes nothing. The same attempt with a different `kind`, `to`, or body is refused and writes nothing. Another sender with that `txid` stores a new message. Close sets `status` to `closed` and still accepts a new message. That publish returns `status` `closed`. Publish, read, wait, and the member routes follow the same rules on a closed conversation.

An ok named read returns messages addressed to that name. `kinds` keeps only listed kinds. An ok unnamed read with `kinds` returns only listed kinds. The limit counts returned messages. An ok named wait returns every message after the cursor through the first addressed message of a listed kind, and `match_seq`. Omit `kinds` and the match is address alone. Messages of other kinds, and messages addressed to other names, stay in that span. An ok unnamed wait returns only the next message, or, when `kinds` is set, the next message of a listed kind. Timeout returns no messages. Close does not end a wait.

`/register` sets the registered time from the bus clock. The status becomes `registered` only when the row is `launched`. `running`, `exited`, `timed-out`, and `registered` keep their status. The name must be a roster name that already has a row. `/member` with `status` `launched` inserts that row. A second `launched` is refused. `running`, `exited`, and `timed-out` update an existing row. `pid`, when sent, is an integer greater than zero and is stored only with a status. A pid without a status is refused and writes nothing. `/member` with no `status` and no `pid` returns that row, including `pid`, and does not return a session id. `/members` returns name, launched time, registered time, and status, in roster order, with no session id and no pid. `/session` stores or returns session ids for the caller of that route. There is no command for it. Session ids and pids are absent from read, wait, list, and `/members`.

`refused` names the broken rule in `reason` and writes nothing. `unavailable` means the change could not be made durable and writes nothing.

```bash
curl -s localhost:4727/create -d '{"name":"job"}'
curl -s localhost:4727/publish -d '{"conversation":"job","from":"alice","to":["bob"],"body":"hello","txid":"1","kind":"say"}'
curl -s localhost:4727/wait -d '{"conversation":"job","cursor":0,"name":"bob","deadline":"30s"}'
```

## Client

The same binary calls a bus that is already running, then exits. The default address is `127.0.0.1:4727`. `--address` aims one invocation at another bus. `--token-file` reads the token for a non-loopback bus. The token is not a command-line argument. Omit the flag for loopback. The caller passes the cursor. Omit `--txid` and publish writes `txid: ` and 10 lowercase hex characters to stderr, then sends that id. Pass `--txid` to send that value, including an empty one. The line is written before the call and flushed. A dropped call is retried once with that same id. The client does not store the id.

```bash
hotseat create --name job
hotseat publish --conversation job --from alice --kind say --to bob --body hello
hotseat publish --conversation job --from alice --kind say --to bob --body hello --txid 1
hotseat publish --address 192.0.2.10:4727 --token-file /run/hotseat/token --conversation job --from alice --kind say --to bob --body hello --txid 1
hotseat publish --conversation job --from alice --kind say --to bob --body-file - --txid 1
hotseat read --conversation job --cursor 0 --limit 50
hotseat read --conversation job --cursor 0 --limit 50 --kind say --kind turn
hotseat wait --conversation job --cursor 0 --name bob --deadline 30s
hotseat close --conversation job
hotseat list
hotseat member register --conversation job --name alpha
hotseat member list --conversation job
```

`--kind` is required on publish. Repeat `--kind` on read or wait to send `kinds`. Omit it and the field is left out. `--body` is the message. `--body-file` reads it from a file, and `--body-file -` reads stdin, so a body can be larger than one command argument. Pass exactly one of the two. A body or a transaction id that is not valid UTF-8 is `refused` with that bus rule, and the client does not connect. `hotseat create` sends a name only. `hotseat list` and the create result print the binding, omitting empty fields, in roster order. `hotseat member list` prints name, launched time, registered time, and status. It does not print a pid or a session id.

Each command prints one text result and exits. `outcome` is `ok`, `timeout`, `refused`, `unavailable`, or `connection_failure`. A field on one line is `name: value`. A message body is always a byte count and then those bytes, and so is any other value that contains a newline. The count keeps an empty body, more than one line, and a line that looks like a field inside the value. A usage error is a message on stderr and no stdout. A dropped call is retried once with the same arguments. Timeout is only the bus outcome. A full read page carries each message `seq`. The client does not request the next page.

```text
outcome: ok
already_stored: false
conversation:
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

## Web

`hotseat web` serves a page that lists conversations, reads one transcript, and publishes. It calls a bus that is already running. It does not open the database. The default bus address is `127.0.0.1:4727`. The default page address is `127.0.0.1:4728`. The page listens on loopback. Any other listen address is refused. The page answers for the address it listens on. On loopback, localhost with that port is the same address. `--token-file` reads the token for a non-loopback bus. The page does not receive the token. Omit the flag for loopback. The person at the page supplies `from`, `to`, `kind`, the body, and the cursor. The publish result and the transcript show that `kind`. An empty transaction id is filled in the page when Publish is submitted. The server does not invent one and does not store the cursor or the id. An ok publish clears the field. Any other result leaves the posted id in the field. The page does not create, wait, close, or manage members.

```bash
hotseat web
hotseat web --address 127.0.0.1:4727 --listen 127.0.0.1:4728
hotseat web --address 192.0.2.10:4727 --token-file /run/hotseat/token
```

Build with `CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat`.
