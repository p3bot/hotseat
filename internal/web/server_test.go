// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bytes"
	"context"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/store"
)

var pageClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func TestNamesFromLines(t *testing.T) {
	if got := namesFromLines(""); got == nil || len(got) != 0 {
		t.Fatalf("%#v", got)
	}
	got := namesFromLines("carol\r\n\nbob\ncarol\n  bob")
	want := []string{"carol", "bob", "carol", "  bob"}
	if len(got) != len(want) {
		t.Fatalf("%#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%#v", got)
		}
	}
}

func TestListShowsOpenAndClosed(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	status, body, hdr := getPage(t, page.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, `id="empty"`) {
		t.Fatalf("empty %d %s", status, body)
	}
	assertNoCookie(t, hdr)

	createConv(t, addr, "", "alpha")
	createConv(t, addr, "", "beta")
	closed := busCall(t, addr, "", "close", client.CloseRequest{Conversation: "beta"})
	if closed.Outcome != bus.OutcomeOK {
		t.Fatalf("close %+v", closed)
	}
	_, body, hdr = getPage(t, page.URL+"/")
	assertNoCookie(t, hdr)
	alpha := strings.Index(body, ">alpha<")
	beta := strings.Index(body, ">beta<")
	if alpha < 0 || beta < alpha {
		t.Fatalf("order alpha %d beta %d\n%s", alpha, beta, body)
	}
	if !strings.Contains(body, `class="status">open<`) || !strings.Contains(body, `class="status">closed<`) {
		t.Fatalf("status\n%s", body)
	}
	_, body, _ = getPage(t, page.URL+"/c?conversation=beta")
	if !strings.Contains(body, `id="conversation-status"`) || !strings.Contains(body, `class="status">closed<`) {
		t.Fatalf("beta\n%s", body)
	}
	if strings.Contains(logs.String(), "cursor=") {
		t.Fatalf("log stored a cursor: %s", logs.String())
	}
}

