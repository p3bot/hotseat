// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"

	"github.com/p3bot/hotseat/internal/client"
)

func TestViewTime(t *testing.T) {
	raw := "2026-10-17T12:30:27.069994715Z"
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.In(time.Local).Format("2006-01-02 15:04:05.000")
	if got := viewTime(raw); got != want {
		t.Fatalf("viewTime %q, want %q", got, want)
	}
	if got := viewTime("not-a-time"); got != "not-a-time" {
		t.Fatalf("unparsed %q", got)
	}
}

func TestRenderedMessageShowsLocalTimeKindAndSequence(t *testing.T) {
	raw := "2026-10-17T12:30:27.069994715Z"
	msg := client.Message{
		Seq: 4, Time: raw, From: "alice", To: []string{"bob"},
		Body: "hello", TxID: "idem", Kind: "say",
	}
	p := conversationPage("job")
	p.Group = &messageGroup{Outcome: "ok", Messages: []messageItem{newMessageItem("job", msg)}}
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, p); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.In(time.Local).Format("2006-01-02 15:04:05.000")
	if strings.Count(html, want) != 1 || strings.Count(html, `datetime="`+raw+`"`) != 1 {
		t.Fatalf("time\n%s", html)
	}
	if strings.Count(html, `data-kind="say">say<`) != 1 || strings.Count(html, `class="seq-n">4<`) != 1 {
		t.Fatalf("kind or seq\n%s", html)
	}
	if strings.Contains(html, `id="read-outcome"`) {
		t.Fatalf("ok outcome shown\n%s", html)
	}
}

func TestEveryMessageItemNamesItsSeqAndConversation(t *testing.T) {
	if n := strings.Count(pageSource, `<li class="message"`); n != 1 {
		t.Fatalf("message item markup appears %d times", n)
	}
	messages := page{Kind: kindMessages, Group: &messageGroup{Outcome: "ok", Messages: []messageItem{
		newMessageItem("beta", client.Message{Seq: 9, From: "alice", Body: "b", Kind: "say"}),
		newMessageItem("..", client.Message{Seq: 7, From: "alice", Body: "d", Kind: "say"}),
	}}}
	transcript := conversationPage("job")
	transcript.Group = transcriptGroup("job", []client.Message{
		{Seq: 4, From: "alice", Body: "one", Kind: "say"},
		{Seq: 6, From: "bob", Body: "two", Kind: "say"},
	}, false)
	cases := []struct {
		name  string
		page  page
		items []string
	}{
		{name: "messages", page: messages, items: []string{
			`<li class="message" data-seq="9" data-conversation="beta"><p class="where"><a href="/conversations/beta">beta</a></p>`,
			`<li class="message" data-seq="7" data-conversation=".."><p class="where">..</p>`,
		}},
		{name: "transcript", page: transcript, items: []string{
			`<li class="message" data-seq="6" data-conversation="job"><p class="where"><a href="/conversations/job">job</a></p>`,
			`<li class="message" data-seq="4" data-conversation="job"><p class="where"><a href="/conversations/job">job</a></p>`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := renderPage(t, tc.page)
			if n := strings.Count(html, `<li class="message"`); n != len(tc.items) {
				t.Fatalf("items %d\n%s", n, html)
			}
			at := 0
			for _, want := range tc.items {
				i := strings.Index(html[at:], want)
				if i < 0 {
					t.Fatalf("missing or out of order %s\n%s", want, html)
				}
				at += i + len(want)
			}
		})
	}
	if !strings.Contains(renderPage(t, transcript), "#transcript .where { display: none; }") {
		t.Fatal("transcript shows the conversation link on each message")
	}
}

func TestMessageFooterKeepsSourceBesideRenderedMarkdown(t *testing.T) {
	p := conversationPage("job")
	p.Group = &messageGroup{Outcome: "ok", Messages: []messageItem{newMessageItem("job", client.Message{
		Seq: 4, From: "alice", Body: "**hello**", Kind: "instruction",
	})}}
	html := renderPage(t, p)
	rendered := strings.Index(html, "<strong>hello</strong>")
	source := strings.Index(html, `class="body source" hidden>**hello**</pre>`)
	actions := strings.Index(html, `<button type="button" class="markdown" aria-pressed="false" aria-label="Markdown" title="Markdown"><svg`)
	copyTitle := strings.Index(html, `class="copy" aria-label="Copy markdown" title="Copy markdown"`)
	if copyTitle < 0 {
		t.Fatalf("copy tooltip\n%s", html)
	}
	when := strings.Index(html, `class="when"`)
	kind := strings.Index(html, `class="seq-n">4</span><span class="kind" data-kind="instruction">instruction</span>`)
	if rendered < 0 || source < rendered || actions < 0 || when < 0 || actions < when || kind < source {
		t.Fatalf("message\n%s", html)
	}
}

func TestMessageControlsCopySourceAndToggleView(t *testing.T) {
	p := transcriptFixture("job", "")
	p.Group.Messages = []messageItem{newMessageItem("job", client.Message{Seq: 4, From: "alice", Body: "line\n**two**", Kind: "say"})}
	d := loadPage(t, renderPage(t, p), "/conversations/job", "")
	d.run(`var __copy = document.querySelector("button.copy"); var __md = document.querySelector("button.markdown");
function __shown() {
  var r = document.querySelector("div.rendered").hidden, s = document.querySelector("pre.source").hidden;
  if (!r && s) return "rendered";
  if (r && !s) return "source";
  return "both";
}
function __copyState() { return __copy.getAttribute("aria-label") + "|" + __copy.getAttribute("title") + "|" + __copy.classList.contains("copied"); }`)

	d.run(`__click(__copy.querySelector("svg"))`)
	if got := d.str("__lastCopy"); got != "line\n**two**" {
		t.Fatalf("copied %q", got)
	}
	if got := d.str("__copyState()"); got != "Copied|Copied|true" {
		t.Fatalf("copy %s", got)
	}
	d.run(`__flushTimers()`)
	if got := d.str("__copyState()"); got != "Copy markdown|Copy markdown|false" {
		t.Fatalf("restore %s", got)
	}
	d.run(`__writeFails = true; __click(__copy)`)
	if got := d.str("__copyState()"); got != "Copy failed|Copy failed|false" {
		t.Fatalf("fail %s", got)
	}
	d.run(`__click(__md)`)
	if d.str(`__md.getAttribute("aria-pressed")`) != "true" || d.str("__shown()") != "source" || d.str(`__md.getAttribute("aria-label")`) != "Markdown" {
		t.Fatalf("on pressed %s shown %s", d.str(`__md.getAttribute("aria-pressed")`), d.str("__shown()"))
	}
	d.run(`__click(__md)`)
	if d.str(`__md.getAttribute("aria-pressed")`) != "false" || d.str("__shown()") != "rendered" {
		t.Fatalf("off pressed %s shown %s", d.str(`__md.getAttribute("aria-pressed")`), d.str("__shown()"))
	}
}

func TestTabTitleNamesTheProduct(t *testing.T) {
	if got := (page{}).TabTitle(); got != "Hotseat" {
		t.Fatalf("recent %q", got)
	}
	if got := (page{Kind: kindList}).TabTitle(); got != "Hotseat - Conversations" {
		t.Fatalf("list %q", got)
	}
	if got := (page{Name: "job"}).TabTitle(); got != "Hotseat - job" {
		t.Fatalf("conversation %q", got)
	}
}

