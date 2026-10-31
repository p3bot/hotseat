// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bufio"
	"context"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func TestEventsCatchesUpThenListsThenStaysLive(t *testing.T) {
	const token = "hstk-events-token"
	addr := newBus(t, token, false)
	page, logs := newPage(t, addr, token)
	createConv(t, addr, token, "job")
	first := publishSeq(t, addr, token, "job", "m1")
	second := publishSeq(t, addr, token, "job", "m2")
	third := publishSeq(t, addr, token, "job", "m3")

	s := openEvents(t, page.URL+"/events?after="+strconv.FormatInt(first, 10), nil)
	if ev := s.nextRecord(t); ev.Retry != strconv.Itoa(streamRetry) || ev.Name != "" {
		t.Fatalf("stream does not start with retry: %+v", ev)
	}
	expectMessage(t, s.next(t), second, "job")
	expectMessage(t, s.next(t), third, "job")
	expectConversations(t, s.next(t))
	fourth := publishSeq(t, addr, token, "job", "m4")
	expectMessage(t, s.next(t), fourth, "job")

	s.close()
	assertOmits(t, "stream", s.raw(), token)
	assertOmits(t, "logs", logs.String(), token)
}

func TestEventsHeadersAreEventStreamNoStore(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	s := openEvents(t, page.URL+"/events?view=list", nil)
	if got := s.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control %q", got)
	}
	if got := s.header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("x-content-type-options %q", got)
	}
	expectConversations(t, s.next(t))
}

func TestTranscriptStreamSendsOnlyItsConversationAndMetaOnClose(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "a")
	createConv(t, addr, "", "b")
	own := publishSeq(t, addr, "", "a", "a1")
	publishSeq(t, addr, "", "b", "b1")

	s := openEvents(t, page.URL+"/events?conversation=a", nil)
	expectMessage(t, s.next(t), own, "a")
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")

	publishSeq(t, addr, "", "b", "b2")
	live := publishSeq(t, addr, "", "a", "a2")
	ev := s.nextExcept(t, eventConversations)
	expectMessage(t, ev, live, "a")

	if res := busCall(t, addr, "", "close", client.CloseRequest{Conversation: "a"}); res.Outcome != bus.OutcomeOK {
		t.Fatalf("close %+v", res)
	}
	ev = s.nextExcept(t, eventConversations)
	if ev.Name != eventMeta || !strings.HasPrefix(ev.Data, `<aside id="conversation-meta">`) || !strings.Contains(ev.Data, `data-status="closed"`) {
		t.Fatalf("meta %+v", ev)
	}
}

func TestListStreamSendsNoMessages(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "a")
	publishSeq(t, addr, "", "a", "a1")

	s := openEvents(t, page.URL+"/events?view=list&after=999", nil)
	expectConversations(t, s.next(t))
	publishSeq(t, addr, "", "a", "a2")
	createConv(t, addr, "", "c")
	ev := s.next(t)
	expectConversations(t, ev)
	if !strings.Contains(ev.Data, `data-name="c"`) {
		t.Fatalf("list change missing c: %s", ev.Data)
	}
}

func TestTranscriptCatchesUpAmongManyOtherMessagesWithoutReset(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "a")
	createConv(t, addr, "", "b")
	var own []int64
	for i := range 60 {
		publishSeq(t, addr, "", "b", "b"+strconv.Itoa(i))
		if i%20 == 0 {
			own = append(own, publishSeq(t, addr, "", "a", "a"+strconv.Itoa(i)))
		}
	}

	s := openEvents(t, page.URL+"/events?conversation=a&after=0", nil)
	for _, seq := range own {
		expectMessage(t, s.next(t), seq, "a")
	}
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
}

func TestViewMissingMoreThanAGroupGetsResetAndEnds(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "a")
	createConv(t, addr, "", "b")
	publishSeq(t, addr, "", "b", "other")
	var own []int64
	for i := range groupLimit + 1 {
		own = append(own, publishSeq(t, addr, "", "a", "a"+strconv.FormatInt(i, 10)))
	}

	for _, rawURL := range []string{page.URL + "/events", page.URL + "/events?conversation=a"} {
		s := openEvents(t, rawURL, nil)
		ev := s.next(t)
		if ev.Name != eventReset || ev.ID != "" {
			t.Fatalf("%s: want reset, got %+v", rawURL, ev)
		}
		s.expectEnd(t)
	}

	s := openEvents(t, page.URL+"/events?conversation=a&after="+strconv.FormatInt(own[0], 10), nil)
	for _, seq := range own[1:] {
		expectMessage(t, s.next(t), seq, "a")
	}
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
}

