# hotseat

Put a team of AI agents in one chat and let them get the work done without you.

hotseat runs three or four coding agents, from different vendors where possible, on a single task: a ticket review, a pre-commit review, triage, or an implementation. Each member reads the work, raises what it finds, argues it out with the others, and applies the fix the group agrees on. You set the task. You hear back only when the team needs a decision (`ask`) or is finished (`done`).

- **Different models catch different mistakes.** A Claude and a Grok reviewing the same diff disagree in useful ways. hotseat turns that disagreement into a structured discussion instead of two reports for you to reconcile.
- **Agreement, not majority.** A finding settles only when every live member agrees with the current proposal. A finding nobody can agree on is escalated to you as a deadlock with a report. It is never quietly outvoted.
- **One finding at a time.** A fix can make a later finding obsolete, so findings are discussed and fixed in order rather than all at once.
- **Durable and resumable.** Every message is committed to SQLite before it is acknowledged. A run that stops, crashes, or loses its terminal resumes with `hotseat run continue <name>` from the transcript and the decisions it has recorded. Settled findings stay settled, and applied fixes are not applied twice.
- **Watchable.** Follow any chat live in your browser, tail it from the terminal, or ask for a one-screen status.
- **One static binary.** Pure Go with no cgo, an embedded SQLite driver, and nothing to deploy. The bus listens on loopback by default.

## How it works

```text
            you
             │  task                     ask / done
             ▼                                ▲
  ┌──────────────────────────────────────────────────┐
  │ hotseat run   (the conductor)                    │
  │   launches members, drives turns, runs the       │
  │   finding protocol, stops on done or a limit     │
  └───────┬───────────────┬───────────────┬──────────┘
          │ launch        │ launch        │ launch
          ▼               ▼               ▼
       alpha            bravo          charlie      agents via `start`
       (claude)         (grok)         (claude)
          │               │               │
          └──── send ─────┼──── send ─────┘
                          ▼
  ┌──────────────────────────────────────────────────┐
  │ hotseat bus   durable, ordered log of chats      │
  │   SQLite (WAL), single writer, HTTP on loopback  │
  └──────────────────────────────────────────────────┘
                          │
                          ▼
           web page · chat attend · chat status
```

- The bus is a durable, ordered log of named chats. It stores messages and knows nothing about what they mean, so it stays small and general.
- A member is an agent process. `run` launches it for one step, the member sends its messages and exits, and a later step resumes the same agent session. Members never sit idle waiting on the bus.
- `run` is the conductor, and it is deterministic. It decides who launches, in what order, and when the run stops. The members decide everything that needs judgement: what the work needs, whether a finding holds, and which option wins.
- The chat is the record. Anyone who reads it afterwards can see how the work went and why each decision was made.

### The finding protocol

Every review task uses the same protocol.

1. The members review in parallel. Each sends one `finding` per issue.
2. The acting seat groups the findings and drops duplicates.
3. Each finding is discussed in turn. Members `propose` a change, `dismiss` the finding, `agree`, `object`, or `stand` by a proposal. Each member has a budget of turns per finding.
4. When every live member agrees, the finding is settled and the member who raised it applies the fix.
5. A discussion that stalls goes to the acting seat, which grants more turns or declares a `deadlock`.
6. After the last finding, the team runs one satisfaction pass over the result and the seat posts `done`.

The run exits 0 only when every finding was settled and every agreed change was applied. Anything else exits non-zero and names the unsettled and unfixed findings by `seq`.

## Quickstart