func TestPublishScriptMintsOnlyOnEmptyPublish(t *testing.T) {
	d := loadPage(t, renderPage(t, conversationPage("job")), "/conversations/job", "")
	d.run(`function __submit(id, value) {
  __randomCalls = 0;
  document.getElementById("txid").value = value;
  document.getElementById("send-form").dispatchEvent({ type: "submit", submitter: document.getElementById(id) });
  return document.getElementById("txid").value + "|" + __randomCalls;
}`)
	if got, want := d.str(`__submit("publish-button", "")`), scriptFixedHex()+"|1"; got != want {
		t.Fatalf("empty publish %s, want %s", got, want)
	}
	if got := d.str(`__submit("publish-button", "keep-me")`); got != "keep-me|0" {
		t.Fatalf("filled publish %s", got)
	}
	if got := d.str(`__submit("send-cancel", "")`); got != "|0" {
		t.Fatalf("other submitter %s", got)
	}
}

func scriptFixedHex() string {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "%02x", (i*17+3)&255)
	}
	return b.String()
}

func TestRefusedSendOpensTheSameDialog(t *testing.T) {
	refused := conversationPage("job")
	refused.SendOpen = true
	refusedHTML := renderPage(t, refused)
	if strings.Contains(refusedHTML, `id="send-dialog" open`) || !sendMarked(refusedHTML) {
		t.Fatalf("marker\n%s", refusedHTML)
	}
	d := loadPage(t, refusedHTML, "/conversations/job/messages", "")
	if d.str("__shownCount") != "1" {
		t.Fatal("refused send did not open the dialog")
	}
	d.run(`__click(document.getElementById("send-pen"))`)
	if d.str("__shownCount") != "1" {
		t.Fatal("pen opened a dialog that was already open")
	}

	quietHTML := renderPage(t, conversationPage("job"))
	if sendMarked(quietHTML) {
		t.Fatalf("marker\n%s", quietHTML)
	}
	d = loadPage(t, quietHTML, "/conversations/job", "")
	if d.str("__shownCount") != "0" {
		t.Fatal("a closed send opened the dialog")
	}
	d.run(`__click(document.getElementById("send-pen"))`)
	if d.str("__shownCount") != "1" {
		t.Fatal("pen did not open the dialog")
	}
}

func sendMarked(html string) bool {
	return strings.Contains(html, `id="send-dialog" data-send-open`)
}

func TestOlderGroupAppendsToEitherListAndAFailureKeepsTheMarker(t *testing.T) {
	transcript := transcriptFixture("job", "/conversations/job?before=3", 3)
	messages := messagesFixture("/?before=3", 3)
	pages := []struct {
		name    string
		page    page
		url     string
		list    string
		marker  string
		further string
		at      func(next string, seqs ...int64) page
		other   page
	}{
		{
			name: "transcript", page: transcript, url: "/conversations/job", list: "transcript",
			marker: "/conversations/job?before=3", further: "/conversations/job?before=1",
			at:    func(next string, seqs ...int64) page { return transcriptFixture("job", next, seqs...) },
			other: messagesFixture("", 2),
		},
		{
			name: "messages", page: messages, url: "/", list: "messages",
			marker: "/?before=3", further: "/?before=1",
			at:    func(next string, seqs ...int64) page { return messagesFixture(next, seqs...) },
			other: transcriptFixture("job", "", 2),
		},
	}
	for _, pg := range pages {
		t.Run(pg.name, func(t *testing.T) {
			html := renderPage(t, pg.page)
			if !strings.Contains(html, `id="group-error"`) || !strings.Contains(html, `id="`+pg.list+`"`) {
				t.Fatalf("full group has no place to show a failed load\n%s", html)
			}
			notice := pg.at("")
			notice.Group = nil
			notice.Notice = "bus down"
			refused := pg.at("")
			refused.Group = &messageGroup{Outcome: "refused", Reason: "closed"}
			cases := []struct {
				name    string
				serve   func(d *pageDOM)
				marker  string
				message string
				added   string
			}{
				{name: "notice", serve: func(d *pageDOM) { d.serve(true, renderPage(t, notice)) }, marker: pg.marker, message: "bus down", added: "0"},
				{name: "refused", serve: func(d *pageDOM) { d.serve(true, renderPage(t, refused)) }, marker: pg.marker, message: "refused closed", added: "0"},
				{name: "status", serve: func(d *pageDOM) { d.serve(false, "boom") }, marker: pg.marker, message: "older messages did not load", added: "0"},
				{name: "network", serve: func(d *pageDOM) { d.serveNetworkFailure() }, marker: pg.marker, message: "older messages did not load", added: "0"},
				{name: "other list", serve: func(d *pageDOM) { d.serve(true, renderPage(t, pg.other)) }, marker: pg.marker, message: "older messages did not load", added: "0"},
				{name: "end", serve: func(d *pageDOM) { d.serve(true, renderPage(t, pg.at("", 2))) }, marker: "<unset>", added: "1"},
				{name: "empty", serve: func(d *pageDOM) { d.serve(true, renderPage(t, pg.at(""))) }, marker: "<unset>", added: "0"},
				{name: "next", serve: func(d *pageDOM) { d.serve(true, renderPage(t, pg.at(pg.further, 2))) }, marker: pg.further, added: "1"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					d := loadPage(t, html, pg.url, "")
					tc.serve(d)
					d.run("__intersect()")
					if got := d.str(`__attr("more", "data-next")`); got != tc.marker {
						t.Fatalf("marker %q", got)
					}
					if got := d.str(`__text_of("group-error")`); got != tc.message {
						t.Fatalf("message %q", got)
					}
					if got := d.str(`String(document.getElementById(` + quoteJS(pg.list) + `).children.length - 1)`); got != tc.added {
						t.Fatalf("added %q", got)
					}
				})
			}

			d := loadPage(t, html, pg.url, "")
			d.serve(true, renderPage(t, notice))
			d.run("__intersect()")
			d.serve(true, renderPage(t, pg.at(pg.further, 2, 1)))
			d.run("__intersect()")
			d.serve(true, renderPage(t, pg.at("", 0)))
			d.run("__intersect()")
			if d.str(`__attr("more", "data-next")`) != "<unset>" || d.str(`__text_of("group-error")`) != "" || d.str(`__seqs(`+quoteJS(pg.list)+`)`) != "3,2,1,0" {
				t.Fatalf("marker %q message %q seqs %q", d.str(`__attr("more", "data-next")`), d.str(`__text_of("group-error")`), d.str(`__seqs(`+quoteJS(pg.list)+`)`))
			}
		})
	}
}

func TestSearchKeepsListOrder(t *testing.T) {
	p := page{Kind: kindList}
	applyList(&p, &[]client.Conversation{
		{Name: "zeta", Status: "open", Task: "apple"},
		{Name: "alpha", Status: "closed", Ticket: "pear"},
		{Name: "quiet", Status: "open"},
	})
	d := loadPage(t, renderPage(t, p), "/conversations", "")
	d.run(searchHelpers)
	if got := d.str(`__query("A")`); got != "zeta open,alpha closed" || d.str("__results.hidden") != "false" {
		t.Fatalf("names %q hidden %s", got, d.str("__results.hidden"))
	}
	if got := d.str(`__query("PEAR")`); got != "alpha closed" {
		t.Fatalf("ticket %q", got)
	}
	if got := d.str(`__query("  ")`); got != "" || d.str("__results.hidden") != "true" {
		t.Fatalf("blank %q hidden %s", got, d.str("__results.hidden"))
	}
	if got := d.str(`__query("missing")`); got != "" || d.str(`__text_of("search-empty")`) != "No matches." {
		t.Fatalf("missing %q", got)
	}
}