func TestReadRepeatsAndPagesWithoutStoringCursor(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	publishMsg(t, addr, "", "job", "alice", []string{"bob"}, "one", "k1")
	publishMsg(t, addr, "", "job", "alice", []string{"carol"}, "two", "k2")
	publishMsg(t, addr, "", "job", "alice", []string{}, "three", "k3")

	firstURL := page.URL + "/c?conversation=job&cursor=0&limit=2"
	_, first, hdr := getPage(t, firstURL)
	_, second, _ := getPage(t, firstURL)
	if first != second {
		t.Fatalf("repeat differed\n%s\n%s", first, second)
	}
	assertNoCookie(t, hdr)
	if !strings.Contains(first, `id="read-outcome">ok<`) {
		t.Fatalf("outcome\n%s", first)
	}
	one := strings.Index(first, ">one<")
	two := strings.Index(first, ">two<")
	three := strings.Index(first, ">three<")
	if one < 0 || two < one || three >= 0 {
		t.Fatalf("page one %d two %d three %d\n%s", one, two, three, first)
	}
	if !strings.Contains(first, `id="page-full"`) || !strings.Contains(first, `id="next-cursor">2<`) {
		t.Fatalf("next\n%s", first)
	}
	if !strings.Contains(first, `id="cursor" name="cursor" value="0"`) {
		t.Fatalf("form cursor\n%s", first)
	}
	href := attr(t, first, `id="next-page" href="`)
	if !strings.Contains(href, "cursor=2") || !strings.Contains(href, "limit=2") {
		t.Fatalf("href %s", href)
	}
	_, next, _ := getPage(t, page.URL+href)
	if strings.Contains(next, ">one<") || strings.Contains(next, ">two<") || !strings.Contains(next, ">three<") {
		t.Fatalf("next page\n%s", next)
	}
	if strings.Contains(next, `id="page-full"`) {
		t.Fatalf("short page marked full\n%s", next)
	}
	_, again, _ := getPage(t, firstURL)
	if again != first {
		t.Fatal("cursor advanced on the server")
	}

	_, bad, _ := getPage(t, page.URL+"/c?conversation=job&cursor=abc&limit=2")
	if !strings.Contains(bad, `id="notice">cursor and limit must be integers<`) || strings.Contains(bad, `id="transcript"`) {
		t.Fatalf("bad cursor\n%s", bad)
	}
	if !strings.Contains(bad, `value="abc"`) {
		t.Fatalf("replaced cursor\n%s", bad)
	}
	_, onlyCursor, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0")
	if !strings.Contains(onlyCursor, "cursor and limit are both required") || strings.Contains(onlyCursor, `id="transcript"`) {
		t.Fatalf("missing limit\n%s", onlyCursor)
	}
	if strings.Contains(logs.String(), "cursor=") || strings.Contains(logs.String(), "k1") || strings.Contains(logs.String(), "idempotency") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestPublishUsesSuppliedKeyAndClosedStaysReadable(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "carol\n\nbob")
	form.Set("body", "<b>&")
	form.Set("idempotency_key", "idem-9f3a")

	_, stored, hdr := postPage(t, page.URL+"/publish", form)
	assertNoCookie(t, hdr)
	if !strings.Contains(stored, `id="stored">Stored.<`) || strings.Contains(stored, `id="already-stored"`) {
		t.Fatalf("stored\n%s", stored)
	}
	if !strings.Contains(stored, "&lt;b&gt;&amp;") || strings.Contains(stored, "<b>&") {
		t.Fatalf("escape\n%s", stored)
	}
	carol := strings.Index(stored, `class="addressee">carol<`)
	bob := strings.Index(stored, `class="addressee">bob<`)
	if carol < 0 || bob < carol {
		t.Fatalf("to order\n%s", stored)
	}
	if !strings.Contains(stored, `class="from">alice<`) || !strings.Contains(stored, `class="key">idem-9f3a<`) {
		t.Fatalf("identity\n%s", stored)
	}
	if !strings.Contains(stored, `value="idem-9f3a"`) {
		t.Fatalf("key not held on the page\n%s", stored)
	}

	_, again, _ := postPage(t, page.URL+"/publish", form)
	if !strings.Contains(again, `id="already-stored">Already stored.<`) || strings.Contains(again, `id="stored"`) {
		t.Fatalf("again\n%s", again)
	}
	if !strings.Contains(again, "seq 1 from") {
		t.Fatalf("original seq\n%s", again)
	}

	changed := url.Values{}
	changed.Set("conversation", "job")
	changed.Set("from", "alice")
	changed.Set("to", "carol\n\nbob")
	changed.Set("body", "other")
	changed.Set("idempotency_key", "idem-9f3a")
	_, conflict, _ := postPage(t, page.URL+"/publish", changed)
	if !strings.Contains(conflict, `id="publish-outcome">refused<`) || !strings.Contains(conflict, bus.ReasonKeyConflict) {
		t.Fatalf("conflict\n%s", conflict)
	}

	emptyKey := url.Values{}
	emptyKey.Set("conversation", "job")
	emptyKey.Set("from", "alice")
	emptyKey.Set("to", "bob")
	emptyKey.Set("body", "nope")
	emptyKey.Set("idempotency_key", "")
	_, missing, _ := postPage(t, page.URL+"/publish", emptyKey)
	if !strings.Contains(missing, bus.ReasonKeyRequired) {
		t.Fatalf("empty key\n%s", missing)
	}

	_, transcript, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	if strings.Count(transcript, "&lt;b&gt;&amp;") != 1 || strings.Contains(transcript, ">other<") || strings.Contains(transcript, ">nope<") {
		t.Fatalf("transcript\n%s", transcript)
	}

	shut := busCall(t, addr, "", "close", client.CloseRequest{Conversation: "job"})
	if shut.Outcome != bus.OutcomeOK {
		t.Fatalf("close %+v", shut)
	}
	fresh := url.Values{}
	fresh.Set("conversation", "job")
	fresh.Set("from", "alice")
	fresh.Set("to", "bob")
	fresh.Set("body", "later")
	fresh.Set("idempotency_key", "idem-new")
	_, refused, _ := postPage(t, page.URL+"/publish", fresh)
	if !strings.Contains(refused, `id="publish-outcome">refused<`) || !strings.Contains(refused, bus.ReasonClosed) {
		t.Fatalf("closed publish\n%s", refused)
	}
	if !strings.Contains(refused, `id="read-form"`) || !strings.Contains(refused, `class="status">closed<`) {
		t.Fatalf("not readable\n%s", refused)
	}
	_, still, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	if strings.Count(still, "&lt;b&gt;&amp;") != 1 || strings.Contains(still, ">later<") {
		t.Fatalf("changed\n%s", still)
	}
	if !strings.Contains(still, `id="read-outcome">ok<`) || !strings.Contains(still, `class="status">closed<`) {
		t.Fatalf("read closed\n%s", still)
	}
	_, replay, _ := postPage(t, page.URL+"/publish", form)
	if !strings.Contains(replay, `id="already-stored">Already stored.<`) {
		t.Fatalf("replay\n%s", replay)
	}
	_, final, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	if strings.Count(final, "&lt;b&gt;&amp;") != 1 {
		t.Fatalf("replay wrote\n%s", final)
	}
	if strings.Contains(logs.String(), "idem-9f3a") || strings.Contains(logs.String(), "idem-new") || strings.Contains(logs.String(), "cursor=") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestPublishEchoesTheRequestCursor(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	publishMsg(t, addr, "", "job", "alice", []string{"bob"}, "one", "k1")
	publishMsg(t, addr, "", "job", "alice", []string{"carol"}, "two", "k2")

	_, blank, _ := getPage(t, page.URL+"/c?conversation=job")
	if strings.Contains(blank, `value="0"`) || strings.Contains(blank, `value="50"`) {
		t.Fatalf("server supplied a cursor\n%s", blank)
	}
	if !strings.Contains(blank, `placeholder="0"`) || !strings.Contains(blank, `placeholder="50"`) {
		t.Fatalf("placeholders\n%s", blank)
	}
	if strings.Contains(blank, `id="transcript"`) {
		t.Fatalf("read with no cursor\n%s", blank)
	}

	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("cursor", "1")
	form.Set("limit", "1")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "via-page")
	form.Set("idempotency_key", "page-key")
	_, published, _ := postPage(t, page.URL+"/publish", form)
	if !strings.Contains(published, `id="stored"`) || !strings.Contains(published, `id="cursor" name="cursor" value="1"`) {
		t.Fatalf("publish\n%s", published)
	}
	if strings.Contains(published, `value="0"`) || strings.Contains(published, `value="50"`) {
		t.Fatalf("replaced cursor\n%s", published)
	}
	if !strings.Contains(published, `id="transcript"`) || !strings.Contains(published, ">two<") || strings.Contains(published, ">one<") {
		t.Fatalf("transcript\n%s", published)
	}

	read := url.Values{}
	read.Set("conversation", "job")
	read.Set("cursor", "1")
	read.Set("limit", "1")
	read.Set("idempotency_key", "page-key")
	status, body, _ := postPage(t, page.URL+"/c", read)
	if status != http.StatusOK || !strings.Contains(body, `value="page-key"`) || !strings.Contains(body, `id="cursor" name="cursor" value="1"`) {
		t.Fatalf("read post %d %s", status, body)
	}
	if strings.Contains(body, "page-key=") || !strings.Contains(body, `action="/c"`) {
		t.Fatalf("key in a url\n%s", body)
	}
	if strings.Contains(logs.String(), "page-key") || strings.Contains(logs.String(), "cursor=") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestPublishRejectsForeignOrigin(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "pwned-cross-site")
	form.Set("idempotency_key", "idem-foreign")
	foreign := []http.Header{
		{"Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"cross-site"}},
		{"Origin": {"null"}},
		{"Origin": {"http://127.0.0.1:9"}, "Sec-Fetch-Site": {"same-site"}},
		{"Origin": {page.URL}, "Sec-Fetch-Site": {"cross-site"}},
	}
	for _, h := range foreign {
		status, body, _ := postHeaders(t, page.URL+"/publish", form, h)
		if status != http.StatusForbidden || !strings.Contains(body, `id="notice">this publish did not come from this page<`) {
			t.Fatalf("headers %v → %d %s", h, status, body)
		}
		if strings.Contains(body, "pwned-cross-site") || strings.Contains(body, "idem-foreign") || strings.Contains(body, `id="read-form"`) {
			t.Fatalf("echoed the foreign post\n%s", body)
		}
	}
	got := busCall(t, addr, "", "read", client.ReadRequest{Conversation: "job", Cursor: 0, Limit: 10})
	if got.Outcome != bus.OutcomeOK || got.Messages == nil || len(*got.Messages) != 0 {
		t.Fatalf("transcript %+v", got.Messages)
	}

	own := http.Header{"Origin": {page.URL}, "Sec-Fetch-Site": {"same-origin"}}
	status, body, hdr := postHeaders(t, page.URL+"/publish", form, own)
	if hdr.Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("referrer policy %q", hdr.Get("Referrer-Policy"))
	}
	if status != http.StatusOK || !strings.Contains(body, `id="stored"`) || !strings.Contains(body, ">pwned-cross-site<") {
		t.Fatalf("same origin %d %s", status, body)
	}
	if strings.Contains(logs.String(), "idem-foreign") {
		t.Fatalf("log %s", logs.String())
	}
	if !strings.Contains(logs.String(), "cross-site request") {
		t.Fatalf("missing refusal log %s", logs.String())
	}
}

