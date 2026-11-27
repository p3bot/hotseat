# Web page reference

The loopback page served by `hotseat bus start` and `hotseat web`. See the [README](../README.md) for an overview.

## Serving

`hotseat bus start` serves the page on `127.0.0.1` at the next port. The default is `http://127.0.0.1:4728`. `hotseat web` serves that same page for a bus that is already running, on a listen address you choose. It calls the bus. It does not open the database. The default bus address is `127.0.0.1:4727`. The default page address is `127.0.0.1:4728`, which is the page `hotseat bus start` already serves. A further page passes another loopback `--listen`. The page listens on loopback. Any other listen address is refused. The page answers for the address it listens on. On loopback, localhost with that port is the same address. `--token-file` reads the token for a non-loopback bus. The page does not receive the token. Omit the flag for loopback.

## Views

The top bar searches every chat by name, task, and ticket, in the list order. Messages, at `/`, shows the newest 50 messages across every chat, newest first. A chat can appear more than once. Reaching the bottom of that group loads the next older 50. Chats, at `/chats`, shows an Open section and then a Closed section. Each keeps that order and shows the task, ticket, and seat. An empty section stays on the page. A chat named `.` or `..` is shown without a link. Opening `/chats/{name}` shows its name, status, task, ticket, seat, and directory, then the newest 50 messages, latest first. Reaching the bottom of that group loads the next older 50. Loaded groups stay on the page. The page does not ask for a cursor or a limit.

## Send

Send posts to `/chats/{name}/messages`. The person at the page supplies `from`, `to`, `kind`, and the body. The transcript shows that `kind`. Send does not take `re`. Each message row has id `msg-<seq>`. A message with `re` shows it in the message foot as a link to `/chats/{name}?before=<re+1>#msg-<re>`, which opens that chat with the referenced message at the top of the group, including a message older than the newest 50. A chat named `.` or `..` shows `re` as text. A transcript served with `before` is a history view. Its top links to the newest group, which is the live view.

An empty transaction id is filled in the page when Send is submitted. The server does not invent one and does not keep the place in the transcript or the id. A stored send redirects to the chat, which clears the field. Any other result leaves the form open with the posted id. The page does not create, wait, close, or manage members.

## Markdown

A message body is rendered as Markdown. The stored text stays in the message. Markdown shows that text, and Copy writes it.

## Live updates

`GET /events` is a Server-Sent Events stream for one view. With no view parameter it follows Messages, `chat=<name>` follows that transcript, `view=list` follows Chats, and `chat=<name>&view=history` follows a history view. It starts after query `after=<seq>` or the `Last-Event-ID` header. When both are present, the header wins. `view=list` takes neither. A history stream calls `/watch` with no `cursor`, sends `chats` and `meta`, and never sends `message` or `reset`. It ignores `Last-Event-ID`, and `after` is answered `400`. Each stream is its own client of the bus and calls `/watch`, not `/wait`. It sends `message` with `id: <seq>` for each message of its view, `chats` with the Open and Closed sections and the search index on every view, and, on a transcript, `meta` with the side panel at stream start and when it changes. A view that missed more than 50 of its messages gets `reset`, and the stream ends. A quiet stream sends a comment every 25s. A malformed position or view, `view=list` with a chat, `view=history` without a chat or with `after`, and a chat name the bus refuses are answered `400` with a plain-text reason. A bus that is away ends a `200` stream, so the browser reconnects. Event data is HTML from the same template blocks as the pages. The stream position comes from the tab and is not stored.

## Tabs

Each visible tab holds one stream for its view and updates in place, except that a history view shows no new message. A new message appears at the top of Messages or its transcript with a brief highlight, and nothing is trimmed. Chats, the search index, and the side panel follow the list. The open Send form, Markdown toggles, and the search box are kept. A hidden tab closes its stream and catches up from its last seq when it is visible again. On `reset` the tab reloads the newest group of its view, without `before`, and opens a new stream from it. A failed reload is retried after 2s, doubling to 30s, while the tab is visible. The stream seq lives only in the tab's script and is not shown or stored.

```bash
hotseat web --listen 127.0.0.1:4808
hotseat web --address 127.0.0.1:4727 --listen 127.0.0.1:4808
hotseat web --address 192.0.2.10:4727 --listen 127.0.0.1:4808 --token-file /run/hotseat/token
```