const searchHelpers = `
var __box = document.getElementById("search");
var __results = document.getElementById("search-results");
function __hits() {
  return __results.children.filter(function (li) { return li.querySelector("a"); }).map(function (li) {
    return li.querySelector("a").textContent + " " + li.querySelector(".status").textContent;
  }).join(",");
}
function __query(v) {
  __box.value = v;
  __box.dispatchEvent({ type: "input" });
  return __hits();
}
`

func TestStreamOpensForItsViewAfterTheHighestSeq(t *testing.T) {
	refused := messagesFixture("")
	refused.Group = &messageGroup{Outcome: "refused", Reason: "nope"}
	cases := []struct {
		name string
		page page
		url  string
		want string
	}{
		{name: "messages", page: messagesFixture("", 9, 7), url: "/", want: "/events?after=9"},
		{name: "messages empty", page: messagesFixture(""), url: "/", want: "/events?after=0"},
		{name: "messages read failed", page: refused, url: "/", want: "/events?after=0"},
		{name: "older group", page: messagesFixture("/?before=38", 39, 38), url: "/?before=40", want: "/events?after=39"},
		{name: "transcript", page: transcriptFixture("job", "", 6, 4), url: "/conversations/job", want: "/events?conversation=job&after=6"},
		{name: "refused send", page: transcriptFixture("job", "", 6), url: "/conversations/job/messages", want: "/events?conversation=job&after=6"},
		{name: "conversations", page: page{Kind: kindList}, url: "/conversations", want: "/events?view=list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := loadPage(t, renderPage(t, tc.page), tc.url, "")
			if got := d.str("__streamURLs()"); got != tc.want {
				t.Fatalf("streams %q", got)
			}
		})
	}
}

func TestStreamMessageInsertsAtTopOnceWithArrived(t *testing.T) {
	cases := []struct {
		name string
		page page
		url  string
		conv string
		list string
	}{
		{name: "messages", page: messagesFixture("", 7, 5), url: "/", conv: "beta", list: "messages"},
		{name: "transcript", page: transcriptFixture("job", "", 7, 5), url: "/conversations/job", conv: "job", list: "transcript"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := loadPage(t, renderPage(t, tc.page), tc.url, "")
			d.emit("message", messageEvent(t, tc.conv, 9))
			if d.str(`__seqs("`+tc.list+`")`) != "9,7,5" || d.str(`__arrived("`+tc.list+`")`) != "9" {
				t.Fatalf("seqs %s arrived %s", d.str(`__seqs("`+tc.list+`")`), d.str(`__arrived("`+tc.list+`")`))
			}
			d.emit("message", messageEvent(t, tc.conv, 9))
			d.emit("message", messageEvent(t, tc.conv, 5))
			if got := d.str(`__seqs("` + tc.list + `")`); got != "9,7,5" {
				t.Fatalf("known seq inserted again: %s", got)
			}
			d.run(`var __item = document.querySelector("li.arrived"); __click(__item.querySelector("button.markdown"))`)
			if d.str(`__item.querySelector("button.markdown").getAttribute("aria-pressed")`) != "true" {
				t.Fatal("inserted item ignores its Markdown control")
			}
			d.run(`__animationEnd(__item)`)
			if got := d.str(`__arrived("` + tc.list + `")`); got != "" {
				t.Fatalf("arrived after animationend %q", got)
			}
		})
	}
}

func TestFirstStreamedMessageReplacesTheEmptyPlaceholder(t *testing.T) {
	cases := []struct {
		name, url, conv, list, empty string
		page                         page
	}{
		{name: "messages", page: messagesFixture(""), url: "/", conv: "alpha", list: "messages", empty: "empty"},
		{name: "transcript", page: transcriptFixture("job", ""), url: "/conversations/job", conv: "job", list: "transcript", empty: "transcript-empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := loadPage(t, renderPage(t, tc.page), tc.url, "")
			d.emit("message", messageEvent(t, tc.conv, 3))
			if d.str(`__text_of("`+tc.empty+`")`) != "<none>" || d.str(`__seqs("`+tc.list+`")`) != "3" || d.str(`__text_of("group-error")`) != "" {
				t.Fatalf("empty %s seqs %s", d.str(`__text_of("`+tc.empty+`")`), d.str(`__seqs("`+tc.list+`")`))
			}
		})
	}
}

func TestStreamConversationsFollowsTheListOnEveryPage(t *testing.T) {
	before := []client.Conversation{
		{Name: "alpha", Status: "open", Task: "fruit"},
		{Name: "beta", Status: "open"},
	}
	after := []client.Conversation{
		{Name: "gamma", Status: "open", Task: "fruit"},
		{Name: "beta", Status: "open"},
		{Name: "alpha", Status: "closed", Task: "fruit"},
	}
	list := page{Kind: kindList}
	applyList(&list, &before)
	d := loadPage(t, renderPage(t, list), "/conversations", "")
	d.run(searchHelpers)
	if got := d.str(`__query("fruit")`); got != "alpha open" {
		t.Fatalf("before %q", got)
	}
	d.run(`var __boxBefore = __box`)
	d.emit("conversations", conversationsEvent(t, "", after))
	if got := d.str("__hits()"); got != "gamma open,alpha closed" {
		t.Fatalf("search after event %q", got)
	}
	if d.str("__box.value") != "fruit" || d.str("document.getElementById(\"search\") === __boxBefore") != "true" {
		t.Fatal("search box changed")
	}
	if got := d.str(`__names("open-conversations") + "|" + __names("closed-conversations")`); got != "gamma,beta|alpha" {
		t.Fatalf("sections %q", got)
	}
	if got := d.str(`__arrived("open-conversations") + "|" + __arrived("closed-conversations")`); got != "gamma|alpha" {
		t.Fatalf("arrived %q", got)
	}
	d.emit("conversations", conversationsEvent(t, "", []client.Conversation{after[1], after[0], after[2]}))
	if got := d.str(`__arrived("open-conversations") + "|" + __arrived("closed-conversations")`); got != "beta|" {
		t.Fatalf("moved arrived %q", got)
	}
	d.emit("conversations", conversationsEvent(t, "", []client.Conversation{after[0], {Name: "beta", Status: "closed"}, after[2]}))
	if got := d.str(`__arrived("open-conversations") + "|" + __arrived("closed-conversations")`); got != "|beta" {
		t.Fatalf("row left on top by a close arrived %q", got)
	}

	failed := page{Kind: kindList, ListOutcome: "unavailable"}
	d = loadPage(t, renderPage(t, failed), "/conversations", "")
	d.emit("conversations", conversationsEvent(t, "", after))
	if got := d.str(`__names("open-conversations") + "|" + __text_of("list-error")`); got != "gamma,beta|<none>" {
		t.Fatalf("failed list %q", got)
	}

	messages := messagesFixture("", 1)
	applyList(&messages, &before)
	d = loadPage(t, renderPage(t, messages), "/", "")
	d.run(searchHelpers)
	d.emit("conversations", conversationsEvent(t, "", after))
	if got := d.str(`__query("gamma")`); got != "gamma open" {
		t.Fatalf("messages search %q", got)
	}
	if d.str(`__text_of("open-section")`) != "<none>" {
		t.Fatal("messages page gained the list sections")
	}

	transcript := transcriptFixture("alpha", "", 1)
	applyList(&transcript, &before)
	d = loadPage(t, renderPage(t, transcript), "/conversations/alpha", "")
	d.emit("conversations", conversationsEvent(t, "alpha", after))
	d.emit("meta", metaEvent(t, "alpha", after))
	if got := d.str(`document.querySelector("#conversation-status .status").textContent`); got != "closed" {
		t.Fatalf("meta status %q", got)
	}
	if d.str(`document.querySelectorAll("aside").length`) != "1" {
		t.Fatal("meta was added, not replaced")
	}
}

