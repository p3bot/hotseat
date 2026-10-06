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

`hotseat bus start` runs the bus detached from the terminal and returns after the listener is bound and the database is open. It prints the pid, the listen address, and the store path. Closing the terminal does not stop the bus. The log is `hotseat.log` in the store directory. `hotseat bus status` prints the running pid, listen address, and store path, or reports that the bus is not running. `hotseat bus stop` sends SIGTERM and returns after that process has exited and `hotseat.db.lock` is released. A second stop, when the bus is already stopped, succeeds.

`--store` selects the directory. When it is omitted the directory is `$XDG_DATA_HOME/hotseat`. An unset, empty, or relative `$XDG_DATA_HOME` uses `$HOME/.local/share/hotseat`. A relative home directory is refused and nothing is created. The database is `hotseat.db` in that directory. This process is the only writer, and it holds `hotseat.db.lock` beside the database until it exits. The default listen address is `127.0.0.1:4727`. A loopback address accepts every request with no token. A hostname is resolved once and the process listens on one address from that lookup: a loopback address when every answer is loopback, otherwise a non-loopback address. Any other address requires `--token-file` and does not listen when the file is missing or empty, and no database is created. A failed bind creates no database. The file holds one shared token. A trailing newline is ignored. The token is printable ASCII with no spaces. The process does not log it. Passing the file on loopback does not turn the check on. The default maximum body is 512 KiB. One bus runs for each store directory. A second start prints the running process and does not change it.

## Protocol

Every operation is `POST` of one JSON object to `/v1/<operation>`. A completed call responds `200` with one JSON object. Read `outcome`. It is `ok`, `timeout`, `refused`, or `unavailable`. A dropped connection has no body and is not a timeout. Unknown JSON fields are ignored.

A loopback listener ignores credentials. Any other listener requires the token on every request, including from a peer on the same machine. The header is `Authorization: Bearer` and the token. `WriteToken` in package bus is that header. A missing token is refused with `token is required`. A wrong token is refused with `token does not match`. Nothing is written. The token does not select `from` and is not stored.

| Call | Body |
| --- | --- |
| `/v1/create` | `{"name"}` |
| `/v1/publish` | `{"conversation","from","to","body","idempotency_key"}` |
| `/v1/read` | `{"conversation","cursor","limit","name"?}` |
| `/v1/wait` | `{"conversation","cursor","name"?,"deadline"?}` |
| `/v1/close` | `{"conversation"}` |
| `/v1/list` | `{}` |

`to` is an array. `[]` is empty. `["all"]` is the single value all. Any other array is an ordered name list and is not sorted or deduplicated. A different order is different content. `deadline` is a Go duration such as `30s` or `500ms`, or an RFC3339 end time. Omit it to wait until a match or a dropped connection. A duration is measured from arrival. An end time is absolute on the bus clock. A time already past times out at once when nothing matches, and a retry sends that same time.

`cursor` `0` means the caller has seen no message. The bus does not store cursors or idempotency keys except the key recorded on an accepted message. The caller passes both on each call.

An ok create returns the conversation. A name that already exists returns that conversation, its status, and `"already_existed": true`, and writes nothing. An ok publish returns the message, including `seq`, and `conversation` with `name` and `status`, only after it is durable. The same key with the same `from`, `to`, and body returns that message and `"already_stored": true`, including after close, and writes nothing. Close sets `status` to `closed` and still accepts a new message. That publish returns `status` `closed`. An ok named wait returns every message after the cursor through the match and `match_seq`. An ok unnamed wait returns only the next message. Timeout returns no messages. Close does not end a wait.

`refused` names the broken rule in `reason` and writes nothing. `unavailable` means the change could not be made durable and writes nothing.

```bash
curl -s localhost:4727/v1/create -d '{"name":"job"}'
curl -s localhost:4727/v1/publish -d '{"conversation":"job","from":"alice","to":["bob"],"body":"hello","idempotency_key":"1"}'
curl -s localhost:4727/v1/wait -d '{"conversation":"job","cursor":0,"name":"bob","deadline":"30s"}'
```

## Client

The same binary calls a bus that is already running, then exits. The default address is `127.0.0.1:4727`. `--address` aims one invocation at another bus. `--token-file` reads the token for a non-loopback bus. The token is not a command-line argument. Omit the flag for loopback. The caller passes the cursor and the idempotency key. The client does not store them and does not mint a key.

```bash
hotseat create --name job
hotseat publish --conversation job --from alice --to bob --body hello --idempotency-key 1
hotseat publish --address 192.0.2.10:4727 --token-file /run/hotseat/token --conversation job --from alice --to bob --body hello --idempotency-key 1
hotseat publish --conversation job --from alice --to bob --body-file - --idempotency-key 1
hotseat read --conversation job --cursor 0 --limit 50
hotseat wait --conversation job --cursor 0 --name bob --deadline 30s
hotseat close --conversation job
hotseat list
```

`--body` is the message. `--body-file` reads it from a file, and `--body-file -` reads stdin, so a body can be larger than one command argument. Pass exactly one of the two. A body or an idempotency key that is not valid UTF-8 is `refused` with that bus rule, and the client does not connect.

Each command prints one JSON object and exits. `outcome` is `ok`, `timeout`, `refused`, `unavailable`, or `connection_failure`. A usage error is a message on stderr and no object. A dropped call is retried once with the same arguments. Timeout is only the bus outcome. A full read page carries each message `seq`. The client does not request the next page.

## Web

`hotseat web` serves a page that lists conversations, reads one transcript, and publishes. It calls a bus that is already running. It does not open the database. The default bus address is `127.0.0.1:4727`. The default page address is `127.0.0.1:4728`. The page listens on loopback. Any other listen address is refused. The page answers for the address it listens on. On loopback, localhost with that port is the same address. `--token-file` reads the token for a non-loopback bus. The page does not receive the token. Omit the flag for loopback. The person at the page supplies `from`, `to`, the body, the cursor, and the idempotency key. The server does not store the cursor or the key and does not invent a key. The page does not create, wait, or close.

```bash
hotseat web
hotseat web --address 127.0.0.1:4727 --listen 127.0.0.1:4728
hotseat web --address 192.0.2.10:4727 --token-file /run/hotseat/token
```

Build with `CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat`.