func TestPublishShowsParseFormError(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")

	postRaw := func(body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, page.URL+"/publish", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := pageClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(got)
	}

	status, got := postRaw("conversation=job&idempotency_key=idem-bad-escape&body=%")
	const escape = `invalid URL escape "%"`
	if status != http.StatusOK || !strings.Contains(got, `id="notice">`+html.EscapeString(escape)+`<`) {
		t.Fatalf("escape %d %s", status, got)
	}
	if strings.Contains(got, "idem-bad-escape") || strings.Contains(got, `id="read-form"`) || strings.Contains(got, "the form could not be read") {
		t.Fatalf("echoed the bad form\n%s", got)
	}

	const marker = "idem-too-large"
	var big strings.Builder
	big.Grow(len(marker) + (10 << 20) + 1)
	big.WriteString(marker)
	big.WriteString(strings.Repeat("a", (10<<20)+1))
	status, got = postRaw(big.String())
	const tooLarge = "http: POST too large"
	if status != http.StatusOK || !strings.Contains(got, `id="notice">`+tooLarge+`<`) {
		snippet := got
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		t.Fatalf("size %d %s", status, snippet)
	}
	if strings.Contains(got, marker) || strings.Contains(got, `id="read-form"`) {
		t.Fatal("echoed the large form")
	}

	text := logs.String()
	if !strings.Contains(text, "invalid URL escape") || !strings.Contains(text, tooLarge) {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, "idem-bad-escape") || strings.Contains(text, marker) || strings.Contains(text, "cursor=") {
		t.Fatalf("log stored the form %s", text)
	}

	read := busCall(t, addr, "", "read", client.ReadRequest{Conversation: "job", Cursor: 0, Limit: 10})
	if read.Outcome != bus.OutcomeOK || read.Messages == nil || len(*read.Messages) != 0 {
		t.Fatalf("transcript %+v", read.Messages)
	}
}

