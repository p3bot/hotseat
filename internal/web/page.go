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
	ListOutcome   string
	ListReason    string
	Name          string
	Status        string
	StatusKnown   bool
	ReadAction    string
	PublishAction string
	Read          *readView
	From          string
	ToText        string
	MessageKind   string
	Body          string
	TxID          string
	Publish       *publishView
	HTTPStatus    int
}

type conversationRow struct {
	Name   string
	Status string
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
body { font-family: sans-serif; line-height: 1.45; max-width: 42rem; margin: 1.5rem auto; padding: 0 1rem; background: Canvas; color: CanvasText; }
a { color: LinkText; }
input, textarea, button { font: inherit; color: inherit; background: Field; }
label { display: grid; gap: 0.25rem; margin: 0.75rem 0; }
input, textarea { width: 100%; box-sizing: border-box; padding: 0.35rem 0.5rem; }
button { padding: 0.35rem 0.8rem; }
section, form { margin: 1.25rem 0; }
.message { border-top: 1px solid ButtonBorder; padding: 0.6rem 0; }
#more { height: 1px; }
#older-error:empty { display: none; }
pre.body { white-space: pre-wrap; overflow-wrap: anywhere; margin: 0.25rem 0 0; font-family: ui-monospace, monospace; }
.hint, .meta { font-size: 0.9rem; }
ul { padding-left: 1.2rem; }
</style>
</head>
<body>
<p><a href="/">Conversations</a></p>
{{if .Notice}}<p id="notice">{{.Notice}}</p>{{end}}
{{if eq .Kind "list"}}
<h1>Conversations</h1>
{{if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else if eq (len .Conversations) 0}}<p id="empty">No conversations.</p>
{{else}}<ul id="conversations">
{{range .Conversations}}<li><a href="{{.Href}}">{{.Name}}</a> <span class="status">{{.Status}}</span></li>
{{end}}</ul>{{end}}
{{else if eq .Kind "conversation"}}
<h1>{{.Name}}</h1>
{{if .StatusKnown}}<p id="conversation-status">Status: <span class="status">{{.Status}}</span></p>
{{else if .ListOutcome}}<p id="list-error">{{.ListOutcome}}{{if .ListReason}} {{.ListReason}}{{end}}</p>
{{else}}<p id="conversation-status">Not listed.</p>{{end}}
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
<section id="publish-section">
<h2>Publish</h2>
<label for="from">From <input id="from" name="from" value="{{.From}}" autocomplete="off"></label>
<label for="to">To <textarea id="to" name="to" rows="3">{{.ToText}}</textarea></label>
<p class="hint">One name per line. Empty sends no addressee. Order is kept.</p>
<label for="kind">Kind <input id="kind" name="kind" value="{{.MessageKind}}" autocomplete="off"></label>
<label for="body">Body <textarea id="body" name="body" rows="6">{{.Body}}</textarea></label>
<label for="txid">Transaction id <input id="txid" name="txid" value="{{.TxID}}" autocomplete="off"></label>
<button type="submit" id="publish-button" formaction="{{.PublishAction}}">Publish</button>
<p class="hint">An empty id is filled here when you publish. The server does not invent one. A stored publish clears this field.</p>
</section>
</form>
<script>
(function () {
  var form = document.getElementById("read-form");
  var publish = document.getElementById("publish-button");
  if (!form || !publish) return;
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
{{end}}
</body>
</html>
`