func TestStreamEndsWithEventStreamWhenTheBusIsAway(t *testing.T) {
	page, _ := newPage(t, "127.0.0.1:1", "")
	s := openEvents(t, page.URL+"/events", nil)
	s.expectEnd(t)
	if !strings.HasPrefix(s.raw(), "retry: ") {
		t.Fatalf("raw %q", s.raw())
	}
}

func TestFirstWatchUnavailableEndsWithEventStream(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	proxy.set(func(p *busProxy) { p.failWatch = true })
	page, _ := newPage(t, proxy.addr(), "")
	s := openEvents(t, page.URL+"/events", nil)
	if ev := s.nextRecord(t); ev.Retry != strconv.Itoa(streamRetry) {
		t.Fatalf("want retry, got %+v", ev)
	}
	s.expectEnd(t)
	if n := proxy.count(bus.PathList); n != 0 {
		t.Fatalf("list calls %d", n)
	}
}

func TestStreamThePageCannotCallIs500(t *testing.T) {
	const token = "bad token"
	page, logs := newPage(t, "127.0.0.1:1", token)
	status, body, hdr := doPage(t, mustGet(t, page.URL+"/events"))
	if status != http.StatusInternalServerError || hdr.Get("Content-Type") == "text/event-stream" {
		t.Fatalf("%d %q %q", status, hdr.Get("Content-Type"), body)
	}
	assertOmits(t, "body", body, token)
	assertOmits(t, "logs", logs.String(), token)
}

func TestLiveBurstWiderThanAGroupArrivesWithoutReset(t *testing.T) {
	prev := groupLimit
	groupLimit = 2
	t.Cleanup(func() { groupLimit = prev })
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "job")

	s := openEvents(t, page.URL+"/events", nil)
	expectConversations(t, s.next(t))
	proxy.waitCount(t, bus.PathWatch, 2)
	release := make(chan struct{})
	proxy.set(func(p *busProxy) { p.hold = release })
	seqs := []int64{publishSeq(t, addr, "", "job", "m0")}
	expectMessage(t, s.next(t), seqs[0], "job")
	proxy.waitCount(t, bus.PathWatch, 3)
	for i := 1; i <= 5; i++ {
		seqs = append(seqs, publishSeq(t, addr, "", "job", "m"+strconv.Itoa(i)))
	}
	proxy.set(func(p *busProxy) { p.hold = nil })
	close(release)
	for _, seq := range seqs[1:] {
		expectMessage(t, s.next(t), seq, "job")
	}
	if n := proxy.count(bus.PathWatch); n < 5 {
		t.Fatalf("watch calls %d, want the burst read in groups of %d", n, groupLimit)
	}
}

func TestStreamLogsFailedWatchAndDisconnectWithoutBodies(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	logs := &lockedBuffer{}
	page := startPage(t, Server{
		BusAddress: proxy.addr(),
		Log:        slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	createConv(t, addr, "", "job")

	s := openEvents(t, page.URL+"/events?conversation=job", nil)
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
	proxy.waitCount(t, bus.PathWatch, 2)
	proxy.set(func(p *busProxy) { p.failWatch = true })
	seq := publishSeq(t, addr, "", "job", "secret-txid")
	expectMessage(t, s.next(t), seq, "job")
	s.expectEnd(t)
	logs.waitFor(t, `level=WARN msg="stream ended" op=events view=transcript conversation=job call=watch outcome=unavailable`)
	if !strings.Contains(logs.String(), `level=DEBUG msg="stream open" op=events view=transcript conversation=job after=0`) {
		t.Fatalf("no open line\n%s", logs.String())
	}

	proxy.set(func(p *busProxy) { p.failWatch = false })
	list := openEvents(t, page.URL+"/events?view=list", nil)
	expectConversations(t, list.next(t))
	list.close()
	logs.waitFor(t, `level=DEBUG msg="stream closed" op=events view=list cause="request ended"`)
	assertOmits(t, "logs", logs.String(), "body secret-txid", "line two", "secret-txid")

	refusals := []string{
		page.URL + "/events?after=x",
		page.URL + "/events?conversation=" + url.QueryEscape("bad name"),
	}
	for _, rawURL := range refusals {
		if status, body, _ := doPage(t, mustGet(t, rawURL)); status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", rawURL, status, body)
		}
	}
	logs.waitFor(t, `msg=call op=events outcome=refused reason="after must be an integer of at least 0"`)
	logs.waitFor(t, `msg=call op=events view=transcript conversation="bad name" outcome=refused reason="`+bus.ReasonBadConversation+`"`)
}

func TestStreamEndsWhenAWatchFails(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "job")

	s := openEvents(t, page.URL+"/events", nil)
	expectConversations(t, s.next(t))
	proxy.waitCount(t, bus.PathWatch, 2)
	proxy.set(func(p *busProxy) { p.failWatch = true })
	seq := publishSeq(t, addr, "", "job", "m1")
	expectMessage(t, s.next(t), seq, "job")
	s.expectEnd(t)
}