func TestStreamEventsKeepTheOpenSendDialog(t *testing.T) {
	cs := []client.Conversation{{Name: "job", Status: "open"}}
	p := transcriptFixture("job", "", 4)
	applyList(&p, &cs)
	d := loadPage(t, renderPage(t, p), "/conversations/job", "")
	d.run(`__click(document.getElementById("send-pen"));
var __f = { from: "alice", to: "bob\ncarol", kind: "say", body: "draft", txid: "tx1" };
for (var k in __f) document.getElementById(k).value = __f[k];
__click(document.querySelector("button.markdown"));
function __form() {
  var out = [];
  for (var k in __f) out.push(document.getElementById(k).value);
  return out.join("|") + "|" + document.getElementById("send-dialog").open + "|" + document.querySelector("li[data-seq=\"4\"] button.markdown").getAttribute("aria-pressed");
}`)
	want := "alice|bob\ncarol|say|draft|tx1|true|true"
	d.emit("message", messageEvent(t, "job", 5))
	d.emit("conversations", conversationsEvent(t, "job", cs))
	d.emit("meta", metaEvent(t, "job", []client.Conversation{{Name: "job", Status: "closed"}}))
	if got := d.str("__form()"); got != want {
		t.Fatalf("form %q", got)
	}
}

func TestArrivedFadesAndHoldsAStillTintUnderReducedMotion(t *testing.T) {
	html := renderPage(t, messagesFixture("", 1))
	for _, want := range []string{
		".arrived { animation: arrived 2.4s ease-out; }",
		"@keyframes arrived { from { background-color: var(--arrived); } }",
		"@keyframes arrived-still { from, to { background-color: var(--arrived); } }",
		"@media (prefers-reduced-motion: reduce) {\n  .arrived { animation-name: arrived-still; animation-timing-function: linear; }\n}",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("page style lacks %q", want)
		}
	}
}

func TestHiddenTabHoldsNoStream(t *testing.T) {
	d := loadPage(t, renderPage(t, messagesFixture("", 5)), "/", "")
	d.emit("message", messageEvent(t, "alpha", 8))
	d.run(`__setVisibility("hidden")`)
	if got := d.str("__streamURLs()"); got != "" {
		t.Fatalf("hidden streams %q", got)
	}
	d.run(`__setVisibility("visible")`)
	if got := d.str("__streamURLs()"); got != "/events?after=8" {
		t.Fatalf("visible streams %q", got)
	}

	d = loadPage(t, renderPage(t, messagesFixture("", 5)), "/", `document.visibilityState = "hidden"`)
	if got := d.str("__sources.length"); got != "0" {
		t.Fatalf("loaded hidden opened %s streams", got)
	}
	d.run(`__setVisibility("visible")`)
	if got := d.str("__streamURLs()"); got != "/events?after=5" {
		t.Fatalf("first visible %q", got)
	}
}

func TestResetReloadsTheNewestGroupAndReopens(t *testing.T) {
	d := loadPage(t, renderPage(t, messagesFixture("/?before=8", 9, 8)), "/", "")
	d.serve(true, renderPage(t, messagesFixture("", 7, 6)))
	d.run("__intersect()")
	if got := d.str(`__seqs("messages")`); got != "9,8,7,6" {
		t.Fatalf("older %q", got)
	}
	d.run(`__pages.push({ hold: true })`)
	d.emit("reset", "")
	if got := d.str(`__fetches.join(" ")`); got != "/?before=8 streams=1 / streams=0" {
		t.Fatalf("fetches %q", got)
	}
	d.run(`__setVisibility("hidden"); __setVisibility("visible")`)
	if got := d.str("__sources.length + \" \" + __streamURLs()"); got != "1 " {
		t.Fatalf("stream during reload %q", got)
	}
	d.set("__arg", renderPage(t, messagesFixture("/?before=119", 120, 119)))
	d.run(`__release({ ok: true, html: __arg })`)
	if got := d.str(`__seqs("messages") + " " + __arrived("messages") + " " + __attr("more", "data-next")`); got != "120,119  /?before=119" {
		t.Fatalf("after reset %q", got)
	}
	if got := d.str("__streamURLs()"); got != "/events?after=120" {
		t.Fatalf("reopened %q", got)
	}
	d.serve(true, renderPage(t, messagesFixture("", 118)))
	d.run("__intersect()")
	if got := d.str(`__seqs("messages")`); got != "120,119,118" {
		t.Fatalf("older after reset %q", got)
	}
}

func TestOlderLoadForAReplacedRegionIsDropped(t *testing.T) {
	d := loadPage(t, renderPage(t, messagesFixture("/?before=8", 9, 8)), "/", "")
	d.run(`__pages.push({ hold: true })`)
	d.run("__intersect()")
	d.serve(true, renderPage(t, messagesFixture("/?before=119", 120, 119)))
	d.emit("reset", "")
	d.serve(true, renderPage(t, messagesFixture("/?before=118", 118)))
	d.run("__intersect()")
	if got := d.str(`__seqs("messages") + " " + __attr("more", "data-next")`); got != "120,119,118 /?before=118" {
		t.Fatalf("stale older load blocked the new marker %q", got)
	}
	d.run(`__release({ network: true })`)
	if got := d.str(`__seqs("messages") + " " + __attr("more", "data-next") + " " + __text_of("group-error")`); got != "120,119,118 /?before=118 " {
		t.Fatalf("stale older load applied %q", got)
	}
	d.serve(true, renderPage(t, messagesFixture("", 117)))
	d.run("__intersect()")
	if got := d.str(`__seqs("messages")`); got != "120,119,118,117" {
		t.Fatalf("older after stale group %q", got)
	}
}

func TestFailedResetReloadRetriesWithBackoffWhileVisible(t *testing.T) {
	d := loadPage(t, renderPage(t, transcriptFixture("job", "", 3, 2)), "/conversations/job", "")
	if d.str(`__attr("more", "data-next")`) != "<none>" {
		t.Fatal("short group has an older marker")
	}
	d.serveNetworkFailure()
	d.emit("reset", "")
	if got := d.str(`__text_of("group-error") + "|" + __streamURLs() + "|" + __pendingDelays()`); got != "messages did not load||2000" {
		t.Fatalf("failed reload %q", got)
	}
	notice := transcriptFixture("job", "")
	notice.Group, notice.Notice = nil, "bus down"
	d.serve(true, renderPage(t, notice))
	d.run(`__flushTimers()`)
	if got := d.str(`__text_of("group-error") + "|" + __streamURLs() + "|" + __pendingDelays()`); got != "bus down||4000" {
		t.Fatalf("timed retry %q", got)
	}
	d.run(`__setVisibility("hidden")`)
	if got := d.str(`__pendingDelays()`); got != "" {
		t.Fatalf("hidden tab keeps a retry %q", got)
	}
	d.serveNetworkFailure()
	d.run(`__setVisibility("visible")`)
	if got := d.str(`__fetches.length + "|" + __pendingDelays()`); got != "3|8000" {
		t.Fatalf("shown tab retry %q", got)
	}
	for i := 0; i < 3; i++ {
		d.serveNetworkFailure()
		d.run(`__flushTimers()`)
	}
	if got := d.str(`__pendingDelays()`); got != "30000" {
		t.Fatalf("backoff cap %q", got)
	}
	d.serve(true, renderPage(t, transcriptFixture("job", "/conversations/job?before=60", 61, 60)))
	d.run(`__flushTimers()`)
	if got := d.str(`__seqs("transcript") + "|" + __text_of("group-error") + "|" + __streamURLs() + "|" + __pendingDelays()`); got != "61,60||/events?conversation=job&after=61|" {
		t.Fatalf("retry %q", got)
	}
	d.serve(true, renderPage(t, transcriptFixture("job", "", 59)))
	d.run("__intersect()")
	if got := d.str(`__seqs("transcript")`); got != "61,60,59" {
		t.Fatalf("older after retry %q", got)
	}
	d.serveNetworkFailure()
	d.emit("reset", "")
	if got := d.str(`__pendingDelays()`); got != "2000" {
		t.Fatalf("backoff after recovery %q", got)
	}
}