func TestPageRejectsADifferentHost(t *testing.T) {
	addr := newBus(t, "", false)
	page, logs := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "pwned-rebind")
	form.Set("idempotency_key", "idem-rebind")

	post := func(host, origin, site string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, page.URL+"/publish", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		resp, err := pageClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}

	status, body := post("evil.example:4728", "http://evil.example:4728", "same-origin")
	if status != http.StatusForbidden || !strings.Contains(body, `id="notice">this page does not serve that host<`) {
		t.Fatalf("rebind %d %s", status, body)
	}
	if strings.Contains(body, "pwned-rebind") || strings.Contains(body, "idem-rebind") || strings.Contains(body, ">job<") {
		t.Fatalf("echoed the rebound post\n%s", body)
	}
	status, body = post("evil.example:4728", "", "")
	if status != http.StatusForbidden || strings.Contains(body, "pwned-rebind") {
		t.Fatalf("curl host %d %s", status, body)
	}

	greq, err := http.NewRequest(http.MethodGet, page.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	greq.Host = "evil.example:4728"
	gresp, err := pageClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gresp.Body.Close() }()
	gbody, err := io.ReadAll(gresp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if gresp.StatusCode != http.StatusForbidden || strings.Contains(string(gbody), ">job<") {
		t.Fatalf("list %d %s", gresp.StatusCode, gbody)
	}

	got := busCall(t, addr, "", "read", client.ReadRequest{Conversation: "job", Cursor: 0, Limit: 10})
	if got.Outcome != bus.OutcomeOK || got.Messages == nil || len(*got.Messages) != 0 {
		t.Fatalf("transcript %+v", got.Messages)
	}
	text := logs.String()
	if strings.Contains(text, "evil.example") || strings.Contains(text, "pwned-rebind") || strings.Contains(text, "idem-rebind") {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, "op=list") || strings.Contains(text, "op=publish") || !strings.Contains(text, "wrong host") {
		t.Fatalf("log %s", text)
	}

	u, err := url.Parse(page.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, body = post("localhost:"+u.Port(), "http://localhost:"+u.Port(), "same-origin")
	if status != http.StatusOK || !strings.Contains(body, `id="stored"`) || !strings.Contains(body, ">pwned-rebind<") {
		t.Fatalf("localhost %d %s", status, body)
	}
	status, body = post("localhost:9", "http://localhost:9", "same-origin")
	if status != http.StatusForbidden || strings.Contains(body, `id="stored"`) {
		t.Fatalf("localhost port %d %s", status, body)
	}
}

func TestWrongHost(t *testing.T) {
	cases := []struct {
		name   string
		listen string
		host   string
		want   bool
	}{
		{name: "bound", listen: "127.0.0.1:4728", host: "127.0.0.1:4728", want: false},
		{name: "localhost alias", listen: "127.0.0.1:4728", host: "localhost:4728", want: false},
		{name: "localhost case", listen: "127.0.0.1:4728", host: "LocalHost:4728", want: false},
		{name: "localhost other port", listen: "127.0.0.1:4728", host: "localhost:9", want: true},
		{name: "rebind name", listen: "127.0.0.1:4728", host: "evil.example:4728", want: true},
		{name: "ipv6 alias", listen: "[::1]:4728", host: "localhost:4728", want: false},
		{name: "ipv6 omits port", listen: "[::1]:80", host: "[::1]", want: false},
		{name: "ipv6 states port", listen: "[::1]:80", host: "[::1]:80", want: false},
		{name: "ipv6 omitted port is 80", listen: "[::1]:4728", host: "[::1]", want: true},
		{name: "ipv6 other spelling", listen: "[::1]:80", host: "[0:0:0:0:0:0:0:1]", want: true},
		{name: "remote no alias", listen: "192.0.2.10:4728", host: "localhost:4728", want: true},
		{name: "remote itself", listen: "192.0.2.10:4728", host: "192.0.2.10:4728", want: false},
		{name: "unspecified", listen: "0.0.0.0:4728", host: "127.0.0.1:4728", want: true},
		{name: "empty listen", listen: "", host: "127.0.0.1:4728", want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/", nil)
			req.Host = tt.host
			if got := (Server{ListenHost: tt.listen}).wrongHost(req); got != tt.want {
				t.Fatalf("wrongHost = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestForeignPublish(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		site   string
		host   string
		want   bool
	}{
		{name: "no headers", host: "127.0.0.1:4728", want: false},
		{name: "cross-site", origin: "https://evil.example", site: "cross-site", host: "127.0.0.1:4728", want: true},
		{name: "null origin", origin: "null", host: "127.0.0.1:4728", want: true},
		{name: "other port", origin: "http://127.0.0.1:9", site: "same-site", host: "127.0.0.1:4728", want: true},
		{name: "same origin", origin: "http://127.0.0.1:4728", site: "same-origin", host: "127.0.0.1:4728", want: false},
		{name: "default port", origin: "http://127.0.0.1", host: "127.0.0.1", want: false},
		{name: "ipv6 default port", origin: "http://[::1]", host: "[::1]", want: false},
		{name: "ipv6 origin omits port", origin: "http://[::1]", host: "[::1]:80", want: false},
		{name: "cross-site wins", origin: "http://127.0.0.1:4728", site: "cross-site", host: "127.0.0.1:4728", want: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://"+tt.host+"/publish", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.site != "" {
				req.Header.Set("Sec-Fetch-Site", tt.site)
			}
			if got := foreignPublish(req); got != tt.want {
				t.Fatalf("foreign = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnfilteredReadHasNoRoster(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	publishMsg(t, addr, "", "job", "alice", []string{"bob"}, "for-bob", "a")
	publishMsg(t, addr, "", "job", "alice", []string{}, "for-none", "b")
	publishMsg(t, addr, "", "job", "carol", []string{"alice"}, "for-carol", "c")
	_, body, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	bob := strings.Index(body, ">for-bob<")
	none := strings.Index(body, ">for-none<")
	carol := strings.Index(body, ">for-carol<")
	if bob < 0 || none < bob || carol < none {
		t.Fatalf("order bob %d none %d carol %d\n%s", bob, none, carol, body)
	}
	if strings.Contains(body, `name="name"`) {
		t.Fatal("name filter")
	}
	lower := strings.ToLower(body)
	for _, word := range []string{"member", "presence", "roster", ">create<", ">wait<", ">close<", "/v1/create", "/v1/wait", "/v1/close"} {
		if strings.Contains(lower, word) {
			t.Fatalf("found %s\n%s", word, body)
		}
	}
	for _, path := range []string{"/v1/create", "/v1/wait", "/v1/close", "/create", "/wait", "/close"} {
		status, _, _ := getPage(t, page.URL+path)
		if status == http.StatusOK {
			t.Fatalf("%s served", path)
		}
	}
}

func TestLoopbackWorksWithoutToken(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	_, body, _ := getPage(t, page.URL+"/")
	if !strings.Contains(body, ">job<") || !strings.Contains(body, `class="status">open<`) {
		t.Fatalf("list\n%s", body)
	}
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "hello")
	form.Set("idempotency_key", "k")
	_, stored, _ := postPage(t, page.URL+"/publish", form)
	if !strings.Contains(stored, `id="stored"`) {
		t.Fatalf("publish\n%s", stored)
	}
	_, read, _ := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	if !strings.Contains(read, ">hello<") || !strings.Contains(read, `class="from">alice<`) {
		t.Fatalf("read\n%s", read)
	}

	const token = "hstk-loopback-ignored"
	page2, logs := newPage(t, addr, token)
	_, body, hdr := getPage(t, page2.URL+"/")
	if !strings.Contains(body, ">job<") {
		t.Fatalf("with token\n%s", body)
	}
	assertOmits(t, "page", body, token)
	assertOmits(t, "headers", headerText(hdr), token)
	assertOmits(t, "logs", logs.String(), token)
}

func TestTokenStaysOffThePage(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	const wrong = "hstk-ffffffffffffffff"
	addr := newBus(t, token, true)

	bare, bareLogs := newPage(t, addr, "")
	_, body, hdr := getPage(t, bare.URL+"/")
	if !strings.Contains(body, bus.ReasonTokenRequired) {
		t.Fatalf("bare\n%s", body)
	}
	assertNoCookie(t, hdr)
	assertOmits(t, "bare page", body, token, wrong)
	assertOmits(t, "bare headers", headerText(hdr), token, wrong)
	assertOmits(t, "bare logs", bareLogs.String(), token, wrong)

	bad, badLogs := newPage(t, addr, wrong)
	_, refused, _ := getPage(t, bad.URL+"/")
	if !strings.Contains(refused, bus.ReasonTokenRejected) {
		t.Fatalf("wrong\n%s", refused)
	}
	assertOmits(t, "wrong page", refused, token, wrong)
	assertOmits(t, "wrong logs", badLogs.String(), token, wrong)

	page, logs := newPage(t, addr, token)
	createConv(t, addr, token, "job")
	_, body, hdr = getPage(t, page.URL+"/")
	if !strings.Contains(body, ">job<") || !strings.Contains(body, `class="status">open<`) {
		t.Fatalf("list\n%s", body)
	}
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "hello")
	form.Set("idempotency_key", "idem-token")
	_, stored, shdr := postPage(t, page.URL+"/publish", form)
	if !strings.Contains(stored, `id="stored"`) || !strings.Contains(stored, ">hello<") || !strings.Contains(stored, `class="from">alice<`) {
		t.Fatalf("publish\n%s", stored)
	}
	_, read, rhdr := getPage(t, page.URL+"/c?conversation=job&cursor=0&limit=10")
	if !strings.Contains(read, ">hello<") || !strings.Contains(read, `class="key">idem-token<`) {
		t.Fatalf("read\n%s", read)
	}
	for _, part := range []struct{ name, text string }{
		{"list", body},
		{"publish", stored},
		{"read", read},
		{"list headers", headerText(hdr)},
		{"publish headers", headerText(shdr)},
		{"read headers", headerText(rhdr)},
		{"logs", logs.String()},
	} {
		assertOmits(t, part.name, part.text, token, wrong)
	}
	if strings.Contains(logs.String(), "idem-token") || strings.Contains(logs.String(), "cursor=") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestProtocolIsListReadPublish(t *testing.T) {
	const token = "hstk-protocol-token"
	var mu sync.Mutex
	var seen []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		mu.Unlock()
		var payload string
		switch r.URL.Path {
		case bus.PathList:
			payload = `{"outcome":"ok","conversations":[{"name":"job","status":"open"}]}`
		case bus.PathRead:
			payload = `{"outcome":"ok","messages":[{"seq":4,"time":"t","from":"alice","to":["bob"],"body":"hello","idempotency_key":"k"}]}`
		case bus.PathPublish:
			payload = `{"outcome":"ok","already_stored":false,"message":{"seq":5,"time":"t","from":"alice","to":["carol","bob"],"body":"hello","idempotency_key":"k1"}}`
		default:
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		if _, err := io.WriteString(w, payload); err != nil {
			return
		}
	}))
	t.Cleanup(fake.Close)

	var logs bytes.Buffer
	page := startPage(t, Server{
		BusAddress: fake.Listener.Addr().String(),
		Token:      token,
		Log:        slog.New(slog.NewTextHandler(&logs, nil)),
	})
	auth := "Bearer " + token
	take := func() []string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), seen...)
		seen = nil
		return out
	}

	_, body, _ := getPage(t, page.URL+"/")
	got := take()
	if len(got) != 1 || got[0] != bus.PathList+" "+auth+" {}" {
		t.Fatalf("list %#v", got)
	}
	assertOmits(t, "list page", body, token)

	_, body, _ = getPage(t, page.URL+"/c?conversation=job&cursor=4&limit=2")
	got = take()
	if len(got) != 2 ||
		got[0] != bus.PathList+" "+auth+" {}" ||
		got[1] != bus.PathRead+" "+auth+` {"conversation":"job","cursor":4,"limit":2}` {
		t.Fatalf("read %#v", got)
	}
	if strings.Contains(got[1], `"name"`) {
		t.Fatalf("name filter %s", got[1])
	}
	assertOmits(t, "read page", body, token)

	_, _, _ = getPage(t, page.URL+"/c?conversation=job&cursor=abc&limit=2")
	got = take()
	if len(got) != 1 || !strings.HasPrefix(got[0], bus.PathList+" ") {
		t.Fatalf("bad cursor called %#v", got)
	}
	_, _, _ = getPage(t, page.URL+"/c?conversation=job&cursor=4")
	got = take()
	if len(got) != 1 || !strings.HasPrefix(got[0], bus.PathList+" ") {
		t.Fatalf("missing limit called %#v", got)
	}
	_, _, _ = getPage(t, page.URL+"/c")
	if got = take(); len(got) != 0 {
		t.Fatalf("missing conversation called %#v", got)
	}

	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "carol\nbob")
	form.Set("body", "hello")
	form.Set("idempotency_key", "k1")
	_, body, _ = postPage(t, page.URL+"/publish", form)
	got = take()
	wantPub := bus.PathPublish + " " + auth + ` {"conversation":"job","from":"alice","to":["carol","bob"],"body":"hello","idempotency_key":"k1"}`
	if len(got) != 2 || got[0] != bus.PathList+" "+auth+" {}" || got[1] != wantPub {
		t.Fatalf("publish %#v", got)
	}
	assertOmits(t, "publish page", body, token)
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "k1") || strings.Contains(logs.String(), "cursor=") {
		t.Fatalf("logs %s", logs.String())
	}
}

func TestServeRefusesNonLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr %T", ln.Addr())
	}
	dial := net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
	err = Serve(context.Background(), ln, "127.0.0.1:1", "hstk-page-secret", slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "page listen address must be loopback") {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "hstk-page-secret") {
		t.Fatalf("err includes the token: %v", err)
	}
	conn, dialErr := net.DialTimeout("tcp", dial, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("still listening")
	}
}

func TestBusDownIsAnOutcome(t *testing.T) {
	page, _ := newPage(t, "127.0.0.1:1", "")
	status, body, _ := getPage(t, page.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, client.OutcomeConnectionFailure) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestHandlerWritesNoStore(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	getPage(t, page.URL+"/")
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func newBus(t *testing.T, token string, remote bool) string {
	t.Helper()
	network := "tcp"
	bind := "127.0.0.1:0"
	if remote {
		network = "tcp4"
		bind = "0.0.0.0:0"
	}
	ln, err := net.Listen(network, bind)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.Serve(ctx, ln, st, bus.Options{
			Token:  token,
			Logger: slog.New(slog.DiscardHandler),
		})
	}()
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr %T", ln.Addr())
	}
	dial := net.JoinHostPort("127.0.0.1", strconv.Itoa(tcp.Port))
	waitListen(t, dial, errCh)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("bus: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("bus did not stop")
		}
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return dial
}

