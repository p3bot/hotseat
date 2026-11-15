// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func replyItem(conv string, seq, re int64) messageItem {
	item := fixtureItem(conv, seq)
	item.Message.Re = &re
	return item
}

func historyFixture(name, next string, seqs ...int64) page {
	p := transcriptFixture(name, next, seqs...)
	p.History = true
	p.NewestHref = conversationPath(name)
	return p
}

func TestMessageFootLinksReToTheGroupThatHoldsIt(t *testing.T) {
	const link = `<a class="re" href="/conversations/beta?before=4#msg-3">re 3</a>`
	cases := []struct {
		name string
		item messageItem
		want string
	}{
		{"reply", replyItem("beta", 9, 3), `<span class="seq-n">9</span>` + link + `</span><span class="kind"`},
		{"conversation without a link", replyItem("..", 9, 3), `<span class="seq-n">9</span><span class="re">re 3</span></span>`},
		{"no reply", fixtureItem("beta", 9), `<span class="seq-n">9</span></span><span class="kind"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := map[string]string{
				"messages": renderPage(t, page{Kind: kindMessages, Group: &messageGroup{Outcome: "ok", Messages: []messageItem{tc.item}}}),
			}
			frag, err := renderBlock("messageItem", tc.item)
			if err != nil {
				t.Fatal(err)
			}
			pages["event"] = frag
			if tc.item.Conversation == "beta" {
				transcript := conversationPage("beta")
				transcript.Group = &messageGroup{Outcome: "ok", Messages: []messageItem{tc.item}}
				pages["transcript"] = renderPage(t, transcript)
			}
			for where, html := range pages {
				if !strings.Contains(html, tc.want) {
					t.Fatalf("%s foot lacks %s\n%s", where, tc.want, html)
				}
				if strings.Contains(tc.want, `class="re"`) != strings.Contains(html, `class="re"`) {
					t.Fatalf("%s re shown wrongly\n%s", where, html)
				}
			}
		})
	}
}

func TestTranscriptServedWithBeforeIsAHistoryViewLinkingTheNewestGroup(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "a")
	first := publishSeq(t, addr, "", "a", "first")
	for i := range groupLimit + 5 {
		publishSeq(t, addr, "", "a", "later"+strconv.FormatInt(i, 10))
	}
	re := first
	res := busCall(t, addr, "", "publish", client.PublishRequest{
		Conversation: "a", From: "bob", To: []string{"alice"}, Body: "reply", TxID: "reply", Kind: "say", Re: &re,
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("reply %+v", res)
	}
	href := "/conversations/a?before=" + strconv.FormatInt(first+1, 10) + "#msg-" + strconv.FormatInt(first, 10)

	for _, path := range []string{"/", "/conversations/a"} {
		status, body, _ := doPage(t, mustGet(t, page.URL+path))
		if status != http.StatusOK || !strings.Contains(body, `<a class="re" href="`+strings.ReplaceAll(href, "&", "&amp;")+`">`) {
			t.Fatalf("%s lacks the re link %s\n%s", path, href, body)
		}
		if strings.Contains(body, " data-history>") || strings.Contains(body, `id="newest"`) {
			t.Fatalf("%s is a history view\n%s", path, body)
		}
	}

	target, _, _ := strings.Cut(href, "#")
	status, body, _ := doPage(t, mustGet(t, page.URL+target))
	if status != http.StatusOK {
		t.Fatalf("history %d\n%s", status, body)
	}
	if !strings.Contains(body, `<section id="transcript-section" data-history>`) || !strings.Contains(body, `<p id="newest"><a href="/conversations/a">Newest messages</a></p>`) {
		t.Fatalf("history markers\n%s", body)
	}
	at := strings.Index(body, `<ol id="transcript">`)
	if at < 0 {
		t.Fatalf("history has no transcript list\n%s", body)
	}
	list := body[at:]
	top := `<li class="message" id="msg-` + strconv.FormatInt(first, 10) + `"`
	if !strings.HasPrefix(strings.TrimPrefix(list, "<ol id=\"transcript\">\n"), top) {
		t.Fatalf("referenced message is not the top of the group\n%s", list)
	}
}

func TestHistoryStreamSendsNoMessageOrResetAndFollowsClose(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "a")
	createConv(t, addr, "", "b")
	for i := range groupLimit + 5 {
		publishSeq(t, addr, "", "a", "a"+strconv.FormatInt(i, 10))
	}

	s := openEvents(t, page.URL+"/events?conversation=a&view=history", nil)
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
	proxy.waitCount(t, bus.PathWatch, 2)
	publishSeq(t, addr, "", "b", "b1")
	publishSeq(t, addr, "", "a", "a-live")
	if res := busCall(t, addr, "", "close", client.CloseRequest{Conversation: "a"}); res.Outcome != bus.OutcomeOK {
		t.Fatalf("close %+v", res)
	}
	for {
		ev := s.nextExcept(t, eventConversations)
		if ev.Name == eventMeta && strings.Contains(ev.Data, `data-status="closed"`) {
			break
		}
		if ev.Name != eventMeta {
			t.Fatalf("history sent %+v", ev)
		}
	}
	s.close()
	for _, name := range []string{"event: " + eventMessage, "event: " + eventReset, "id: "} {
		if strings.Contains(s.raw(), name) {
			t.Fatalf("history stream has %q\n%s", name, s.raw())
		}
	}
}

func TestHistoryStreamRefusesAfterAndIgnoresLastEventID(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "a")
	for _, query := range []string{
		"view=history&conversation=a&after=0",
		"view=history&conversation=a&after=x",
		"view=history",
		"view=history&conversation=",
	} {
		status, body, hdr := doPage(t, mustGet(t, page.URL+"/events?"+query))
		if status != http.StatusBadRequest || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") || strings.TrimSpace(body) == "" {
			t.Fatalf("%q: %d %q %q", query, status, hdr.Get("Content-Type"), body)
		}
	}
	if n := proxy.count(""); n != 0 {
		t.Fatalf("bus calls %d", n)
	}

	status, body, hdr := doPage(t, mustGet(t, page.URL+"/events?view=history&conversation="+url.QueryEscape("bad name")))
	if status != http.StatusBadRequest || hdr.Get("Content-Type") == "text/event-stream" || strings.TrimSpace(body) != bus.ReasonBadConversation {
		t.Fatalf("bus-refused name: %d %q %q", status, hdr.Get("Content-Type"), body)
	}

	req := mustGet(t, page.URL+"/events?view=history&conversation=a")
	req.Header.Set("Last-Event-ID", "not a seq")
	s := openRequest(t, req)
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
}

func TestHistoryWatchWaitsOnRevAlone(t *testing.T) {
	st := stream{view: viewHistory, name: "a"}
	for _, hasRev := range []bool{false, true} {
		st.hasRev = hasRev
		req := st.watchRequest()
		if req.Cursor != nil || req.Limit != nil || req.Conversation != "a" {
			t.Fatalf("hasRev %v: %+v", hasRev, req)
		}
		if hasRev != (req.Rev != nil) {
			t.Fatalf("hasRev %v: rev %v", hasRev, req.Rev)
		}
	}
}

func TestHistoryViewOpensTheHistoryStreamAndDoesNotReload(t *testing.T) {
	d := loadPage(t, renderPage(t, historyFixture("job", "/conversations/job?before=38", 40, 39, 38)), "/conversations/job?before=41", "")
	if got := d.str("__streamURLs()"); got != "/events?conversation=job&view=history" {
		t.Fatalf("streams %q", got)
	}
	if got := d.str(`Object.keys(__openStreams()[0].listeners).sort().join(",")`); got != "conversations,meta" {
		t.Fatalf("listeners %q", got)
	}
	d.emit("conversations", conversationsEvent(t, "job", []client.Conversation{{Name: "job", Status: "open"}}))
	d.emit("meta", metaEvent(t, "job", []client.Conversation{{Name: "job", Status: "closed"}}))
	if got := d.str(`__fetches.length + " " + __seqs("transcript") + " " + location.search`); got != "0 40,39,38 ?before=41" {
		t.Fatalf("history reloaded: %q", got)
	}
	if got := d.str(`__text_of("conversation-status")`); got != "closed" {
		t.Fatalf("meta %q", got)
	}

	d.serve(true, renderPage(t, historyFixture("job", "", 37)))
	d.run("__intersect()")
	if got := d.str(`__seqs("transcript")`); got != "40,39,38,37" {
		t.Fatalf("older group %q", got)
	}

	d.run(`__setVisibility("hidden")`)
	d.run(`__setVisibility("visible")`)
	if got := d.str("__streamURLs()"); got != "/events?conversation=job&view=history" {
		t.Fatalf("visible again %q", got)
	}
}

func TestHistoryViewWithAFailedGroupDoesNotReload(t *testing.T) {
	p := historyFixture("job", "")
	p.Group = &messageGroup{Outcome: "unavailable", Reason: "down"}
	d := loadPage(t, renderPage(t, p), "/conversations/job?before=41", "")
	d.emit("conversations", conversationsEvent(t, "job", []client.Conversation{{Name: "job", Status: "open"}}))
	if got := d.str(`__fetches.length + " " + __streamURLs()`); got != "0 /events?conversation=job&view=history" {
		t.Fatalf("history reloaded: %q", got)
	}
}