func TestFailedListEndsStreamAndReconnectSendsTheChange(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "job")
	seq := publishSeq(t, addr, "", "job", "m1")

	s := openEvents(t, page.URL+"/events", nil)
	expectMessage(t, s.next(t), seq, "job")
	expectConversations(t, s.next(t))
	proxy.set(func(p *busProxy) { p.failList = true })
	createConv(t, addr, "", "later")
	s.expectEnd(t)

	proxy.set(func(p *busProxy) { p.failList = false })
	s = openEvents(t, page.URL+"/events", http.Header{"Last-Event-ID": {strconv.FormatInt(seq, 10)}})
	ev := s.next(t)
	expectConversations(t, ev)
	if !strings.Contains(ev.Data, `data-name="later"`) {
		t.Fatalf("reconnect missed the change: %s", ev.Data)
	}
}

func TestReconnectedTranscriptSendsMetaChangedWhileAway(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	seq := publishSeq(t, addr, "", "job", "m1")

	s := openEvents(t, page.URL+"/events?conversation=job", nil)
	expectMessage(t, s.next(t), seq, "job")
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "open")
	s.close()

	if res := busCall(t, addr, "", "close", client.CloseRequest{Conversation: "job"}); res.Outcome != bus.OutcomeOK {
		t.Fatalf("close %+v", res)
	}
	s = openEvents(t, page.URL+"/events?conversation=job", http.Header{"Last-Event-ID": {strconv.FormatInt(seq, 10)}})
	expectConversations(t, s.next(t))
	expectMeta(t, s.next(t), "closed")
}

func TestReconnectResumesFromLastEventIDOverStaleAfter(t *testing.T) {
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	createConv(t, addr, "", "job")
	one := publishSeq(t, addr, "", "job", "m1")
	two := publishSeq(t, addr, "", "job", "m2")

	s := openEvents(t, page.URL+"/events?after=0", nil)
	expectMessage(t, s.next(t), one, "job")
	expectMessage(t, s.next(t), two, "job")
	expectConversations(t, s.next(t))
	s.close()

	three := publishSeq(t, addr, "", "job", "m3")
	four := publishSeq(t, addr, "", "job", "m4")
	s = openEvents(t, page.URL+"/events?after=0", http.Header{"Last-Event-ID": {strconv.FormatInt(two, 10)}})
	expectMessage(t, s.next(t), three, "job")
	expectMessage(t, s.next(t), four, "job")
	expectConversations(t, s.next(t))
}

func TestReorderingPublishSendsConversationsOnEveryView(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	createConv(t, addr, "", "a")
	createConv(t, addr, "", "b")
	last := strconv.FormatInt(publishSeq(t, addr, "", "a", "a0"), 10)

	all := openEvents(t, page.URL+"/events?after="+last, nil)
	tr := openEvents(t, page.URL+"/events?conversation=a&after="+last, nil)
	list := openEvents(t, page.URL+"/events?view=list", nil)
	for _, s := range []*sseStream{all, tr, list} {
		expectConversations(t, s.next(t))
	}
	expectMeta(t, tr.next(t), "open")

	before := proxy.count(bus.PathList)
	same := publishSeq(t, addr, "", "a", "a1")
	expectMessage(t, all.next(t), same, "a")
	expectMessage(t, tr.next(t), same, "a")
	moved := publishSeq(t, addr, "", "b", "b1")
	expectMessage(t, all.next(t), moved, "b")
	for _, s := range []*sseStream{all, tr, list} {
		ev := s.next(t)
		expectConversations(t, ev)
		if strings.Index(ev.Data, `data-name="b"`) > strings.Index(ev.Data, `data-name="a"`) {
			t.Fatalf("b is not first: %s", ev.Data)
		}
	}
	if got := proxy.count(bus.PathList) - before; got != 3 {
		t.Fatalf("list calls = %d, want one per stream for the reorder only", got)
	}
}

