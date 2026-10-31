// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"html/template"
	"net/http"
	"time"

	"github.com/p3bot/hotseat/internal/client"
)

const (
	kindMessages     = "messages"
	kindList         = "list"
	kindConversation = "conversation"
)

type page struct {
	Kind          string
	Notice        string
	Open          []conversationRow
	Closed        []conversationRow
	Index         []indexEntry
	ListOutcome   string
	ListReason    string
	Name          string
	Status        string
	StatusKnown   bool
	Task          string
	Ticket        string
	Seat          string
	Directory     string
	PublishAction string
	Group         *messageGroup
	From          string
	ToText        string
	MessageKind   string
	Body          string
	TxID          string
	Publish       *publishView
	SendOpen      bool
	HTTPStatus    int
}

// conversationRow is one entry in the Open or Closed section.
type conversationRow struct {
	Name   string
	Href   string
	Task   string
	Ticket string
	Seat   string
}

// messageItem is one rendered message with the conversation it belongs to.
// Href is the conversation link. It is empty for a name that has none.
type messageItem struct {
	Conversation string
	Href         string
	Message      client.Message
}

type indexEntry struct {
	Name   string
	Status string
	Task   string
	Ticket string
	Href   string
}

type messageGroup struct {
	Outcome  string
	Reason   string
	Messages []messageItem
	Full     bool
	NextPage string
}

type publishView struct {
	Outcome string
	Reason  string
}

// TabTitle is the browser tab label. Messages is the product name.
// The conversation list and a transcript add a suffix so two tabs stay distinct.
func (p page) TabTitle() string {
	switch {
	case p.Name != "":
		return "Hotseat - " + p.Name
	case p.Kind == kindList:
		return "Hotseat - Conversations"
	default:
		return "Hotseat"
	}
}

func (s Server) render(w http.ResponseWriter, p page) {
	setPageHeaders(w)
	code := http.StatusOK
	if p.HTTPStatus != 0 {
		code = p.HTTPStatus
	}
	w.WriteHeader(code)
	if err := pageTmpl.Execute(w, p); err != nil {
		s.logger().Error("write page", "err", err)
	}
}

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The body holds the transcript and, after a failed publish, the posted id.
	w.Header().Set("Cache-Control", "no-store")
	// Inline script fills an empty txid on Publish. default-src none would block it.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// same-origin keeps a cross-site request from carrying the cursor in Referer.
	// no-referrer would send Origin: null for this page's own POST.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
}

var pageTmpl = template.Must(template.New("page").Funcs(template.FuncMap{
	"viewTime":    viewTime,
	"renderBody":  renderBody,
	"streamRetry": func() int { return streamRetry },
}).Parse(pageSource))

// viewTime renders a bus timestamp for reading.
// The zone is the machine running the page. An unparsed value is shown unchanged.
func viewTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return parsed.In(time.Local).Format("2006-01-02 15:04:05.000")
}

const pageSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.TabTitle}}</title>
<style>
:root {
  color-scheme: light;
  --bg: #f6f4ef;
  --fg: #1c1917;
  --muted: #57534e;
  --line: #d6d3d1;
  --card: #fffdf8;
  --ticket: #fff;
  --accent: #1d4ed8;
  --ok: #166534;
  --issues: #9f1239;
  --banner: #fff1f2;
  --arrived: color-mix(in srgb, var(--accent) 16%, #fff);
}
body { margin: 0; height: 100vh; overflow: hidden; font-family: ui-sans-serif, system-ui, sans-serif; line-height: 1.5; background: var(--bg); color: var(--fg); display: grid; grid-template-columns: 13rem minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); grid-template-areas: "bar bar" "nav main"; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
body.has-meta { grid-template-columns: 13rem minmax(0, 1fr) 20rem; grid-template-areas: "bar bar bar" "nav main meta"; }
.topbar { grid-area: bar; display: flex; align-items: center; gap: 1rem; padding: 0.55rem 1rem; border-bottom: 1px solid var(--line); background: var(--ticket); }
.brand { color: var(--fg); text-decoration: none; font-weight: 650; }
.brand:hover { text-decoration: none; }
.search { margin-left: auto; position: relative; width: min(24rem, 46vw); }
.sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); border: 0; }
label.sr { display: inline; margin: 0; }
input, textarea, button, #search { font: inherit; color: var(--fg); background: var(--ticket); }
input, textarea, #search { width: 100%; box-sizing: border-box; padding: 0.3rem 0.5rem; border: 1px solid var(--line); border-radius: 4px; background: var(--bg); }
#search-results { position: absolute; z-index: 2; top: calc(100% + 0.25rem); left: 0; right: 0; margin: 0; padding: 0.25rem 0; list-style: none; background: var(--ticket); border: 1px solid var(--line); border-radius: 4px; max-height: 18rem; overflow: auto; }
#search-results[hidden] { display: none; }
#search-results li { display: flex; justify-content: space-between; gap: 0.75rem; align-items: baseline; padding: 0 0.7rem; }
#search-results a { padding: 0.3rem 0; }
.status { font-size: 0.75rem; font-weight: 650; color: var(--muted); }
.status[data-status="open"] { color: var(--ok); }
.status[data-status="closed"] { color: var(--muted); }
nav { grid-area: nav; min-height: 0; overflow: auto; padding: 0.75rem; border-right: 1px solid var(--line); background: var(--ticket); }
nav a { display: block; margin: 0 0 0.4rem; padding: 0.5rem 0.7rem; border: 1px solid color-mix(in srgb, var(--accent) 30%, var(--line)); border-radius: 6px; background: color-mix(in srgb, var(--accent) 8%, #fff); color: var(--accent); font-weight: 650; text-decoration: none; }
nav a:hover { background: color-mix(in srgb, var(--accent) 16%, #fff); border-color: color-mix(in srgb, var(--accent) 50%, var(--line)); text-decoration: none; }
nav a[aria-current="page"] { background: var(--accent); border-color: var(--accent); color: #fff; }
nav a[aria-current="page"]:hover { background: color-mix(in srgb, var(--accent) 88%, #000); border-color: color-mix(in srgb, var(--accent) 88%, #000); }
main { grid-area: main; min-width: 0; min-height: 0; overflow: auto; padding: 1.25rem 1.5rem 1.5rem; }
body:not(.has-meta) main { max-width: 46rem; }
body.has-meta main { display: flex; flex-direction: column; overflow: hidden; }
body.has-meta #transcript-section { flex: 1 1 auto; min-height: 0; overflow: auto; margin-right: -1.5rem; margin-bottom: -1.5rem; padding: 0 1.25rem 1.5rem 0; }
.conv-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; flex: none; }
.conv-head h1 { margin: 0 0 2rem; }
#conversation-meta { grid-area: meta; min-width: 0; min-height: 0; overflow: auto; padding: 1.25rem 1rem 2rem; border-left: 1px solid var(--line); background: var(--ticket); }
.binding { display: grid; gap: 0.9rem; margin: 0; }
.binding div { display: grid; gap: 0.15rem; min-width: 0; }
.binding dt { color: var(--fg); font-weight: 650; font-size: 1.05rem; }
.binding dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
#conversation-meta .status { font-size: 1rem; }
#send-pen { border: 1px solid var(--accent); background: var(--accent); color: #fff; border-radius: 4px; padding: 0.4rem; line-height: 0; }
#send-pen:hover { background: color-mix(in srgb, var(--accent) 88%, #000); border-color: color-mix(in srgb, var(--accent) 88%, #000); }
#send-pen svg { width: 1.15rem; height: 1.15rem; display: block; }
label { display: grid; gap: 0.25rem; margin: 0.75rem 0; }
button { padding: 0.3rem 0.7rem; border: 1px solid var(--line); border-radius: 4px; background: var(--bg); cursor: pointer; }
button:hover { border-color: var(--accent); }
#publish-button { background: var(--accent); border-color: var(--accent); color: #fff; }
#publish-button:hover { background: color-mix(in srgb, var(--accent) 88%, #000); border-color: color-mix(in srgb, var(--accent) 88%, #000); }
.dialog-actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
dialog { border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg); padding: 1rem 1.15rem 1.15rem; width: min(34rem, calc(100vw - 2rem)); }
dialog input, dialog textarea, dialog button { background: var(--ticket); }
dialog::backdrop { background: rgb(28 25 23 / 0.45); }
#transcript { list-style: none; margin: 0; padding: 0; }
#transcript .where { display: none; }
.message { background: var(--ticket); border: 1px solid var(--line); border-radius: 6px; padding: 0.3rem 0.75rem; margin: 0 0 1.2rem; box-shadow: 0 1px 2px rgb(28 25 23 / 0.08), 0 4px 12px rgb(28 25 23 / 0.12); }
#more { height: 1px; }
#group-error, #list-error, #publish-reason { color: var(--issues); }
#group-error:empty { display: none; }
#notice, #list-error, #publish-reason { background: var(--banner); padding: 0.35rem 0.55rem; border-radius: 4px; }
.meta-end { display: flex; align-items: center; gap: 0.75rem; margin-left: auto; flex: none; }
.msg-actions { display: flex; align-items: center; gap: 0.35rem; flex: none; }
.msg-actions .markdown,
.msg-actions .copy { width: 1.7rem; height: 1.7rem; padding: 0; display: grid; place-items: center; line-height: 0; border-radius: 4px; flex: none; }
.msg-actions .markdown svg { width: 1.2rem; height: 0.61rem; display: block; }
.msg-actions .copy svg { width: 1rem; height: 1rem; display: block; }
.msg-actions .markdown[aria-pressed="true"] { background: var(--accent); border-color: var(--accent); color: #fff; }
.msg-actions .markdown[aria-pressed="true"]:hover { background: color-mix(in srgb, var(--accent) 88%, #000); border-color: color-mix(in srgb, var(--accent) 88%, #000); }
.msg-actions .copy .icon-check { display: none; }
.msg-actions .copy.copied .icon-copy { display: none; }
.msg-actions .copy.copied .icon-check { display: block; }
.body { margin: 1rem 0 0; overflow-wrap: anywhere; }
.rendered { overflow-x: auto; font-size: 1.125rem; }
.rendered > :first-child { margin-top: 0; }
.rendered > :last-child { margin-bottom: 0; }
.rendered p, .rendered ul, .rendered ol, .rendered pre, .rendered blockquote, .rendered h1, .rendered h2, .rendered h3, .rendered h4, .rendered h5, .rendered h6 { margin: 0.55rem 0; }
.rendered h1, .rendered h2, .rendered h3, .rendered h4, .rendered h5, .rendered h6 { font-size: 1.1em; line-height: 1.4; }
.rendered ul, .rendered ol { padding-left: 1.3rem; }
.rendered blockquote { margin-left: 0; padding-left: 0.75rem; border-left: 3px solid var(--line); }
.rendered pre, .rendered code { font-family: ui-monospace, monospace; }
.rendered pre { white-space: pre-wrap; overflow-wrap: anywhere; background: var(--bg); padding: 0.55rem 0.7rem; border-radius: 4px; }
.rendered :not(pre) > code { background: var(--bg); padding: 0.05rem 0.3rem; border-radius: 3px; }
.table-scroll { overflow-x: auto; max-width: 100%; }
.rendered table { border-collapse: collapse; }
.rendered th, .rendered td { border: 1px solid var(--line); padding: 0.15rem 0.45rem; vertical-align: top; text-align: left; }
pre.body.source { white-space: pre-wrap; overflow-wrap: anywhere; margin: 1rem 0 0; font-family: ui-monospace, monospace; }
.hint, .meta { font-size: 0.875rem; color: var(--muted); }
.meta { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: flex-start; gap: 0.25rem 1rem; margin: 0; }
.meta .who { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; }
.meta .from { display: inline-block; min-width: 12ch; max-width: 100%; margin-right: 0.75rem; color: var(--fg); overflow-wrap: anywhere; vertical-align: baseline; }
.meta .to { overflow-wrap: anywhere; color: var(--fg); }
.meta .when { white-space: nowrap; font-variant-numeric: tabular-nums; }
.foot { display: flex; justify-content: space-between; align-items: center; gap: 1rem; margin: 1rem 0 0.15rem; font-size: 0.875rem; color: var(--muted); }
.foot .seq-n { color: var(--fg); font-variant-numeric: tabular-nums; }
.kind { display: inline-block; padding: 0.05rem 0.55rem; border: 1px solid var(--line); border-radius: 999px; background: var(--bg); color: var(--muted); font-size: 0.75rem; font-weight: 650; line-height: 1.4; text-transform: capitalize; }
.kind[data-kind="error"] { color: var(--issues); background: var(--banner); border-color: color-mix(in srgb, var(--issues) 35%, var(--line)); }
.kind[data-kind="instruction"], .kind[data-kind="instructions"] { color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, #fff); border-color: color-mix(in srgb, var(--accent) 35%, var(--line)); }
.where { margin: 0 0 0.35rem; font-weight: 650; }
#messages { list-style: none; margin: 0; padding: 0; }
.conv-group { margin: 0 0 2rem; }
.conv-group h2 { display: flex; align-items: center; gap: 0.5rem; margin: 0 0 0.75rem; font-size: 1.15rem; }
.conv-group .count { padding: 0.05rem 0.55rem; border: 1px solid var(--line); border-radius: 999px; background: var(--ticket); color: var(--muted); font-size: 0.75rem; font-weight: 650; line-height: 1.4; font-variant-numeric: tabular-nums; }
#open-section .count { color: var(--ok); border-color: color-mix(in srgb, var(--ok) 35%, var(--line)); }
.conv-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 0.6rem; }
.conv { position: relative; padding: 0.6rem 0.85rem; border: 1px solid var(--line); border-radius: 6px; background: var(--ticket); box-shadow: 0 1px 2px rgb(28 25 23 / 0.08), 0 4px 12px rgb(28 25 23 / 0.12); }
.conv:has(a):hover { border-color: color-mix(in srgb, var(--accent) 50%, var(--line)); }
.conv-name { margin: 0; font-weight: 650; overflow-wrap: anywhere; }
.conv-name a::after { content: ""; position: absolute; inset: 0; border-radius: 6px; }
.conv-name a:hover { text-decoration: none; }
.conv-binding { position: relative; z-index: 1; display: flex; flex-wrap: wrap; gap: 0.15rem 1.25rem; margin: 0.25rem 0 0; font-size: 0.875rem; color: var(--muted); }
.conv-binding div { display: flex; gap: 0.35rem; min-width: 0; }
.conv-binding dd { margin: 0; color: var(--fg); overflow-wrap: anywhere; }
#closed-section .conv { background: transparent; box-shadow: none; }
#closed-section .conv-name a { color: color-mix(in srgb, var(--accent) 70%, var(--muted)); }
.conv-empty { margin: 0; color: var(--muted); }
.arrived { animation: arrived 2.4s ease-out; }
@keyframes arrived { from { background-color: var(--arrived); } }
@keyframes arrived-still { from, to { background-color: var(--arrived); } }
@media (prefers-reduced-motion: reduce) {
  .arrived { animation-name: arrived-still; animation-timing-function: linear; }
}
@media (max-width: 960px) {
  body, body.has-meta { height: auto; min-height: 100vh; overflow: visible; grid-template-columns: 1fr; grid-template-rows: auto; grid-template-areas: "bar" "nav" "meta" "main"; }
  main, body.has-meta main, body.has-meta #transcript-section { display: block; overflow: visible; }
  body.has-meta #transcript-section { margin: 0; padding: 0; }
  nav { border-right: 0; border-bottom: 1px solid var(--line); }
  #conversation-meta { border-left: 0; border-bottom: 1px solid var(--line); }
  .search { width: min(16rem, 50vw); }
}
</style>
</head>
<body{{if eq .Kind "conversation"}} class="has-meta"{{end}}>
<header class="topbar">
<a class="brand" href="/">Hotseat</a>
<div class="search">
<label class="sr" for="search">Search</label>
<input id="search" type="search" placeholder="Search conversations" autocomplete="off">
<ul id="search-results" hidden></ul>
</div>
</header>
<nav>
<a href="/" id="nav-messages"{{if eq .Kind "messages"}} aria-current="page"{{end}}>Messages</a>
<a href="/conversations" id="nav-conversations"{{if eq .Kind "list"}} aria-current="page"{{end}}>Conversations</a>
</nav>
<main id="main">
{{if .Notice}}<p id="notice">{{.Notice}}</p>{{end}}
{{if eq .Kind "messages"}}
<h1>Messages</h1>
{{if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else if .Group}}{{if ne .Group.Outcome "ok"}}<p id="read-outcome">{{.Group.Outcome}}</p>{{if .Group.Reason}}<p id="read-reason">{{.Group.Reason}}</p>{{end}}
{{else}}{{if eq (len .Group.Messages) 0}}<p id="empty">No messages.</p>
{{else}}<ol id="messages">
{{range .Group.Messages}}{{template "messageItem" .}}{{end}}</ol>
{{end}}{{template "olderMarker" .Group}}{{end}}{{end}}
<p id="group-error"></p>
{{else if eq .Kind "list"}}
<h1>Conversations</h1>
{{if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else if not .Notice}}{{template "convSections" .}}{{end}}
{{else if eq .Kind "conversation"}}
<header class="conv-head">
<h1>{{.Name}}</h1>
<button type="button" id="send-pen" aria-label="Send" title="Send">
<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z"/></svg>
</button>
</header>
{{if .Publish}}<section id="publish-result">
<p id="publish-outcome">{{.Publish.Outcome}}</p>
{{if .Publish.Reason}}<p id="publish-reason">{{.Publish.Reason}}</p>{{end}}
</section>{{end}}
<section id="transcript-section">
{{if .Group}}{{if ne .Group.Outcome "ok"}}<p id="read-outcome">{{.Group.Outcome}}</p>
{{if .Group.Reason}}<p id="read-reason">{{.Group.Reason}}</p>{{end}}
{{end}}{{if and .Group (eq .Group.Outcome "ok")}}{{if eq (len .Group.Messages) 0}}<p id="transcript-empty">No messages.</p>
{{else}}<ol id="transcript">
{{range .Group.Messages}}{{template "messageItem" .}}{{end}}</ol>
{{end}}{{template "olderMarker" .Group}}{{end}}{{end}}
<p id="group-error"></p>
</section>
<form id="send-form" method="post" action="{{.PublishAction}}">
<dialog id="send-dialog"{{if .SendOpen}} data-send-open{{end}}>
<h2>Send</h2>
<label for="from">From <input id="from" name="from" value="{{.From}}" autocomplete="off"></label>
<label for="to">To <textarea id="to" name="to" rows="3">{{.ToText}}</textarea></label>
<p class="hint">One name per line. Empty sends no addressee. Order is kept.</p>
<label for="kind">Kind <input id="kind" name="kind" value="{{.MessageKind}}" autocomplete="off"></label>
<label for="body">Body <textarea id="body" name="body" rows="6">{{.Body}}</textarea></label>
<label for="txid">Transaction id <input id="txid" name="txid" value="{{.TxID}}" autocomplete="off"></label>
<div class="dialog-actions">
<button type="button" id="send-cancel">Cancel</button>
<button type="submit" id="publish-button">Send</button>
</div>
<p class="hint">An empty id is filled here when you send. The server does not invent one. A stored send clears this field.</p>
</dialog>
</form>
{{end}}
</main>
{{if eq .Kind "conversation"}}{{template "convMeta" .}}{{end}}
{{template "convIndex" .}}
<script>
(function () {
  var mainEl = document.getElementById("main");
  var box = document.getElementById("search");
  var results = document.getElementById("search-results");
  function trim(s) { return s.replace(/^\s+/, "").replace(/\s+$/, ""); }
  function indexItems() {
    var index = document.getElementById("conversation-index");
    var items = [];
    if (!index) return items;
    var rows = index.children;
    for (var n = 0; n < rows.length; n++) {
      items.push({
        name: rows[n].getAttribute("data-name") || "",
        status: rows[n].getAttribute("data-status") || "",
        task: rows[n].getAttribute("data-task") || "",
        ticket: rows[n].getAttribute("data-ticket") || "",
        href: rows[n].getAttribute("data-href") || ""
      });
    }
    return items;
  }
  function hit(item, q) {
    return item.name.toLowerCase().indexOf(q) >= 0 || item.task.toLowerCase().indexOf(q) >= 0 || item.ticket.toLowerCase().indexOf(q) >= 0;
  }
  function renderSearch() {
    var q = trim(box.value).toLowerCase();
    while (results.firstChild) results.removeChild(results.firstChild);
    if (q === "") { results.hidden = true; return; }
    var items = indexItems();
    var shown = 0;
    for (var i = 0; i < items.length; i++) {
      if (!hit(items[i], q)) continue;
      var li = document.createElement("li");
      var link = document.createElement(items[i].href ? "a" : "span");
      if (items[i].href) link.href = items[i].href;
      link.textContent = items[i].name;
      var st = document.createElement("span");
      st.className = "status";
      st.setAttribute("data-status", items[i].status);
      st.textContent = items[i].status;
      li.appendChild(link);
      li.appendChild(st);
      results.appendChild(li);
      shown++;
    }
    if (shown === 0) {
      var empty = document.createElement("li");
      empty.id = "search-empty";
      empty.textContent = "No matches.";
      results.appendChild(empty);
    }
    results.hidden = false;
  }
  if (box && results) {
    box.addEventListener("input", renderSearch);
    box.addEventListener("keydown", function (event) {
      if (event.key !== "Enter") return;
      var link = results.querySelector("a");
      if (!link) return;
      event.preventDefault();
      window.location = link.href;
    });
  }
  var form = document.getElementById("send-form");
  var publish = document.getElementById("publish-button");
  if (form && publish) {
    form.addEventListener("submit", function (event) {
      if (event.submitter !== publish) return;
      var field = document.getElementById("txid");
      if (!field || field.value !== "") return;
      var bytes = new Uint8Array(5);
      crypto.getRandomValues(bytes);
      var hex = "";
      for (var i = 0; i < bytes.length; i++) {
        var h = bytes[i].toString(16);
        if (h.length < 2) h = "0" + h;
        hex += h;
      }
      field.value = hex;
    });
  }
  var pen = document.getElementById("send-pen");
  var dialog = document.getElementById("send-dialog");
  function openSend() {
    if (!dialog || typeof dialog.showModal !== "function" || dialog.open) return;
    dialog.showModal();
  }
  if (dialog && dialog.hasAttribute("data-send-open")) openSend();
  if (pen) pen.addEventListener("click", openSend);
  var cancel = document.getElementById("send-cancel");
  if (cancel && dialog && typeof dialog.close === "function") {
    cancel.addEventListener("click", function () { dialog.close(); });
  }
  if (mainEl) mainEl.addEventListener("click", onMessageClick);
  function hasClass(node, name) {
    return node && node.classList && node.classList.contains(name);
  }
  function ancestor(node, name) {
    while (node && node !== mainEl) {
      if (hasClass(node, name)) return node;
      node = node.parentNode;
    }
    return null;
  }
  function part(msg, tag, name) {
    if (!msg || !msg.getElementsByTagName) return null;
    var nodes = msg.getElementsByTagName(tag);
    for (var i = 0; i < nodes.length; i++) if (hasClass(nodes[i], name)) return nodes[i];
    return null;
  }
  function setHidden(el, on) {
    if (!el) return;
    el.hidden = on;
    if (on) el.setAttribute("hidden", "");
    else el.removeAttribute("hidden");
  }
  function toggleMarkdown(btn) {
    var msg = ancestor(btn, "message");
    var on = btn.getAttribute("aria-pressed") === "true";
    btn.setAttribute("aria-pressed", on ? "false" : "true");
    setHidden(part(msg, "div", "rendered"), !on);
    setHidden(part(msg, "pre", "source"), on);
  }
  function copyMarkdown(btn) {
    var pre = part(ancestor(btn, "message"), "pre", "source");
    var text = pre ? pre.textContent : "";
    function copyName(text) {
      btn.setAttribute("aria-label", text);
      btn.setAttribute("title", text);
    }
    var fail = function () {
      btn.classList.remove("copied");
      copyName("Copy failed");
    };
    var clip = typeof navigator === "undefined" ? null : navigator.clipboard;
    if (!clip || !clip.writeText) { fail(); return; }
    var pending;
    try { pending = clip.writeText(text); } catch (err) { fail(); return; }
    if (!pending || !pending.then) { fail(); return; }
    pending.then(function () {
      btn.classList.add("copied");
      copyName("Copied");
      if (btn._copyTimer) clearTimeout(btn._copyTimer);
      btn._copyTimer = setTimeout(function () {
        btn.classList.remove("copied");
        copyName("Copy markdown");
      }, 1500);
    }, fail);
  }
  function onMessageClick(event) {
    var copy = ancestor(event.target, "copy");
    if (copy) { copyMarkdown(copy); return; }
    var markdown = ancestor(event.target, "markdown");
    if (markdown) toggleMarkdown(markdown);
  }

  var view = pageView(location.pathname);
  if (!view) return;
  function pageView(path) {
    if (path === "/") return { list: "messages", empty: "empty", region: "main", url: "/", stream: "/events?", loadNotice: true };
    if (path === "/conversations") return { sections: true, stream: "/events?view=list", loadNotice: true };
    var m = /^\/conversations\/([^\/]+)(\/messages)?$/.exec(path);
    if (!m) return null;
    var name;
    try { name = decodeURIComponent(m[1]); } catch (err) { return null; }
    return {
      meta: true,
      list: "transcript",
      empty: "transcript-empty",
      region: "transcript-section",
      url: "/conversations/" + m[1],
      stream: "/events?conversation=" + encodeURIComponent(name) + "&",
      // After a failed Send the notice explains the send, which a reload does not undo.
      loadNotice: !m[2]
    };
  }
  var olderFailed = "older messages did not load";
  var newestFailed = "messages did not load";
  function parseHTML(html) {
    return new DOMParser().parseFromString(html, "text/html");
  }
  function fetchPage(url) {
    return fetch(url).then(function (res) {
      return res.text().then(function (html) { return { ok: res.ok, doc: parseHTML(html) }; });
    });
  }
  function clearLoadNotice() {
    var notice = document.getElementById("notice");
    if (view.loadNotice && notice) notice.parentNode.removeChild(notice);
  }
  function showGroupError(text) {
    var el = document.getElementById("group-error");
    if (el) el.textContent = text;
  }
  function groupOk(doc) {
    var outcome = doc.getElementById("read-outcome");
    if (outcome && outcome.textContent !== "ok") return false;
    return !!(doc.getElementById(view.list) || doc.getElementById(view.empty));
  }
  function groupFailure(doc, fallback) {
    var notice = doc.getElementById("notice");
    if (notice && notice.textContent) return notice.textContent;
    var outcome = doc.getElementById("read-outcome");
    if (outcome && outcome.textContent && outcome.textContent !== "ok") {
      var reason = doc.getElementById("read-reason");
      if (reason && reason.textContent) return outcome.textContent + " " + reason.textContent;
      return outcome.textContent;
    }
    return fallback;
  }

  var older = null;
  var observed = null;
  // The marker whose group is loading, so a load for a replaced marker cannot block the new one.
  var loading = null;
  function observeMore() {
    var more = document.getElementById("more");
    if (!more || !more.getAttribute("data-next") || more === observed) return;
    if (!older) {
      older = new IntersectionObserver(function (entries) {
        for (var i = 0; i < entries.length; i++) if (entries[i].isIntersecting) loadOlder();
      });
    }
    if (observed) older.unobserve(observed);
    observed = more;
    older.observe(more);
  }
  function inView(el) {
    var r = el.getBoundingClientRect();
    return r.top < window.innerHeight && r.bottom > 0;
  }
  function appendOlder(list, more, doc) {
    var nextList = doc.getElementById(view.list);
    if (nextList) {
      var items = nextList.querySelectorAll("li.message");
      for (var i = 0; i < items.length; i++) list.appendChild(document.importNode(items[i], true));
    }
    var nextMore = doc.getElementById("more");
    if (nextMore && nextMore.getAttribute("data-next")) more.setAttribute("data-next", nextMore.getAttribute("data-next"));
    else more.removeAttribute("data-next");
  }
  function loadOlder() {
    var more = document.getElementById("more");
    var list = document.getElementById(view.list);
    var url = more && more.getAttribute("data-next");
    if (!url || !list || loading === more) return;
    loading = more;
    // A reset reload may have replaced the region, and this result belongs to the old one.
    function current() {
      if (loading === more) loading = null;
      return document.getElementById("more") === more;
    }
    fetchPage(url).then(function (got) {
      if (!current()) return;
      if (!got.ok || !groupOk(got.doc)) { showGroupError(groupFailure(got.doc, olderFailed)); return; }
      showGroupError("");
      appendOlder(list, more, got.doc);
      if (more.getAttribute("data-next") && inView(more)) loadOlder();
    }).catch(function () {
      if (current()) showGroupError(olderFailed);
    });
  }
  if (view.list) observeMore();

  var anchoring = CSS.supports("overflow-anchor", "auto");
  function scrollerOf(el) {
    for (var node = el.parentNode; node && node.nodeType === 1; node = node.parentNode) {
      var y = getComputedStyle(node).overflowY;
      if (y === "auto" || y === "scroll" || y === "overlay") return node;
    }
    return document.scrollingElement || document.documentElement;
  }
  function markArrived(el) {
    el.classList.add("arrived");
    el.addEventListener("animationend", function done(event) {
      if (event.target !== el) return;
      el.classList.remove("arrived");
      el.removeEventListener("animationend", done);
    });
  }
  function seqOf(el) {
    var n = Number(el.getAttribute("data-seq"));
    return n > 0 ? n : 0;
  }
  function pageSeq() {
    var list = document.getElementById(view.list);
    var top = 0;
    if (!list) return top;
    var items = list.children;
    for (var i = 0; i < items.length; i++) if (seqOf(items[i]) > top) top = seqOf(items[i]);
    return top;
  }
  function listed(list, n) {
    var items = list.children;
    for (var i = 0; i < items.length; i++) if (seqOf(items[i]) === n) return true;
    return false;
  }
  function insertMessage(list, empty, item) {
    var scroller = scrollerOf(list || empty);
    var top = scroller.scrollTop;
    var height = scroller.scrollHeight;
    if (!list) {
      list = document.createElement("ol");
      list.id = view.list;
      empty.parentNode.replaceChild(list, empty);
    }
    list.insertBefore(item, list.firstChild);
    markArrived(item);
    if (!anchoring && top > 0) scroller.scrollTop = top + scroller.scrollHeight - height;
  }

  var source = null;
  var seq = view.list ? pageSeq() : 0;
  var reloading = false;
  var reloadDue = false;
  function openStream() {
    if (source || reloading || document.visibilityState === "hidden") return;
    source = new EventSource(view.list ? view.stream + "after=" + seq : view.stream);
    source.addEventListener("conversations", applyConversations);
    if (view.meta) source.addEventListener("meta", applyMeta);
    if (view.list) {
      source.addEventListener("message", applyMessage);
      source.addEventListener("reset", reset);
    }
  }
  function closeStream() {
    if (!source) return;
    source.close();
    source = null;
  }
  // A group that failed at load has neither its list nor its placeholder.
  function groupMissing() {
    return !document.getElementById(view.list) && !document.getElementById(view.empty);
  }
  function applyMessage(event) {
    var item = parseHTML(event.data).querySelector("li.message");
    if (!item) return;
    var n = seqOf(item);
    if (n > seq) seq = n;
    if (groupMissing()) { reset(); return; }
    var list = document.getElementById(view.list);
    var empty = document.getElementById(view.empty);
    if (list && listed(list, n)) return;
    insertMessage(list, empty, document.importNode(item, true));
  }
  function replaceFrom(doc, id) {
    var old = document.getElementById(id);
    var fresh = doc.getElementById(id);
    if (!old || !fresh) return null;
    fresh = document.importNode(fresh, true);
    old.parentNode.replaceChild(fresh, old);
    return fresh;
  }
  function rowNames(section) {
    var names = [];
    var rows = section ? section.querySelectorAll("li.conv") : [];
    for (var i = 0; i < rows.length; i++) {
      var label = rows[i].querySelector(".conv-name");
      names.push(label ? label.textContent : "");
    }
    return names;
  }
  function applySection(doc, id) {
    var before = rowNames(document.getElementById(id));
    var section = replaceFrom(doc, id);
    if (!section) return;
    var rows = section.querySelectorAll("li.conv");
    var names = rowNames(section);
    for (var i = 0; i < rows.length; i++) {
      if (passedOthers(before, names, i)) markArrived(rows[i]);
    }
  }
  // A row arrived when it is new or now sits above a row that was above it,
  // so a row that only rose because others left is not marked.
  function passedOthers(before, names, i) {
    var was = before.indexOf(names[i]);
    if (was < 0) return true;
    for (var j = i + 1; j < names.length; j++) {
      var other = before.indexOf(names[j]);
      if (other >= 0 && other < was) return true;
    }
    return false;
  }
  function applySections(doc) {
    var ids = ["open-section", "closed-section"];
    if (document.getElementById(ids[0])) {
      for (var i = 0; i < ids.length; i++) applySection(doc, ids[i]);
      return;
    }
    // A list that failed at load has no sections, only its failure text.
    var failure = document.getElementById("list-error");
    var at = failure && failure.parentNode === mainEl ? failure : null;
    for (var j = 0; j < ids.length; j++) {
      var fresh = doc.getElementById(ids[j]);
      if (fresh) mainEl.insertBefore(document.importNode(fresh, true), at);
    }
    if (at) mainEl.removeChild(at);
    clearLoadNotice();
  }
  function applyConversations(event) {
    var doc = parseHTML(event.data);
    replaceFrom(doc, "conversation-index");
    if (view.sections && mainEl) applySections(doc);
    if (box && results && trim(box.value) !== "") renderSearch();
    // The event follows a watch and a list that worked, so a quiet view can recover too.
    if (view.list && groupMissing()) reset();
  }
  function applyMeta(event) {
    replaceFrom(parseHTML(event.data), "conversation-meta");
  }
  var regionIds = {};
  var regionList = [view.list, view.empty, "group-error", "more", "list-error", "read-outcome", "read-reason"];
  for (var r = 0; r < regionList.length; r++) regionIds[regionList[r]] = true;
  function region(container) {
    var out = [];
    var nodes = container ? container.children : [];
    for (var i = 0; i < nodes.length; i++) if (regionIds[nodes[i].id]) out.push(nodes[i]);
    return out;
  }
  function replaceRegion(doc) {
    var container = document.getElementById(view.region);
    var old = region(container);
    var fresh = region(doc.getElementById(view.region));
    var at = old.length ? old[0] : null;
    for (var i = 0; i < fresh.length; i++) container.insertBefore(document.importNode(fresh[i], true), at);
    for (var j = 0; j < old.length; j++) container.removeChild(old[j]);
  }
  // A failed reload backs off from the stream's own reconnect delay, as the browser would.
  var retryFirst = {{streamRetry}};
  var retryMax = 30000;
  var retryDelay = 0;
  var retryTimer = null;
  function cancelRetry() {
    if (retryTimer === null) return;
    clearTimeout(retryTimer);
    retryTimer = null;
  }
  function scheduleRetry() {
    cancelRetry();
    if (document.visibilityState === "hidden") return;
    retryDelay = retryDelay ? Math.min(retryDelay * 2, retryMax) : retryFirst;
    retryTimer = setTimeout(function () { retryTimer = null; reload(); }, retryDelay);
  }
  function reloadFailed(text) {
    reloading = false;
    reloadDue = true;
    showGroupError(text);
    scheduleRetry();
  }
  // The stream stays closed until the replace, so a live insert cannot be lost under it.
  function reload() {
    if (reloading) return;
    cancelRetry();
    reloading = true;
    reloadDue = false;
    // The view URL has no before, so an older group cannot reload itself and reset again.
    fetchPage(view.url).then(function (got) {
      if (!got.ok || !groupOk(got.doc)) { reloadFailed(groupFailure(got.doc, newestFailed)); return; }
      replaceRegion(got.doc);
      clearLoadNotice();
      reloading = false;
      retryDelay = 0;
      seq = pageSeq();
      if (/[?&]before=/.test(location.search)) history.replaceState(history.state, "", view.url);
      observeMore();
      openStream();
    }).catch(function () {
      reloadFailed(newestFailed);
    });
  }
  // Closing first keeps the browser from reconnecting with the stale Last-Event-ID.
  function reset() {
    closeStream();
    reload();
  }
  document.addEventListener("visibilitychange", function () {
    if (document.visibilityState === "hidden") { closeStream(); cancelRetry(); }
    else if (reloadDue) reload();
    else openStream();
  });
  openStream();
})();
</script>
</body>
</html>
{{define "convSections"}}<section class="conv-group" id="open-section">
<h2>Open <span class="count">{{len .Open}}</span></h2>
{{if .Open}}<ul class="conv-list" id="open-conversations">{{template "convRows" .Open}}</ul>{{else}}<p class="conv-empty" id="open-empty">No open conversations.</p>{{end}}
</section>
<section class="conv-group" id="closed-section">
<h2>Closed <span class="count">{{len .Closed}}</span></h2>
{{if .Closed}}<ul class="conv-list" id="closed-conversations">{{template "convRows" .Closed}}</ul>{{else}}<p class="conv-empty" id="closed-empty">No closed conversations.</p>{{end}}
</section>{{end}}{{define "convIndex"}}<ul id="conversation-index" hidden>
{{range .Index}}<li data-name="{{.Name}}" data-status="{{.Status}}" data-task="{{.Task}}" data-ticket="{{.Ticket}}" data-href="{{.Href}}"></li>
{{end}}</ul>{{end}}{{define "convMeta"}}<aside id="conversation-meta">
<dl class="binding">
{{if .StatusKnown}}<div><dt>Status</dt><dd id="conversation-status"><span class="status" data-status="{{.Status}}">{{.Status}}</span></dd></div>
{{else if .ListOutcome}}<div><dt>Status</dt><dd id="conversation-status"><p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p></dd></div>
{{else}}<div><dt>Status</dt><dd id="conversation-status">Not listed.</dd></div>{{end}}
{{if .Task}}<div><dt>Task</dt><dd id="task">{{.Task}}</dd></div>{{end}}
{{if .Ticket}}<div><dt>Ticket</dt><dd id="ticket">{{.Ticket}}</dd></div>{{end}}
{{if .Seat}}<div><dt>Seat</dt><dd id="seat">{{.Seat}}</dd></div>{{end}}
{{if .Directory}}<div><dt>Directory</dt><dd id="directory">{{.Directory}}</dd></div>{{end}}
</dl>
</aside>{{end}}{{define "convRows"}}{{range .}}<li class="conv"><p class="conv-name">{{if .Href}}<a href="{{.Href}}">{{.Name}}</a>{{else}}<span class="name">{{.Name}}</span>{{end}}</p>{{if or .Task .Ticket .Seat}}<dl class="conv-binding">{{if .Task}}<div><dt>Task</dt><dd>{{.Task}}</dd></div>{{end}}{{if .Ticket}}<div><dt>Ticket</dt><dd>{{.Ticket}}</dd></div>{{end}}{{if .Seat}}<div><dt>Seat</dt><dd>{{.Seat}}</dd></div>{{end}}</dl>{{end}}</li>
{{end}}{{end}}
{{define "messageItem"}}<li class="message" data-seq="{{.Message.Seq}}" data-conversation="{{.Conversation}}"><p class="where">{{if .Href}}<a href="{{.Href}}">{{.Conversation}}</a>{{else}}{{.Conversation}}{{end}}</p>
{{template "messageMeta" .Message}}
{{template "messageBody" .Message}}
{{template "messageFoot" .Message}}
</li>{{end}}
{{define "olderMarker"}}{{if .Full}}<div id="more" data-next="{{.NextPage}}"></div>
{{end}}{{end}}
{{define "messageMeta"}}<p class="meta"><span class="who">From: <span class="from">{{.From}}</span>{{if .To}} To: <span class="to">{{range $i, $name := .To}}{{if $i}}, {{end}}<span class="addressee">{{$name}}</span>{{end}}</span>{{end}}</span><span class="meta-end"><time class="when" datetime="{{.Time}}">{{viewTime .Time}}</time><span class="msg-actions"><button type="button" class="markdown" aria-pressed="false" aria-label="Markdown" title="Markdown"><svg viewBox="25 22 165 84" aria-hidden="true"><path fill="currentColor" d="M30 98V30h20l20 25 20-25h20v68H90V59L70 84 50 59v39zm125 0-30-33h20V30h20v35h20z"/></svg></button><button type="button" class="copy" aria-label="Copy markdown" title="Copy markdown"><svg class="icon-copy" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg><svg class="icon-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg></button></span></span></p>{{end}}
{{define "messageBody"}}<div class="body rendered">{{renderBody .Body}}</div><pre class="body source" hidden>{{.Body}}</pre>{{end}}
{{define "messageFoot"}}<p class="foot"><span class="seq-n">{{.Seq}}</span><span class="kind" data-kind="{{.Kind}}">{{.Kind}}</span></p>{{end}}
`