func TestMessageOnAFailedReadReloadsTheGroup(t *testing.T) {
	refused := transcriptFixture("job", "")
	refused.Group = &messageGroup{Outcome: "refused", Reason: "nope"}
	d := loadPage(t, renderPage(t, refused), "/conversations/job", "")
	d.serve(false, "boom")
	d.emit("message", messageEvent(t, "job", 4))
	if got := d.str(`__fetches.join(" ") + "|" + __text_of("read-outcome") + "|" + __text_of("group-error") + "|" + __streamURLs()`); got != "/conversations/job streams=0|refused|messages did not load|" {
		t.Fatalf("failed reload %q", got)
	}
	d.serve(true, renderPage(t, transcriptFixture("job", "", 4)))
	d.run(`__setVisibility("hidden"); __setVisibility("visible")`)
	if got := d.str(`__seqs("transcript") + "|" + __text_of("read-outcome") + "|" + __text_of("read-reason") + "|" + __text_of("group-error") + "|" + __streamURLs()`); got != "4|<none>|<none>||/events?conversation=job&after=4" {
		t.Fatalf("reloaded %q", got)
	}
}

func TestConversationsOnAFailedReadReloadsAQuietGroup(t *testing.T) {
	cs := []client.Conversation{{Name: "job", Status: "open"}}
	cases := []struct {
		name, url, conv, list, empty, stream string
		failed, fresh                        page
	}{
		{
			name: "messages", url: "/", list: "messages", empty: "empty", stream: "/events?after=0",
			failed: page{Kind: kindMessages, Group: &messageGroup{Outcome: "unavailable"}},
			fresh:  messagesFixture(""),
		},
		{
			name: "transcript", url: "/conversations/job", conv: "job", list: "transcript", empty: "transcript-empty",
			stream: "/events?conversation=job&after=0",
			failed: func() page {
				p := transcriptFixture("job", "")
				p.Group = &messageGroup{Outcome: "unavailable"}
				return p
			}(),
			fresh: transcriptFixture("job", ""),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := loadPage(t, renderPage(t, c.failed), c.url, "")
			d.serve(true, renderPage(t, c.fresh))
			d.emit("conversations", conversationsEvent(t, c.conv, cs))
			if got := d.str(`__fetches.join(" ")`); got != c.url+" streams=0" {
				t.Fatalf("fetches %q", got)
			}
			got := d.str(`__text_of("read-outcome") + "|" + __text_of(` + quoteJS(c.empty) + `) + "|" + __streamURLs()`)
			if got != "<none>|No messages.|"+c.stream {
				t.Fatalf("recovered %q", got)
			}
			d.emit("conversations", conversationsEvent(t, c.conv, cs))
			if got := d.str(`__fetches.length`); got != "1" {
				t.Fatalf("a loaded group reloaded again: %s fetches", got)
			}
		})
	}
}

func TestRecoveryClearsALoadNoticeButNotASendNotice(t *testing.T) {
	cases := []struct {
		name, url, conv, list string
		page                  page
		want                  string
	}{
		{name: "messages", page: messagesFixture(""), url: "/", conv: "alpha", list: "messages", want: "<none>"},
		{name: "transcript", page: transcriptFixture("job", ""), url: "/conversations/job", conv: "job", list: "transcript", want: "<none>"},
		{name: "failed send", page: transcriptFixture("job", ""), url: "/conversations/job/messages", conv: "job", list: "transcript", want: "bus down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failed := tc.page
			failed.Group, failed.Notice = nil, "bus down"
			d := loadPage(t, renderPage(t, failed), tc.url, "")
			recovered := tc.page
			recovered.Group = fixtureGroup(tc.conv, "", 4)
			d.serve(true, renderPage(t, recovered))
			d.emit("message", messageEvent(t, tc.conv, 4))
			if got := d.str(`__seqs("` + tc.list + `") + "|" + __text_of("notice")`); got != "4|"+tc.want {
				t.Fatalf("after reload %q", got)
			}
		})
	}

	d := loadPage(t, renderPage(t, page{Kind: kindList, Notice: "bus down"}), "/conversations", "")
	d.emit("conversations", conversationsEvent(t, "", []client.Conversation{{Name: "job", Status: "open"}}))
	if got := d.str(`__names("open-conversations") + "|" + __text_of("notice")`); got != "job|<none>" {
		t.Fatalf("list after event %q", got)
	}
}

func TestResetFromAnOlderGroupDropsBeforeOnce(t *testing.T) {
	d := loadPage(t, renderPage(t, transcriptFixture("job", "/conversations/job?before=38", 39, 38)), "/conversations/job?before=40", "")
	d.serve(true, renderPage(t, transcriptFixture("job", "/conversations/job?before=199", 200, 199)))
	d.emit("reset", "")
	if got := d.str(`__fetches.join(" ") + "|" + __replaced.join(" ") + "|" + location.search`); got != "/conversations/job streams=0|/conversations/job|" {
		t.Fatalf("reload %q", got)
	}
	if got := d.str(`__sources.length + " " + __streamURLs()`); got != "2 /events?conversation=job&after=200" {
		t.Fatalf("streams %q", got)
	}
}

func TestInsertKeepsTheReaderStillWithoutScrollAnchoring(t *testing.T) {
	cases := []struct {
		name      string
		page      page
		url       string
		conv      string
		layout    string
		scroller  string
		anchoring bool
		top       int
		want      int
	}{
		{name: "messages", page: messagesFixture("", 4), url: "/", conv: "alpha", layout: `document.getElementById("main")._overflowY = "auto"`, scroller: `document.getElementById("main")`, top: 300, want: 400},
		{name: "wide transcript", page: transcriptFixture("job", "", 4), url: "/conversations/job", conv: "job", layout: `document.getElementById("main")._overflowY = "hidden"; document.getElementById("transcript-section")._overflowY = "auto"`, scroller: `document.getElementById("transcript-section")`, top: 300, want: 400},
		{name: "narrow transcript", page: transcriptFixture("job", "", 4), url: "/conversations/job", conv: "job", scroller: "document.scrollingElement", top: 300, want: 400},
		{name: "narrow empty", page: messagesFixture(""), url: "/", conv: "alpha", scroller: "document.scrollingElement", top: 300, want: 400},
		{name: "at top", page: messagesFixture("", 4), url: "/", conv: "alpha", layout: `document.getElementById("main")._overflowY = "auto"`, scroller: `document.getElementById("main")`, top: 0, want: 0},
		{name: "anchoring", page: messagesFixture("", 4), url: "/", conv: "alpha", layout: `document.getElementById("main")._overflowY = "auto"`, scroller: `document.getElementById("main")`, anchoring: true, top: 300, want: 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := fmt.Sprintf("__anchoring = %t; %s; var __scroller = %s; __scroller.scrollTop = %d", tc.anchoring, tc.layout, tc.scroller, tc.top)
			d := loadPage(t, renderPage(t, tc.page), tc.url, setup)
			d.emit("message", messageEvent(t, tc.conv, 9))
			if got := d.str("String(__scroller.scrollTop)"); got != fmt.Sprint(tc.want) {
				t.Fatalf("scrollTop %s", got)
			}
		})
	}
}

