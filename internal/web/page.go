// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"html/template"
	"net/http"

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

var pageTmpl = template.Must(template.New("page").Parse(pageSource))

const pageSource = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.TabTitle}}</title>
<style>
:root { color-scheme: light dark; }
body { margin: 0; min-height: 100vh; font-family: system-ui, sans-serif; line-height: 1.45; background: Canvas; color: CanvasText; display: grid; grid-template-columns: 13rem 1fr; grid-template-rows: auto 1fr; }
a { color: LinkText; }
.topbar { grid-column: 1 / -1; display: flex; align-items: center; gap: 1rem; padding: 0.55rem 1rem; border-bottom: 1px solid ButtonBorder; }
.brand { color: inherit; text-decoration: none; font-weight: 600; }
.search { margin-left: auto; position: relative; width: min(24rem, 46vw); }
.sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); border: 0; }
label.sr { display: inline; margin: 0; }
input, textarea, button, #search { font: inherit; color: inherit; background: Field; }
input, textarea, #search { width: 100%; box-sizing: border-box; padding: 0.35rem 0.55rem; border: 1px solid ButtonBorder; border-radius: 0.25rem; }
#search-results { position: absolute; z-index: 2; top: calc(100% + 0.25rem); left: 0; right: 0; margin: 0; padding: 0.25rem 0; list-style: none; background: Canvas; border: 1px solid ButtonBorder; border-radius: 0.25rem; max-height: 18rem; overflow: auto; }
#search-results[hidden] { display: none; }
#search-results li { display: flex; justify-content: space-between; gap: 0.75rem; align-items: baseline; padding: 0 0.7rem; }
#search-results a { color: inherit; text-decoration: none; padding: 0.3rem 0; }
.status { color: GrayText; font-size: 0.85rem; }
nav { padding: 0.75rem; border-right: 1px solid ButtonBorder; }
nav a { display: block; padding: 0.4rem 0.55rem; border-radius: 0.25rem; color: inherit; text-decoration: none; }
nav a[aria-current="page"] { background: ButtonFace; }
main { min-width: 0; padding: 1.25rem 1.5rem 3rem; max-width: 46rem; }
.conv-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
.binding { display: flex; flex-wrap: wrap; gap: 0.35rem 1rem; margin: 0.25rem 0 0; }
.binding div { display: flex; gap: 0.35rem; min-width: 0; max-width: 100%; }
.binding dt { color: GrayText; }
.binding dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
#send-pen { border: 1px solid ButtonBorder; background: ButtonFace; border-radius: 0.25rem; padding: 0.4rem; line-height: 0; }
#send-pen svg { width: 1.15rem; height: 1.15rem; display: block; }
label { display: grid; gap: 0.25rem; margin: 0.75rem 0; }
button { padding: 0.35rem 0.8rem; }
.dialog-actions { display: flex; gap: 0.5rem; }
dialog { border: 1px solid ButtonBorder; border-radius: 0.4rem; background: Canvas; color: CanvasText; padding: 1rem 1.15rem 1.15rem; width: min(34rem, calc(100vw - 2rem)); }
dialog::backdrop { background: rgb(0 0 0 / 0.45); }
.message { border-top: 1px solid ButtonBorder; padding: 0.6rem 0; }
#more { height: 1px; }
#older-error:empty { display: none; }
pre.body { white-space: pre-wrap; overflow-wrap: anywhere; margin: 0.25rem 0 0; font-family: ui-monospace, monospace; }
.hint, .meta { font-size: 0.9rem; }
#conversations { list-style: none; padding: 0; }
#conversations li { display: flex; gap: 0.75rem; padding: 0.4rem 0; border-bottom: 1px solid ButtonBorder; }
@media (max-width: 720px) {
  body { grid-template-columns: 1fr; }
  nav { border-right: 0; border-bottom: 1px solid ButtonBorder; }
  .search { width: min(16rem, 50vw); }
}
</style>
</head>
<body>
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
{{range .Conversations}}<li><a href="{{.Href}}">{{.Name}}</a> <span class="status">{{.Status}}</span></li>
{{end}}</ul>{{end}}
{{else if eq .Kind "conversation"}}
<header class="conv-head">
<div>
<h1>{{.Name}}</h1>
{{if .StatusKnown}}<p id="conversation-status">Status: <span class="status">{{.Status}}</span></p>
{{else if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else}}<p id="conversation-status">Not listed.</p>{{end}}
<dl class="binding">
{{if .Task}}<div><dt>task</dt><dd id="task">{{.Task}}</dd></div>{{end}}
{{if .Ticket}}<div><dt>ticket</dt><dd id="ticket">{{.Ticket}}</dd></div>{{end}}
{{if .Seat}}<div><dt>seat</dt><dd id="seat">{{.Seat}}</dd></div>{{end}}
{{if .Directory}}<div><dt>directory</dt><dd id="directory">{{.Directory}}</dd></div>{{end}}
</dl>
</div>
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
<p class="meta">seq {{.Publish.Message.Seq}} from <span class="from">{{.Publish.Message.From}}</span> kind <span class="kind">{{.Publish.Message.Kind}}</span> to {{range .Publish.Message.To}}<span class="addressee">{{.}}</span> {{end}}txid <span class="txid">{{.Publish.Message.TxID}}</span></p>
<pre class="body">{{.Publish.Message.Body}}</pre>
</div>{{end}}
</section>{{end}}
<form id="read-form" method="post" action="{{.ReadAction}}">
<input type="hidden" name="conversation" value="{{.Name}}">
<section id="transcript-section">
<h2>Transcript</h2>
{{if .Read}}<p id="read-outcome">{{.Read.Outcome}}</p>
{{if .Read.Reason}}<p id="read-reason">{{.Read.Reason}}</p>{{end}}
{{if eq .Read.Outcome "ok"}}{{if eq (len .Read.Messages) 0}}<p id="transcript-empty">No messages.</p>
{{else}}<ol id="transcript">
{{range .Read.Messages}}<li class="message" data-seq="{{.Seq}}">
<p class="meta">seq <span class="seq">{{.Seq}}</span> {{.Time}} from <span class="from">{{.From}}</span> kind <span class="kind">{{.Kind}}</span> to {{range .To}}<span class="addressee">{{.}}</span> {{end}}txid <span class="txid">{{.TxID}}</span></p>
<pre class="body">{{.Body}}</pre>
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
<button type="submit" id="publish-button" formaction="{{.PublishAction}}">Send</button>
<button type="button" id="send-cancel">Cancel</button>
</div>
<p class="hint">An empty id is filled here when you send. The server does not invent one. A stored send clears this field.</p>
</dialog>
</form>
{{end}}
</main>
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
      var outcome = doc.getElementById("read-outcome");
      if (!got.ok || !outcome || outcome.textContent !== "ok") {
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
`