You need the `hotseat` binary, and [`start`](https://github.com/p3bot/start) and [`tk`](https://github.com/p3bot/tk) on `PATH`. `start` launches each member, and the member modules are `hotseat/claude` and `hotseat/grok`.

```bash
hotseat bus start
hotseat run new hotseat/ticket/review --ticket foo-x234 \
  --agents "alpha=hotseat/claude:<model> bravo=hotseat/grok:<model>"
```

`run new` prints a header with the chat name and a link to the page before it starts work:

```text
chat: foo-x234-ticket-review
task: tasks:hotseat/ticket/review
ticket: foo-x234
seat: alpha
member: alpha=hotseat/claude:<model>
member: bravo=hotseat/grok:<model>
page: http://127.0.0.1:4728/chats/foo-x234-ticket-review
```

Open the page, or follow the chat from another terminal:

```bash
hotseat chat attend foo-x234-ticket-review   # prints ask, error, and done
hotseat chat status foo-x234-ticket-review   # one-screen state: members, findings, settled, fixed
```

When the team finishes, `run` prints its result and exits:

```text
done: 57
outcome: done
elapsed: 18m42s
```

If a run stops before it finishes, because of a limit, a signal, or a closed laptop, continue it by name. The stored roster, seat, and permission are reused:

```bash
hotseat run continue foo-x234-ticket-review
```

## Tasks

A task is a CUE module in the [p3bot library](https://github.com/p3bot/library), so new kinds of work are added there rather than in hotseat's code.

| Task | Work | Members |
| --- | --- | --- |
| `hotseat/ticket/triage` | Decide whether a ticket is ready for review: pass, stub, reject, or split | 1 or more |
| `hotseat/ticket/review` | Review a ticket document, settle and fix each finding by agreement, then a satisfaction pass | 2 or more |
| `hotseat/ticket/implement` | The acting seat implements the ticket and leaves it at `review` | 1 or more |
| `hotseat/pre-commit/review` | Review the pre-commit diff, settle and fix each finding by agreement, then a satisfaction pass | 2 or more |

A roster entry is `name=module:model`. The seat is the first name in ASCII byte order unless `--seat` names another.

To take one ticket through the whole pipeline, start a chief session. The chief runs triage, ticket review, implementation, and pre-commit review as four chats, and answers each `ask` itself:

```bash
start task hotseat/workflow/implement foo-x234
```

## Watching a run

- The page at `http://127.0.0.1:4728` is served by `hotseat bus start`. It shows the newest messages across every chat, live, with Markdown rendering, search, and a send box. It listens on loopback only.
- `hotseat chat attend <name>` waits as `owner` and prints each `ask`, `error`, and `done`. It exits on `done`.
- `hotseat chat status <name>` reads the bus and prints whether a conductor is driving the chat, each member's status, the finding under discussion, and how many findings are settled, fixed, and dropped.
- `hotseat run --verbose` writes one event line to stderr for each launch, exit, finding, stance, settle, and fix.

## The bus

```bash
hotseat bus start     # detach, print pid, address, store, and page URL
hotseat bus status    # health of the bus at an address
hotseat bus stop      # SIGTERM, then wait for the store lock to be released
```

`hotseat bus start` detaches from the terminal and returns once the listener, the database, and the page are ready. Closing the terminal does not stop it. The store defaults to `$XDG_DATA_HOME/hotseat` and holds `hotseat.db` and the log, `hotseat.log`. `--store` selects another directory for `start` and `stop`. `bus status` probes an address and does not take `--store`. `GET /health` answers `pass` or `fail` without a token.

The bus listens on `127.0.0.1:4727` by default. A non-loopback address requires `--token-file`, and every call must then carry the token.

Every `chat` command prints a text result: one `key: value` per line, with a multi-line value given as a byte count followed by those bytes. Agents can parse it reliably, and the exit status gives the outcome: ok 0, usage 1, refused 2, timeout 3, unavailable or unreachable 4.

```bash
hotseat chat create job
hotseat chat send job --from alice --kind say --to bob --body hello --txid 1
```

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

Each send carries a transaction id, so a retry never stores a message twice. A `wait` blocks until a matching message arrives or a deadline passes.

## Reference

The reference docs state exactly what hotseat does, including every outcome, refusal reason, and exit status.

| Document | Covers |
| --- | --- |
| [docs/bus.md](docs/bus.md) | The bus process, store, token, and HTTP protocol |
| [docs/chat.md](docs/chat.md) | `hotseat chat` verbs, text results, `status`, and `attend` |
| [docs/run.md](docs/run.md) | `hotseat run`: lease, launches, turns, the finding protocol, limits, stop results, and continuation |
| [docs/web.md](docs/web.md) | The loopback page and its live event stream |

Each command's `--help` covers its flags.

## Build

hotseat is pure Go and builds without cgo:

```bash
CGO_ENABLED=0 go build -o hotseat ./cmd/hotseat
```

## License

[MPL-2.0](LICENSE)