// pageDOM is one rendered page loaded into the fake DOM with its script running.
type pageDOM struct {
	t  *testing.T
	vm *goja.Runtime
}

// loadPage parses html as the page at url, runs setup, then runs the page script.
func loadPage(t *testing.T, html, url, setup string) *pageDOM {
	t.Helper()
	script, err := pageScript(html)
	if err != nil {
		t.Fatal(err)
	}
	d := &pageDOM{t: t, vm: goja.New()}
	d.run(pageHarness)
	d.set("__arg", html)
	d.run("__load(__arg, " + quoteJS(url) + ")")
	if setup != "" {
		d.run(setup)
	}
	d.run(script)
	return d
}

func (d *pageDOM) run(js string) {
	d.t.Helper()
	if _, err := d.vm.RunString(js); err != nil {
		d.t.Fatal(err)
	}
}

func (d *pageDOM) str(expr string) string {
	d.t.Helper()
	v, err := d.vm.RunString(expr)
	if err != nil {
		d.t.Fatal(err)
	}
	return v.String()
}

func (d *pageDOM) set(name string, v any) {
	d.t.Helper()
	if err := d.vm.Set(name, v); err != nil {
		d.t.Fatal(err)
	}
}

// serve queues the answer to the next fetch.
func (d *pageDOM) serve(ok bool, html string) {
	d.t.Helper()
	d.set("__arg", html)
	d.run(fmt.Sprintf("__pages.push({ ok: %t, html: __arg })", ok))
}

func (d *pageDOM) serveNetworkFailure() {
	d.t.Helper()
	d.run("__pages.push({ network: true })")
}

// emit delivers one event to the single open stream.
func (d *pageDOM) emit(event, data string) {
	d.t.Helper()
	d.set("__arg", data)
	d.run("__emit(" + quoteJS(event) + ", __arg)")
}

func fixtureItem(conv string, seq int64) messageItem {
	return newMessageItem(conv, client.Message{
		Seq: seq, Time: "2026-10-31T00:00:00Z", From: "alice", To: []string{"bob"},
		Body: fmt.Sprintf("body **%d**", seq), TxID: fmt.Sprintf("t%d", seq), Kind: "say",
	})
}

// fixtureGroup lists seqs newest first. A non-empty next marks the group full.
func fixtureGroup(conv, next string, seqs ...int64) *messageGroup {
	g := &messageGroup{Outcome: "ok", Full: next != "", NextPage: next}
	for _, s := range seqs {
		g.Messages = append(g.Messages, fixtureItem(conv, s))
	}
	return g
}

func messagesFixture(next string, seqs ...int64) page {
	return page{Kind: kindMessages, Group: fixtureGroup("alpha", next, seqs...)}
}

func transcriptFixture(name, next string, seqs ...int64) page {
	p := conversationPage(name)
	p.Group = fixtureGroup(name, next, seqs...)
	return p
}

func messageEvent(t *testing.T, conv string, seq int64) string {
	t.Helper()
	frag, err := renderBlock("messageItem", fixtureItem(conv, seq))
	if err != nil {
		t.Fatal(err)
	}
	return frag
}

// conversationsEvent is the conversations event data a stream for name sends.
func conversationsEvent(t *testing.T, name string, cs []client.Conversation) string {
	t.Helper()
	p := page{Name: name}
	applyList(&p, &cs)
	sections, err := renderBlock("convSections", p)
	if err != nil {
		t.Fatal(err)
	}
	index, err := renderBlock("convIndex", p)
	if err != nil {
		t.Fatal(err)
	}
	return sections + index
}

func metaEvent(t *testing.T, name string, cs []client.Conversation) string {
	t.Helper()
	p := page{Name: name}
	applyList(&p, &cs)
	frag, err := renderBlock("convMeta", p)
	if err != nil {
		t.Fatal(err)
	}
	return frag
}

func pageScript(html string) (string, error) {
	const open, close = "<script>", "</script>"
	i := strings.Index(html, open)
	if i < 0 {
		return "", errors.New("page has no script")
	}
	rest := html[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", errors.New("page script is not closed")
	}
	if strings.Contains(rest[j+len(close):], open) {
		return "", errors.New("page has more than one script")
	}
	return rest[:j], nil
}

