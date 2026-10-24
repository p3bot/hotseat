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
	kindList         = "list"
	kindConversation = "conversation"
)

type page struct {
	Kind          string
	Notice        string
	Conversations []conversationRow
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
	ReadAction    string
	PublishAction string
	Read          *readView
	From          string
	ToText        string
	MessageKind   string
	Body          string
	TxID          string
	Publish       *publishView
	SendOpen      bool
	HTTPStatus    int
}

type conversationRow struct {
	Name   string
	Status string
	Href   string
}

type indexEntry struct {
	Name   string
	Status string
	Task   string
	Ticket string
	Href   string
}

type readView struct {
	Outcome  string
	Reason   string
	Messages []client.Message
	Full     bool
	NextPage string
}

type publishView struct {
	Outcome       string
	Reason        string
	AlreadyStored bool
	Stored        bool
	Message       *client.Message
}

// TabTitle is the browser tab label. The list is the product name.
// A conversation adds its name so two transcripts stay distinct.
func (p page) TabTitle() string {
	if p.Name != "" {
		return "Hotseat - " + p.Name
	}
	return "Hotseat"
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
	"viewTime": viewTime,
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
nav a { display: block; padding: 0.5rem 0.7rem; border: 1px solid var(--muted); border-radius: 6px; background: var(--muted); color: #fff; font-weight: 650; text-decoration: none; }
nav a:hover { background: color-mix(in srgb, var(--muted) 88%, #000); border-color: color-mix(in srgb, var(--muted) 88%, #000); text-decoration: none; }
main { grid-area: main; min-width: 0; min-height: 0; overflow: auto; padding: 1.25rem 1.5rem 1.5rem; }
body:not(.has-meta) main { max-width: 46rem; }
body.has-meta main { display: flex; flex-direction: column; overflow: hidden; }
body.has-meta #read-form { flex: 1 1 auto; min-height: 0; overflow: auto; margin-right: -1.5rem; margin-bottom: -1.5rem; padding: 0 1.25rem 1.5rem 0; }
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
button { padding: 0.3rem 0.7rem; border: 1px solid var(--line); border-radius: 4px; background: var(--bg); }
button:hover { border-color: var(--accent); }
#publish-button { background: var(--accent); border-color: var(--accent); color: #fff; }
#publish-button:hover { background: color-mix(in srgb, var(--accent) 88%, #000); border-color: color-mix(in srgb, var(--accent) 88%, #000); }
.dialog-actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
dialog { border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg); padding: 1rem 1.15rem 1.15rem; width: min(34rem, calc(100vw - 2rem)); }
dialog input, dialog textarea, dialog button { background: var(--ticket); }
dialog::backdrop { background: rgb(28 25 23 / 0.45); }
#transcript { list-style: none; margin: 0; padding: 0; }
.message { background: var(--ticket); border: 1px solid var(--line); border-radius: 6px; padding: 0.3rem 0.75rem; margin: 0 0 1.2rem; box-shadow: 0 1px 2px rgb(28 25 23 / 0.08), 0 4px 12px rgb(28 25 23 / 0.12); }
#more { height: 1px; }
#older-error, #list-error, #publish-reason { color: var(--issues); }
#older-error:empty { display: none; }
#notice, #list-error, #publish-reason { background: var(--banner); padding: 0.35rem 0.55rem; border-radius: 4px; }
pre.body { white-space: pre-wrap; overflow-wrap: anywhere; margin: 1rem 0 0; font-family: ui-monospace, monospace; }
.hint, .meta { font-size: 0.875rem; color: var(--muted); }
.meta { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: baseline; gap: 0.25rem 1rem; margin: 0; }
.meta .who { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; }
.meta .from { display: inline-block; min-width: 12ch; max-width: 100%; margin-right: 0.75rem; color: var(--fg); overflow-wrap: anywhere; vertical-align: baseline; }
.meta .to { overflow-wrap: anywhere; color: var(--fg); }
.meta .when { margin-left: auto; white-space: nowrap; font-variant-numeric: tabular-nums; }
.foot { display: flex; justify-content: space-between; align-items: center; gap: 1rem; margin: 1rem 0 0.15rem; font-size: 0.875rem; color: var(--muted); }
.foot .seq-n { color: var(--fg); font-variant-numeric: tabular-nums; }
.kind { display: inline-block; padding: 0.05rem 0.55rem; border: 1px solid var(--line); border-radius: 999px; background: var(--bg); color: var(--muted); font-size: 0.75rem; font-weight: 650; line-height: 1.4; text-transform: capitalize; }
.kind[data-kind="error"] { color: var(--issues); background: var(--banner); border-color: color-mix(in srgb, var(--issues) 35%, var(--line)); }
.kind[data-kind="instruction"], .kind[data-kind="instructions"] { color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, #fff); border-color: color-mix(in srgb, var(--accent) 35%, var(--line)); }
#conversations { list-style: none; padding: 0; }
#conversations li { display: flex; gap: 0.75rem; align-items: baseline; padding: 0.4rem 0; border-bottom: 1px solid var(--line); }
@media (max-width: 960px) {
  body, body.has-meta { height: auto; min-height: 100vh; overflow: visible; grid-template-columns: 1fr; grid-template-rows: auto; grid-template-areas: "bar" "nav" "meta" "main"; }
  main, body.has-meta main, body.has-meta #read-form { display: block; overflow: visible; }
  body.has-meta #read-form { margin: 0; padding: 0; }
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
<a href="/" id="nav-conversations"{{if eq .Kind "list"}} aria-current="page"{{end}}>Conversations</a>
</nav>
<main>
{{if .Notice}}<p id="notice">{{.Notice}}</p>{{end}}
{{if eq .Kind "list"}}
<h1>Conversations</h1>
{{if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else if eq (len .Conversations) 0}}{{if eq (len .Index) 0}}<p id="empty">No conversations.</p>{{else}}<p id="empty">No open conversations.</p>{{end}}
{{else}}<ul id="conversations">
{{range .Conversations}}<li><a href="{{.Href}}">{{.Name}}</a> <span class="status" data-status="{{.Status}}">{{.Status}}</span></li>
{{end}}</ul>{{end}}
{{else if eq .Kind "conversation"}}
<header class="conv-head">
<h1>{{.Name}}</h1>
<button type="button" id="send-pen" aria-label="Send">
<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z"/></svg>
</button>
</header>
{{if .Publish}}<section id="publish-result">
<p id="publish-outcome">{{.Publish.Outcome}}</p>
{{if .Publish.Reason}}<p id="publish-reason">{{.Publish.Reason}}</p>{{end}}
{{if .Publish.AlreadyStored}}<p id="already-stored">Already stored.</p>{{end}}
{{if .Publish.Stored}}<p id="stored">Stored.</p>{{end}}
{{if .Publish.Message}}<div id="publish-message" class="message">
{{template "messageMeta" .Publish.Message}}
<pre class="body">{{.Publish.Message.Body}}</pre>
{{template "messageFoot" .Publish.Message}}
</div>{{end}}
</section>{{end}}
<form id="read-form" method="post" action="{{.ReadAction}}">
<input type="hidden" name="conversation" value="{{.Name}}">
<section id="transcript-section">
{{if .Read}}{{if ne .Read.Outcome "ok"}}<p id="read-outcome">{{.Read.Outcome}}</p>
{{if .Read.Reason}}<p id="read-reason">{{.Read.Reason}}</p>{{end}}
{{end}}{{if and .Read (eq .Read.Outcome "ok")}}{{if eq (len .Read.Messages) 0}}<p id="transcript-empty">No messages.</p>
{{else}}<ol id="transcript">
{{range .Read.Messages}}<li class="message" data-seq="{{.Seq}}">
{{template "messageMeta" .}}
<pre class="body">{{.Body}}</pre>
{{template "messageFoot" .}}
</li>{{end}}</ol>
{{if .Read.Full}}<p id="older-error"></p>
<div id="more" data-next="{{.Read.NextPage}}"></div>{{end}}
{{end}}{{end}}{{end}}
</section>
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
<button type="submit" id="publish-button" formaction="{{.PublishAction}}">Send</button>
</div>
<p class="hint">An empty id is filled here when you send. The server does not invent one. A stored send clears this field.</p>
</dialog>
</form>
{{end}}
</main>
{{if eq .Kind "conversation"}}<aside id="conversation-meta">
<dl class="binding">
{{if .StatusKnown}}<div><dt>Status</dt><dd id="conversation-status"><span class="status" data-status="{{.Status}}">{{.Status}}</span></dd></div>
{{else if .ListOutcome}}<div><dt>Status</dt><dd id="conversation-status"><p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p></dd></div>
{{else}}<div><dt>Status</dt><dd id="conversation-status">Not listed.</dd></div>{{end}}
{{if .Task}}<div><dt>Task</dt><dd id="task">{{.Task}}</dd></div>{{end}}
{{if .Ticket}}<div><dt>Ticket</dt><dd id="ticket">{{.Ticket}}</dd></div>{{end}}
{{if .Seat}}<div><dt>Seat</dt><dd id="seat">{{.Seat}}</dd></div>{{end}}
{{if .Directory}}<div><dt>Directory</dt><dd id="directory">{{.Directory}}</dd></div>{{end}}
</dl>
</aside>{{end}}
<ul id="conversation-index" hidden>
{{range .Index}}<li data-name="{{.Name}}" data-status="{{.Status}}" data-task="{{.Task}}" data-ticket="{{.Ticket}}" data-href="{{.Href}}"></li>
{{end}}</ul>
<script>
(function () {
  var box = document.getElementById("search");
  var results = document.getElementById("search-results");
  var index = document.getElementById("conversation-index");
  if (box && results && index) {
    var items = [];
    for (var n = 0; n < index.children.length; n++) {
      var row = index.children[n];
      items.push({
        name: row.getAttribute("data-name") || "",
        status: row.getAttribute("data-status") || "",
        task: row.getAttribute("data-task") || "",
        ticket: row.getAttribute("data-ticket") || "",
        href: row.getAttribute("data-href") || ""
      });
    }
    function trim(s) { return s.replace(/^\s+/, "").replace(/\s+$/, ""); }
    function hit(item, q) {
      return item.name.toLowerCase().indexOf(q) >= 0 || item.task.toLowerCase().indexOf(q) >= 0 || item.ticket.toLowerCase().indexOf(q) >= 0;
    }
    function render() {
      var q = trim(box.value).toLowerCase();
      while (results.firstChild) results.removeChild(results.firstChild);
      if (q === "") { results.hidden = true; return; }
      var shown = 0;
      for (var i = 0; i < items.length; i++) {
        if (!hit(items[i], q)) continue;
        var li = document.createElement("li");
        var link = document.createElement("a");
        link.href = items[i].href;
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
    box.addEventListener("input", render);
    box.addEventListener("keydown", function (event) {
      if (event.key !== "Enter") return;
      var link = results.querySelector("a");
      if (!link) return;
      event.preventDefault();
      window.location = link.href;
    });
  }
  var form = document.getElementById("read-form");
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
  if (dialog && typeof dialog.hasAttribute === "function" && dialog.hasAttribute("data-send-open")) openSend();
  if (pen) pen.addEventListener("click", openSend);
  var cancel = document.getElementById("send-cancel");
  if (cancel && dialog && typeof dialog.close === "function") {
    cancel.addEventListener("click", function () { dialog.close(); });
  }
  var more = document.getElementById("more");
  var list = document.getElementById("transcript");
  if (!more || !list || !more.getAttribute("data-next")) return;
  var busy = false;
  function visible(el) {
    var r = el.getBoundingClientRect();
    return r.top < window.innerHeight && r.bottom > 0;
  }
  function showOlderError(text) {
    var el = document.getElementById("older-error");
    if (el) el.textContent = text;
  }
  function olderOk(doc) {
    var outcome = doc.getElementById("read-outcome");
    if (outcome && outcome.textContent !== "ok") return false;
    return doc.getElementById("transcript") || doc.getElementById("transcript-empty");
  }
  function olderFailure(doc) {
    var notice = doc.getElementById("notice");
    if (notice && notice.textContent) return notice.textContent;
    var outcome = doc.getElementById("read-outcome");
    if (outcome && outcome.textContent && outcome.textContent !== "ok") {
      var reason = doc.getElementById("read-reason");
      if (reason && reason.textContent) return outcome.textContent + " " + reason.textContent;
      return outcome.textContent;
    }
    return "older messages did not load";
  }
  function applyOlder(doc) {
    var nextList = doc.getElementById("transcript");
    if (nextList) {
      var items = nextList.querySelectorAll("li.message");
      for (var i = 0; i < items.length; i++) list.appendChild(document.importNode(items[i], true));
    }
    var nextMore = doc.getElementById("more");
    if (nextMore && nextMore.getAttribute("data-next")) more.setAttribute("data-next", nextMore.getAttribute("data-next"));
    else more.removeAttribute("data-next");
  }
  function load() {
    if (busy) return;
    var url = more.getAttribute("data-next");
    if (!url) return;
    busy = true;
    fetch(url).then(function (res) {
      return res.text().then(function (html) { return { ok: res.ok, html: html }; });
    }).then(function (got) {
      var doc = new DOMParser().parseFromString(got.html, "text/html");
      if (!got.ok || !olderOk(doc)) {
        showOlderError(olderFailure(doc));
        busy = false;
        return;
      }
      showOlderError("");
      applyOlder(doc);
      busy = false;
      if (more.getAttribute("data-next") && visible(more)) load();
    }).catch(function () {
      showOlderError("older messages did not load");
      busy = false;
    });
  }
  var obs = new IntersectionObserver(function (entries) {
    for (var i = 0; i < entries.length; i++) if (entries[i].isIntersecting) load();
  });
  obs.observe(more);
})();
</script>
</body>
</html>
{{define "messageMeta"}}<p class="meta"><span class="who">From: <span class="from">{{.From}}</span>{{if .To}} To: <span class="to">{{range $i, $name := .To}}{{if $i}}, {{end}}<span class="addressee">{{$name}}</span>{{end}}</span>{{end}}</span><time class="when" datetime="{{.Time}}">{{viewTime .Time}}</time></p>{{end}}
{{define "messageFoot"}}<p class="foot"><span class="seq-n">{{.Seq}}</span><span class="kind" data-kind="{{.Kind}}">{{.Kind}}</span></p>{{end}}
`
