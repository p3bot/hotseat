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
	Title         string
	Notice        string
	Conversations []conversationRow
	ListOutcome   string
	ListReason    string
	Name          string
	Status        string
	StatusKnown   bool
	ReadAction    string
	PublishAction string
	Cursor        string
	Limit         string
	Read          *readView
	From          string
	ToText        string
	Body          string
	Key           string
	Publish       *publishView
	HTTPStatus    int
}

type conversationRow struct {
	Name   string
	Status string
	Href   string
}

type readView struct {
	Outcome    string
	Reason     string
	Messages   []client.Message
	Full       bool
	NextCursor int64
	NextPage   string
}

type publishView struct {
	Outcome       string
	Reason        string
	AlreadyStored bool
	Stored        bool
	Message       *client.Message
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

// The page has no scripts. The response is not stored: it holds the
// transcript and the key the operator just typed.
func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
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
<title>{{.Title}}</title>
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
<p class="meta">seq {{.Publish.Message.Seq}} from <span class="from">{{.Publish.Message.From}}</span> to {{range .Publish.Message.To}}<span class="addressee">{{.}}</span> {{end}}key <span class="key">{{.Publish.Message.Key}}</span></p>
<pre class="body">{{.Publish.Message.Body}}</pre>
</div>{{end}}
</section>{{end}}
<form id="read-form" method="post" action="{{.ReadAction}}">
<input type="hidden" name="conversation" value="{{.Name}}">
<section id="transcript-section">
<h2>Transcript</h2>
<label for="cursor">Cursor <input id="cursor" name="cursor" value="{{.Cursor}}" placeholder="0" inputmode="numeric" autocomplete="off"></label>
<label for="limit">Limit <input id="limit" name="limit" value="{{.Limit}}" placeholder="50" inputmode="numeric"></label>
<button type="submit">Read</button>
<p class="hint">The server forgets this cursor when the response is sent. It is shown here so you can send it again.</p>
{{if .Read}}<p id="read-outcome">{{.Read.Outcome}}</p>
{{if .Read.Reason}}<p id="read-reason">{{.Read.Reason}}</p>{{end}}
{{if eq .Read.Outcome "ok"}}{{if eq (len .Read.Messages) 0}}<p id="transcript-empty">No messages after this cursor.</p>
{{else}}<ol id="transcript">
{{range .Read.Messages}}<li class="message" data-seq="{{.Seq}}">
<p class="meta">seq <span class="seq">{{.Seq}}</span> {{.Time}} from <span class="from">{{.From}}</span> to {{range .To}}<span class="addressee">{{.}}</span> {{end}}key <span class="key">{{.Key}}</span></p>
<pre class="body">{{.Body}}</pre>
</li>{{end}}</ol>{{end}}{{end}}
{{if .Read.Full}}<p id="page-full">Page is full. The next cursor is <span id="next-cursor">{{.Read.NextCursor}}</span>.</p>
<p><a id="next-page" href="{{.Read.NextPage}}">Read from cursor {{.Read.NextCursor}}</a></p>{{end}}
{{end}}
</section>
<section id="publish-section">
<h2>Publish</h2>
<label for="from">From <input id="from" name="from" value="{{.From}}" autocomplete="off"></label>
<label for="to">To <textarea id="to" name="to" rows="3">{{.ToText}}</textarea></label>
<p class="hint">One name per line. Empty sends no addressee. Order is kept.</p>
<label for="body">Body <textarea id="body" name="body" rows="6">{{.Body}}</textarea></label>
<label for="idempotency_key">Idempotency key <input id="idempotency_key" name="idempotency_key" value="{{.Key}}" autocomplete="off"></label>
<button type="submit" formaction="{{.PublishAction}}">Publish</button>
<p class="hint">The server forgets this idempotency key when the response is sent. It does not invent one.</p>
</section>
</form>
{{end}}
</body>
</html>
`