func renderPage(t *testing.T, p page) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, p); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func quoteJS(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// pageHarness is a fake browser for the page script: a DOM built from the
// rendered page, plus EventSource, visibilitychange, animationend, fetch,
// DOMParser, IntersectionObserver, scroll state, and history.
const pageHarness = `
var window = this;
window.innerHeight = 100;
function __Node(type, tag) {
  this.nodeType = type;
  this.localName = tag || "";
  this.tagName = (tag || "").toUpperCase();
  this.attrs = {};
  this.childNodes = [];
  this.parentNode = null;
  this.data = "";
  this.listeners = {};
  this.scrollTop = 0;
}
var __P = __Node.prototype;
function __has(o, k) { return Object.prototype.hasOwnProperty.call(o, k); }
__P.getAttribute = function (k) { return __has(this.attrs, k) ? this.attrs[k] : null; };
__P.setAttribute = function (k, v) { this.attrs[k] = String(v); };
__P.removeAttribute = function (k) { delete this.attrs[k]; };
__P.hasAttribute = function (k) { return __has(this.attrs, k); };
function __reflect(prop, attr) {
  Object.defineProperty(__P, prop, {
    get: function () { var v = this.getAttribute(attr); return v === null ? "" : v; },
    set: function (v) { this.setAttribute(attr, v); }
  });
}
__reflect("id", "id");
__reflect("className", "class");
__reflect("href", "href");
Object.defineProperty(__P, "hidden", {
  get: function () { return this.hasAttribute("hidden"); },
  set: function (v) { if (v) this.setAttribute("hidden", ""); else this.removeAttribute("hidden"); }
});
Object.defineProperty(__P, "open", { get: function () { return this.hasAttribute("open"); } });
Object.defineProperty(__P, "classList", { get: function () {
  var el = this;
  function names() {
    var c = el.getAttribute("class");
    return c ? c.split(/\s+/).filter(function (s) { return s !== ""; }) : [];
  }
  return {
    contains: function (n) { return names().indexOf(n) >= 0; },
    add: function (n) { var l = names(); if (l.indexOf(n) < 0) { l.push(n); el.setAttribute("class", l.join(" ")); } },
    remove: function (n) { var l = names(); var i = l.indexOf(n); if (i >= 0) { l.splice(i, 1); el.setAttribute("class", l.join(" ")); } }
  };
}});
Object.defineProperty(__P, "children", { get: function () {
  return this.childNodes.filter(function (n) { return n.nodeType === 1; });
}});
Object.defineProperty(__P, "firstChild", { get: function () { return this.childNodes[0] || null; } });
Object.defineProperty(__P, "textContent", {
  get: function () {
    if (this.nodeType === 3) return this.data;
    var s = "";
    for (var i = 0; i < this.childNodes.length; i++) s += this.childNodes[i].textContent;
    return s;
  },
  set: function (v) {
    for (var i = 0; i < this.childNodes.length; i++) this.childNodes[i].parentNode = null;
    this.childNodes = [];
    if (String(v) !== "") this.appendChild(__text(String(v)));
  }
});
Object.defineProperty(__P, "value", {
  get: function () {
    if (this._value !== undefined) return this._value;
    if (this.localName === "textarea") return this.textContent;
    var v = this.getAttribute("value");
    return v === null ? "" : v;
  },
  set: function (v) { this._value = String(v); }
});
Object.defineProperty(__P, "offsetHeight", { get: function () {
  if (this._h !== undefined) return this._h;
  if (this.nodeType === 1 && this.classList.contains("message")) return 100;
  return this.scrollHeight;
}});
Object.defineProperty(__P, "scrollHeight", { get: function () {
  var h = 0;
  for (var i = 0; i < this.childNodes.length; i++) h += this.childNodes[i].nodeType === 1 ? this.childNodes[i].offsetHeight : 0;
  return h;
}});
__P.getBoundingClientRect = function () {
  var top = this._top === undefined ? 1000 : this._top;
  return { top: top, bottom: top + 1 };
};
__P.appendChild = function (c) { return this.insertBefore(c, null); };
__P.insertBefore = function (c, ref) {
  if (c.parentNode) c.parentNode.removeChild(c);
  var i = ref ? this.childNodes.indexOf(ref) : -1;
  if (ref && i < 0) throw new Error("insertBefore: reference is not a child");
  if (i < 0) this.childNodes.push(c); else this.childNodes.splice(i, 0, c);
  c.parentNode = this;
  return c;
};
__P.removeChild = function (c) {
  var i = this.childNodes.indexOf(c);
  if (i < 0) throw new Error("removeChild: not a child");
  this.childNodes.splice(i, 1);
  c.parentNode = null;
  return c;
};
__P.replaceChild = function (fresh, old) {
  this.insertBefore(fresh, old);
  return this.removeChild(old);
};
__P.cloneNode = function (deep) {
  var c = new __Node(this.nodeType, this.localName);
  c.data = this.data;
  for (var k in this.attrs) c.attrs[k] = this.attrs[k];
  if (deep) for (var i = 0; i < this.childNodes.length; i++) c.appendChild(this.childNodes[i].cloneNode(true));
  return c;
};
__P.addEventListener = function (type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); };
__P.removeEventListener = function (type, fn) {
  var l = this.listeners[type] || [];
  var i = l.indexOf(fn);
  if (i >= 0) l.splice(i, 1);
};
__P.dispatchEvent = function (ev) {
  if (!ev.target) ev.target = this;
  if (!ev.preventDefault) ev.preventDefault = function () { ev.defaultPrevented = true; };
  var l = (this.listeners[ev.type] || []).slice();
  for (var i = 0; i < l.length; i++) l[i].call(this, ev);
  if (ev.bubbles && this.parentNode) this.parentNode.dispatchEvent(ev);
  return !ev.defaultPrevented;
};
function __all(node) {
  var out = [];
  (function walk(n) {
    for (var i = 0; i < n.childNodes.length; i++) {
      if (n.childNodes[i].nodeType !== 1) continue;
      out.push(n.childNodes[i]);
      walk(n.childNodes[i]);
    }
  })(node);
  return out;
}
function __compound(n, c) {
  var tag = /^[a-zA-Z0-9-]*/.exec(c)[0];
  if (tag && n.localName !== tag.toLowerCase()) return false;
  var rest = c.slice(tag.length);
  var re = /([.#])([\w-]+)|\[([\w-]+)(?:="([^"]*)")?\]/g;
  var used = 0;
  var t;
  while ((t = re.exec(rest))) {
    used += t[0].length;
    if (t[1] === ".") { if (!n.classList.contains(t[2])) return false; }
    else if (t[1] === "#") { if (n.getAttribute("id") !== t[2]) return false; }
    else {
      var v = n.getAttribute(t[3]);
      if (v === null || (t[4] !== undefined && v !== t[4])) return false;
    }
  }
  if (used !== rest.length) throw new Error("unsupported selector " + c);
  return true;
}
function __matches(n, sel, root) {
  var parts = sel.split(/\s+/).filter(function (s) { return s !== ""; });
  if (!__compound(n, parts[parts.length - 1])) return false;
  var up = n.parentNode;
  for (var i = parts.length - 2; i >= 0; i--) {
    while (up && up !== root && !(up.nodeType === 1 && __compound(up, parts[i]))) up = up.parentNode;
    if (!up || up === root) return false;
    up = up.parentNode;
  }
  return true;
}
__P.querySelectorAll = function (sel) {
  var self = this;
  return __all(this).filter(function (n) { return __matches(n, sel, self); });
};
__P.querySelector = function (sel) { return this.querySelectorAll(sel)[0] || null; };
__P.getElementsByTagName = function (tag) {
  return __all(this).filter(function (n) { return n.localName === tag; });
};
__P.getElementById = function (id) {
  var all = __all(this);
  for (var i = 0; i < all.length; i++) if (all[i].getAttribute("id") === id) return all[i];
  return null;
};
var __shownCount = 0;
__P.showModal = function () {
  if (this.open) throw new Error("dialog already open");
  this.setAttribute("open", "");
  __shownCount++;
};
__P.close = function () { this.removeAttribute("open"); };
function __text(s) { var t = new __Node(3); t.data = s; return t; }
function __decode(s) {
  var named = { amp: "&", lt: "<", gt: ">", quot: "\"", apos: "'", nbsp: " " };
  return s.replace(/&(#[xX][0-9a-fA-F]+|#[0-9]+|[a-zA-Z]+);/g, function (all, e) {
    if (e.charAt(0) === "#") {
      var hex = e.charAt(1) === "x" || e.charAt(1) === "X";
      return String.fromCharCode(hex ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
    }
    return __has(named, e) ? named[e] : all;
  });
}
var __voids = { area: 1, base: 1, br: 1, col: 1, embed: 1, hr: 1, img: 1, input: 1, link: 1, meta: 1, source: 1, track: 1, wbr: 1 };
var __raw = { script: 1, style: 1 };
// __parse builds a document from well-formed template output. It is not a full HTML parser.
function __parse(html) {
  var root = __document();
  var stack = [root];
  var i = 0;
  var n = html.length;
  function top() { return stack[stack.length - 1]; }
  while (i < n) {
    var lt = html.indexOf("<", i);
    if (lt < 0) lt = n;
    if (lt > i) { top().appendChild(__text(__decode(html.slice(i, lt)))); i = lt; continue; }
    if (html.substr(i, 4) === "<!--") { var c = html.indexOf("-->", i); i = c < 0 ? n : c + 3; continue; }
    if (html.charAt(i + 1) === "!") { var d = html.indexOf(">", i); i = d < 0 ? n : d + 1; continue; }
    var end = /^<\/([a-zA-Z][a-zA-Z0-9-]*)\s*>/.exec(html.slice(i));
    if (end) {
      var name = end[1].toLowerCase();
      for (var s = stack.length - 1; s > 0; s--) {
        if (stack[s].localName === name) { stack.length = s; break; }
      }
      i += end[0].length;
      continue;
    }
    var open = /^<([a-zA-Z][a-zA-Z0-9-]*)/.exec(html.slice(i));
    if (!open) { top().appendChild(__text("<")); i++; continue; }
    var el = new __Node(1, open[1].toLowerCase());
    i += open[0].length;
    var selfClose = false;
    for (;;) {
      while (/\s/.test(html.charAt(i))) i++;
      if (html.charAt(i) === ">") { i++; break; }
      if (html.substr(i, 2) === "/>") { selfClose = true; i += 2; break; }
      var an = /^[^\s"'>\/=]+/.exec(html.slice(i));
      if (!an) throw new Error("bad attribute at " + i);
      i += an[0].length;
      var val = "";
      if (html.charAt(i) === "=") {
        i++;
        var q = html.charAt(i);
        if (q === "\"" || q === "'") {
          var close = html.indexOf(q, i + 1);
          val = html.slice(i + 1, close);
          i = close + 1;
        } else {
          var uv = /^[^\s>]+/.exec(html.slice(i))[0];
          val = uv;
          i += uv.length;
        }
      }
      el.attrs[an[0].toLowerCase()] = __decode(val);
    }
    top().appendChild(el);
    if (selfClose || __voids[el.localName]) continue;
    if (__raw[el.localName]) {
      var stop = html.toLowerCase().indexOf("</" + el.localName, i);
      if (stop < 0) stop = n;
      el.appendChild(__text(html.slice(i, stop)));
      var gt = html.indexOf(">", stop);
      i = gt < 0 ? n : gt + 1;
      continue;
    }
    stack.push(el);
  }
  return root;
}
function __document() {
  var doc = new __Node(9, "#document");
  doc.visibilityState = "visible";
  doc.createElement = function (tag) { return new __Node(1, tag.toLowerCase()); };
  doc.createTextNode = __text;
  doc.importNode = function (node, deep) { return node.cloneNode(deep); };
  Object.defineProperty(doc, "documentElement", { get: function () { return this.querySelector("html") || this; } });
  Object.defineProperty(doc, "body", { get: function () { return this.querySelector("body") || this; } });
  Object.defineProperty(doc, "scrollingElement", { get: function () { return this.documentElement; } });
  return doc;
}
function DOMParser() {}
DOMParser.prototype.parseFromString = function (html) { return __parse(html); };
function getComputedStyle(el) { return { overflowY: el._overflowY || "visible" }; }
var __anchoring = true;
var CSS = { supports: function (prop, value) { return prop === "overflow-anchor" && value === "auto" && __anchoring; } };
var location = { pathname: "/", search: "" };
var __replaced = [];
var history = { state: null, replaceState: function (state, title, url) {
  __replaced.push(url);
  var q = url.indexOf("?");
  location.pathname = q < 0 ? url : url.slice(0, q);
  location.search = q < 0 ? "" : url.slice(q);
}};
var __timers = [];
function setTimeout(fn, delay) { __timers.push({ fn: fn, delay: delay || 0 }); return __timers.length; }
function clearTimeout(id) { if (__timers[id - 1]) __timers[id - 1].fn = null; }
function __pendingDelays() { return __timers.filter(function (t) { return t.fn; }).map(function (t) { return t.delay; }).join(","); }
function __flushTimers() {
  for (var i = 0; i < __timers.length; i++) {
    var fn = __timers[i].fn;
    if (!fn) continue;
    __timers[i].fn = null;
    fn();
    return;
  }
}
var __randomCalls = 0;
var crypto = { getRandomValues: function (buf) {
  __randomCalls++;
  for (var i = 0; i < buf.length; i++) buf[i] = (i * 17 + 3) & 255;
  return buf;
}};
var __lastCopy = "";
var __writeFails = false;
var navigator = { clipboard: { writeText: function (text) {
  if (__writeFails) return { then: function (ok, err) { err(); } };
  __lastCopy = text;
  return { then: function (ok) { ok(); } };
}}};
// Promise settles at once, so a fetch chain runs to the end inside one call.
function Promise(executor) {
  if (!(this instanceof Promise)) return new Promise(executor);
  this.state = "pending";
  this.value = undefined;
  this.done = [];
  var self = this;
  function settle(state, value) {
    if (self.state !== "pending") return;
    self.state = state;
    self.value = value;
    var wait = self.done.slice();
    self.done = [];
    for (var i = 0; i < wait.length; i++) wait[i]();
  }
  this.then = function (onOk, onErr) {
    return new Promise(function (resolve, reject) {
      function run() {
        try {
          var fn = self.state === "fulfilled" ? onOk : onErr;
          if (typeof fn !== "function") {
            if (self.state === "fulfilled") resolve(self.value);
            else reject(self.value);
            return;
          }
          var out = fn(self.value);
          if (out && typeof out.then === "function") out.then(resolve, reject);
          else resolve(out);
        } catch (err) {
          reject(err);
        }
      }
      if (self.state === "pending") self.done.push(run);
      else run();
    });
  };
  this.catch = function (onErr) { return this.then(null, onErr); };
  try {
    executor(function (v) { settle("fulfilled", v); }, function (e) { settle("rejected", e); });
  } catch (err) {
    settle("rejected", err);
  }
}
// __pages answers fetch in order: {ok, html}, {network: true}, or {hold: true}.
var __pages = [];
var __fetches = [];
var __held = null;
function fetch(url) {
  __fetches.push(url + " streams=" + __openStreams().length);
  var job = __pages.shift();
  // Thrown outside the promise, so it fails the test instead of reading as a network failure.
  if (!job) throw new Error("unexpected fetch " + url);
  return new Promise(function (resolve, reject) {
    function answer(job) {
      if (job.network) { reject(new Error("network")); return; }
      resolve({ ok: job.ok, text: function () { return new Promise(function (r) { r(job.html); }); } });
    }
    if (job.hold) { __held = answer; return; }
    answer(job);
  });
}
function __release(job) { var answer = __held; __held = null; answer(job); }
var __observers = [];
function IntersectionObserver(cb) { this.cb = cb; this.targets = []; __observers.push(this); }
IntersectionObserver.prototype.observe = function (el) { this.targets.push(el); };
IntersectionObserver.prototype.unobserve = function (el) {
  var i = this.targets.indexOf(el);
  if (i >= 0) this.targets.splice(i, 1);
};
function __intersect() {
  var more = document.getElementById("more");
  for (var i = 0; i < __observers.length; i++) {
    if (__observers[i].targets.indexOf(more) >= 0) __observers[i].cb([{ isIntersecting: true, target: more }]);
  }
}
var __sources = [];
function EventSource(url) { this.url = url; this.closed = false; this.listeners = {}; __sources.push(this); }
EventSource.prototype.addEventListener = function (type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); };
EventSource.prototype.close = function () { this.closed = true; };
function __openStreams() { return __sources.filter(function (s) { return !s.closed; }); }
function __streamURLs() { return __openStreams().map(function (s) { return s.url; }).join(" "); }
function __emit(type, data, id) {
  var open = __openStreams();
  if (open.length !== 1) throw new Error("emit " + type + " with " + open.length + " open streams");
  var l = (open[0].listeners[type] || []).slice();
  var ev = { type: type, data: data, lastEventId: id || "" };
  for (var i = 0; i < l.length; i++) l[i].call(open[0], ev);
}
function __setVisibility(state) {
  document.visibilityState = state;
  document.dispatchEvent({ type: "visibilitychange" });
}
function __animationEnd(el) { el.dispatchEvent({ type: "animationend", bubbles: true }); }
function __click(el) { el.dispatchEvent({ type: "click", bubbles: true }); }
var document = __document();
function __load(html, url) {
  document = __parse(html);
  var q = url.indexOf("?");
  location.pathname = q < 0 ? url : url.slice(0, q);
  location.search = q < 0 ? "" : url.slice(q);
}
function __seqs(id) {
  var list = document.getElementById(id);
  if (!list) return "";
  return list.children.map(function (li) { return li.getAttribute("data-seq"); }).join(",");
}
function __arrived(id) {
  var list = document.getElementById(id);
  if (!list) return "";
  return list.querySelectorAll(".arrived").map(function (li) {
    return li.getAttribute("data-seq") || li.querySelector(".conv-name").textContent;
  }).join(",");
}
function __names(id) {
  var list = document.getElementById(id);
  if (!list) return "";
  return list.querySelectorAll(".conv-name").map(function (n) { return n.textContent; }).join(",");
}
function __text_of(id) { var el = document.getElementById(id); return el ? el.textContent : "<none>"; }
function __attr(id, name) {
  var el = document.getElementById(id);
  if (!el) return "<none>";
  var v = el.getAttribute(name);
  return v === null ? "<unset>" : v;
}
`