func TestQuietStreamSendsAKeepAliveComment(t *testing.T) {
	prev := watchDeadline
	watchDeadline = 50 * time.Millisecond
	t.Cleanup(func() { watchDeadline = prev })
	addr := newBus(t, "", false)
	page, _ := newPage(t, addr, "")
	s := openEvents(t, page.URL+"/events?view=list", nil)
	expectConversations(t, s.next(t))
	ev := s.nextRecord(t)
	if ev.Comment != "keep-alive" {
		t.Fatalf("want keep-alive comment, got %+v", ev)
	}
}

func TestServeEndsStreamsWithoutWaitingForShutdownLimit(t *testing.T) {
	addr := newBus(t, "", false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, ln, addr, "", slog.New(slog.DiscardHandler)) }()
	waitListen(t, ln.Addr().String(), errCh)

	s := openEvents(t, "http://"+ln.Addr().String()+"/events?view=list", nil)
	expectConversations(t, s.next(t))
	start := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Serve waited on the open stream")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Serve took %s", d)
	}
	s.expectEnd(t)
}

func TestEventsRefusesWrongHostBeforeBusCall(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	req, err := http.NewRequest(http.MethodGet, page.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example:4728"
	status, _, hdr := doPage(t, req)
	if status != http.StatusForbidden || hdr.Get("Content-Type") == "text/event-stream" {
		t.Fatalf("status %d type %q", status, hdr.Get("Content-Type"))
	}
	if n := proxy.count(""); n != 0 {
		t.Fatalf("bus calls %d", n)
	}
}

func TestStreamThatCanNeverSucceedIs400BeforeBusCall(t *testing.T) {
	addr := newBus(t, "", false)
	proxy := newBusProxy(t, addr)
	page, _ := newPage(t, proxy.addr(), "")
	cases := []struct {
		query  string
		header string
	}{
		{query: "after=-1"},
		{query: "after=x"},
		{query: "after="},
		{query: "conversation=a&after=1.5"},
		{header: "abc"},
		{query: "after=3", header: "-2"},
		{query: "view=all"},
		{query: "view="},
		{query: "view=list&conversation=a"},
		{query: "conversation="},
	}
	for _, c := range cases {
		req, err := http.NewRequest(http.MethodGet, page.URL+"/events?"+c.query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.header != "" {
			req.Header.Set("Last-Event-ID", c.header)
		}
		status, body, hdr := doPage(t, req)
		if status != http.StatusBadRequest || !strings.HasPrefix(hdr.Get("Content-Type"), "text/plain") || hdr.Get("Cache-Control") != "no-store" || strings.TrimSpace(body) == "" {
			t.Fatalf("%q %q: %d %q %q", c.query, c.header, status, hdr.Get("Content-Type"), body)
		}
	}
	if n := proxy.count(""); n != 0 {
		t.Fatalf("bus calls %d", n)
	}

	status, body, hdr := doPage(t, mustGet(t, page.URL+"/events?conversation="+url.QueryEscape("bad name")))
	if status != http.StatusBadRequest || hdr.Get("Content-Type") == "text/event-stream" || strings.TrimSpace(body) != bus.ReasonBadConversation {
		t.Fatalf("bus-refused name: %d %q %q", status, hdr.Get("Content-Type"), body)
	}

	req := mustGet(t, page.URL+"/events?view=list&after=x")
	req.Header.Set("Last-Event-ID", "y")
	s := openRequest(t, req)
	expectConversations(t, s.next(t))
}

// sseEvent is one dispatched record. Comment and Retry records have no Name.
type sseEvent struct {
	Name    string
	ID      string
	Data    string
	Comment string
	Retry   string
}

type sseStream struct {
	header http.Header
	events chan sseEvent
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	text   strings.Builder
}

func openEvents(t *testing.T, rawURL string, header http.Header) *sseStream {
	t.Helper()
	req := mustGet(t, rawURL)
	for k, v := range header {
		req.Header[k] = v
	}
	return openRequest(t, req)
}

func openRequest(t *testing.T, req *http.Request) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("events %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	s := &sseStream{header: resp.Header.Clone(), events: make(chan sseEvent, 256), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go s.read(resp.Body)
	t.Cleanup(s.close)
	return s
}

func (s *sseStream) read(body io.ReadCloser) {
	defer close(s.done)
	defer close(s.events)
	defer func() { _ = body.Close() }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var ev sseEvent
	var data []string
	seen := false
	for sc.Scan() {
		line := sc.Text()
		s.mu.Lock()
		s.text.WriteString(line + "\n")
		s.mu.Unlock()
		if line == "" {
			if seen {
				ev.Data = strings.Join(data, "\n")
				// close waits on done, so a full buffer must not hold the reader after cancel.
				select {
				case s.events <- ev:
				case <-s.ctx.Done():
					return
				}
			}
			ev, data, seen = sseEvent{}, nil, false
			continue
		}
		seen = true
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "":
			ev.Comment = value
		case "event":
			ev.Name = value
		case "id":
			ev.ID = value
		case "data":
			data = append(data, value)
		case "retry":
			ev.Retry = value
		}
	}
}

func (s *sseStream) close() {
	s.cancel()
	<-s.done
}

func (s *sseStream) raw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
}