func newPage(t *testing.T, busAddr, token string) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	srv := startPage(t, Server{
		BusAddress: busAddr,
		Token:      token,
		Log:        slog.New(slog.NewTextHandler(logs, nil)),
	})
	return srv, logs
}

// startPage binds a loopback page and records that address as the only host it serves.
func startPage(t *testing.T, s Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NewServeMux())
	s.ListenHost = srv.Listener.Addr().String()
	srv.Config.Handler = s.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func waitListen(t *testing.T, addr string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if errCh != nil {
			select {
			case err := <-errCh:
				t.Fatalf("stopped: %v", err)
			default:
			}
		}
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not listen on %s: %v", addr, last)
}

func getPage(t *testing.T, rawURL string) (int, string, http.Header) {
	t.Helper()
	resp, err := pageClient.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header.Clone()
}

func postPage(t *testing.T, rawURL string, form url.Values) (int, string, http.Header) {
	t.Helper()
	return postHeaders(t, rawURL, form, nil)
}

func postHeaders(t *testing.T, rawURL string, form url.Values, header http.Header) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, vals := range header {
		req.Header[k] = append([]string(nil), vals...)
	}
	resp, err := pageClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body), resp.Header.Clone()
}

func busCall(t *testing.T, addr, token, op string, body any) client.Result {
	t.Helper()
	res, err := client.Do(context.Background(), addr, op, token, body)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func createConv(t *testing.T, addr, token, name string) {
	t.Helper()
	res := busCall(t, addr, token, "create", client.CreateRequest{Name: name})
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("create %s: %+v", name, res)
	}
}

func publishMsg(t *testing.T, addr, token, conv, from string, to []string, body, key string) {
	t.Helper()
	res := busCall(t, addr, token, "publish", client.PublishRequest{
		Conversation:   conv,
		From:           from,
		To:             to,
		Body:           body,
		IdempotencyKey: key,
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("publish %s: %+v", key, res)
	}
}

func attr(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("missing %s", marker)
	}
	rest := body[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatalf("unclosed %s", marker)
	}
	return html.UnescapeString(rest[:end])
}

func assertNoCookie(t *testing.T, h http.Header) {
	t.Helper()
	if len(h.Values("Set-Cookie")) != 0 {
		t.Fatalf("cookie %v", h.Values("Set-Cookie"))
	}
}

func assertOmits(t *testing.T, label, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s contains %q", label, secret)
		}
	}
}

func headerText(h http.Header) string {
	var b strings.Builder
	for k, vals := range h {
		for _, v := range vals {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
