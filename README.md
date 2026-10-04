# hotseat

One process hosts named conversations. Callers publish into a transcript and block until a matching message exists, a deadline passes, or the conversation is closed.

```bash
hotseat bus --store /var/lib/hotseat
hotseat bus --store /var/lib/hotseat --listen 127.0.0.1:4727 --max-body 524288
```

`--store` is required. The database is `hotseat.db` in that directory. This process is the only writer. The default listen address is `127.0.0.1:4727`. A non-loopback address is refused and no database is created. The default maximum body is 512 KiB. The process runs until it is signalled.

## Protocol

Every operation is `POST` of one JSON object to `/v1/<operation>`. A completed call responds `200` with one JSON object. Read `outcome`. It is `ok`, `timeout`, `closed`, `refused`, or `unavailable`. A dropped connection has no body and is not a timeout. Loopback clients send no token. Unknown JSON fields are ignored.

| Call | Body |
| --- | --- |
| `/v1/create` | `{"name"}` |
| `/v1/publish` | `{"conversation","from","to","body","idempotency_key"}` |
| `/v1/read` | `{"conversation","cursor","limit","name"?}` |
| `/v1/wait` | `{"conversation","cursor","name"?,"deadline"?}` |
| `/v1/close` | `{"conversation"}` |
| `/v1/list` | `{}` |

`to` is an array. `[]` is empty. `["all"]` is the single value all. Any other array is an ordered name list and is not sorted or deduplicated. A different order is different content. `deadline` is a Go duration such as `30s` or `500ms`. Omit it to wait until a match, a close, or a dropped connection.

`cursor` `0` means the caller has seen no message. The bus does not store cursors or idempotency keys except the key recorded on an accepted message. The caller passes both on each call.

An ok publish returns the message, including `seq`, only after it is durable. The same key with the same `from`, `to`, and body returns that message and `"already_stored": true`, including after close, and writes nothing. An ok named wait returns every message after the cursor through the match and `match_seq`. An ok unnamed wait returns only the next message. Timeout returns no messages. A closed named wait with no match returns the tail and no `match_seq`. A closed unnamed wait returns no messages.

`refused` names the broken rule in `reason` and writes nothing. `unavailable` means the change could not be made durable and writes nothing.

```bash
curl -s localhost:4727/v1/create -d '{"name":"job"}'
curl -s localhost:4727/v1/publish -d '{"conversation":"job","from":"alice","to":["bob"],"body":"hello","idempotency_key":"1"}'
curl -s localhost:4727/v1/wait -d '{"conversation":"job","cursor":0,"name":"bob","deadline":"30s"}'
```

## Client

The same binary calls a bus that is already running, then exits. The default address is `127.0.0.1:4727`. `--address` aims one invocation at another bus. The caller passes the cursor and the idempotency key. The client does not store them and does not mint a key.

```bash
hotseat create --name job
hotseat publish --conversation job --from alice --to bob --body hello --idempotency-key 1
hotseat publish --conversation job --from alice --to bob --body-file - --idempotency-key 1
hotseat read --conversation job --cursor 0 --limit 50
hotseat wait --conversation job --cursor 0 --name bob --deadline 30s
hotseat close --conversation job
hotseat list
```

`--body` is the message. `--body-file` reads it from a file, and `--body-file -` reads stdin, so a body can be larger than one command argument. Pass exactly one of the two. A body or an idempotency key that is not valid UTF-8 is `refused` with that bus rule, and the client does not connect.

Each command prints one JSON object and exits. `outcome` is `ok`, `timeout`, `closed`, `refused`, `unavailable`, or `connection_failure`. A usage error is a message on stderr and no object. A dropped call is retried once with the same arguments. Timeout is only the bus outcome. A full read page carries each message `seq`. The client does not request the next page.

Build with `CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat`.