// nextRecord returns the next record, including retry and comment records.
func (s *sseStream) nextRecord(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatalf("stream ended\n%s", s.raw())
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("no event\n%s", s.raw())
	}
	return sseEvent{}
}

// next returns the next named event.
func (s *sseStream) next(t *testing.T) sseEvent {
	t.Helper()
	for {
		ev := s.nextRecord(t)
		if ev.Name != "" {
			return ev
		}
	}
}

// nextExcept returns the next named event that is not skip.
func (s *sseStream) nextExcept(t *testing.T, skip string) sseEvent {
	t.Helper()
	for {
		ev := s.next(t)
		if ev.Name != skip {
			return ev
		}
	}
}

func (s *sseStream) expectEnd(t *testing.T) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}
			if ev.Name != "" {
				t.Fatalf("want end, got %+v", ev)
			}
		case <-timeout:
			t.Fatalf("stream did not end\n%s", s.raw())
		}
	}
}

func expectMessage(t *testing.T, ev sseEvent, seq int64, conv string) {
	t.Helper()
	n := strconv.FormatInt(seq, 10)
	want := `<li class="message" data-seq="` + n + `" data-conversation="` + conv + `">`
	if ev.Name != eventMessage || ev.ID != n || !strings.HasPrefix(ev.Data, want) || !strings.HasSuffix(ev.Data, "</li>") {
		t.Fatalf("want message %s in %s, got %+v", n, conv, ev)
	}
}

func expectConversations(t *testing.T, ev sseEvent) {
	t.Helper()
	if ev.Name != eventConversations || ev.ID != "" {
		t.Fatalf("want conversations, got %+v", ev)
	}
	for _, id := range []string{`id="open-section"`, `id="closed-section"`, `id="conversation-index"`} {
		if strings.Count(ev.Data, id) != 1 {
			t.Fatalf("conversations without %s: %s", id, ev.Data)
		}
	}
}

func expectMeta(t *testing.T, ev sseEvent, status string) {
	t.Helper()
	if ev.Name != eventMeta || ev.ID != "" || !strings.HasPrefix(ev.Data, `<aside id="conversation-meta">`) || !strings.Contains(ev.Data, `data-status="`+status+`"`) {
		t.Fatalf("want meta with status %s, got %+v", status, ev)
	}
}

func publishSeq(t *testing.T, addr, token, conv, txid string) int64 {
	t.Helper()
	res := busCall(t, addr, token, "publish", client.PublishRequest{
		Conversation: conv, From: "alice", To: []string{}, Body: "body " + txid + "\nline two", TxID: txid, Kind: "say",
	})
	if res.Outcome != bus.OutcomeOK || res.Message == nil {
		t.Fatalf("publish %s: %+v", txid, res)
	}
	return res.Message.Seq
}

func mustGet(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func doPage(t *testing.T, req *http.Request) (int, string, http.Header) {
	t.Helper()
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

// lockedBuffer is a log sink that a stream handler writes while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(b.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("log has no %q\n%s", text, b.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// busProxy forwards to a real bus, records each path, and can fail list or watch.
type busProxy struct {
	srv       *httptest.Server
	mu        sync.Mutex
	paths     []string
	failList  bool
	failWatch bool
	hold      chan struct{}
}

func newBusProxy(t *testing.T, busAddr string) *busProxy {
	t.Helper()
	target, err := url.Parse("http://" + busAddr)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorLog = log.New(io.Discard, "", 0)
	p := &busProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.Path)
		fail := (p.failList && r.URL.Path == bus.PathList) || (p.failWatch && r.URL.Path == bus.PathWatch)
		var hold chan struct{}
		if r.URL.Path == bus.PathWatch {
			hold = p.hold
		}
		p.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"outcome":"unavailable"}`)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *busProxy) addr() string { return p.srv.Listener.Addr().String() }

func (p *busProxy) set(f func(*busProxy)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// waitCount waits until path has been called n times.
func (p *busProxy) waitCount(t *testing.T, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.count(path) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s calls %d, want %d", path, p.count(path), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// count is the number of calls to path, or every call when path is empty.
func (p *busProxy) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, got := range p.paths {
		if path == "" || got == path {
			n++
		}
	}
	return n
}
