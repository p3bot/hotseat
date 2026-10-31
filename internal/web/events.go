// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	routeEvents = "/events"

	eventMessage       = "message"
	eventConversations = "conversations"
	eventMeta          = "meta"
	eventReset         = "reset"

	viewParamList = "list"

	// streamRetry is the reconnect delay, in milliseconds, the stream gives the browser.
	streamRetry = 2000
)

// watchDeadline bounds each watch after the first, so a quiet stream still writes.
var watchDeadline = 25 * time.Second

// streamView is the part of the bus one /events stream follows.
// A transcript names its conversation. Conversations has no messages.
type streamView int

const (
	viewMessages streamView = iota
	viewTranscript
	viewList
)

func (v streamView) String() string {
	switch v {
	case viewTranscript:
		return "transcript"
	case viewList:
		return "list"
	default:
		return "messages"
	}
}

// stream is the state of one /events response.
// cursor is the highest seq of the view sent. rev, conversations, and meta are
// what was last sent. hasRev is false until a list after a watch has been sent.
type stream struct {
	view          streamView
	name          string
	cursor        int64
	rev           uint64
	hasRev        bool
	conversations string
	meta          string
}

// parseStream reads the view and the start position. A false result carries
// the reason the request can never succeed.
func parseStream(r *http.Request) (stream, string, bool) {
	q := r.URL.Query()
	st := stream{view: viewMessages}
	if q.Has("view") {
		if q.Get("view") != viewParamList {
			return stream{}, "view must be list or omitted", false
		}
		if q.Has("conversation") {
			return stream{}, "view list does not take a conversation", false
		}
		return stream{view: viewList}, "", true
	}
	if q.Has("conversation") {
		st.view = viewTranscript
		st.name = q.Get("conversation")
		// The watch body omits an empty conversation, which would watch every conversation.
		if st.name == "" {
			return stream{}, bus.ReasonConversationRequired, false
		}
	}
	if q.Has("after") {
		n, ok := position(q.Get("after"))
		if !ok {
			return stream{}, "after must be an integer of at least 0", false
		}
		st.cursor = n
	}
	// The browser's reconnect reuses the original URL, so its after is stale.
	if ids := r.Header.Values("Last-Event-ID"); len(ids) > 0 {
		n, ok := position(ids[0])
		if !ok {
			return stream{}, "Last-Event-ID must be an integer of at least 0", false
		}
		st.cursor = n
	}
	return st, "", true
}

func position(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// watchRequest is the next watch. The first omits rev and asks for one more
// than a group, so a gap wider than a group is seen as a reset.
func (st *stream) watchRequest() client.WatchRequest {
	req := client.WatchRequest{}
	if st.view == viewTranscript {
		req.Conversation = st.name
	}
	if st.view != viewList {
		cursor := st.cursor
		limit := groupLimit
		if !st.hasRev {
			limit++
		}
		req.Cursor, req.Limit = &cursor, &limit
	}
	if st.hasRev {
		rev := st.rev
		deadline := watchDeadline.String()
		req.Rev, req.Deadline = &rev, &deadline
	}
	return req
}

// handleEvents serves one Server-Sent Events stream for one tab's view.
// The stream ends when stop ends, so a blocked watch does not hold Shutdown.
func (s Server) handleEvents(stop context.Context, w http.ResponseWriter, r *http.Request) {
	st, reason, ok := parseStream(r)
	if !ok {
		s.logger().Info("call", "op", "events", "outcome", bus.OutcomeRefused, "reason", reason)
		refuseStream(w, reason)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(stop, cancel)()

	log := s.logger().With("op", "events", "view", st.view.String())
	if st.view == viewTranscript {
		log = log.With("conversation", st.name)
	}

	res, err := s.watch(ctx, &st)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("stream", "err", err)
			http.Error(w, "the page could not call the bus", http.StatusInternalServerError)
		}
		return
	}
	if res.Outcome == bus.OutcomeRefused {
		log.Info("call", "outcome", bus.OutcomeRefused, "reason", res.Reason)
		refuseStream(w, res.Reason)
		return
	}
	setStreamHeaders(w)
	w.WriteHeader(http.StatusOK)
	out := eventWriter{w: w, rc: http.NewResponseController(w)}
	if err := out.retry(); err != nil {
		return
	}
	log.Debug("stream open", "after", st.cursor)
	err = s.run(ctx, &st, res, out)
	var failure *streamFailure
	switch {
	case ctx.Err() != nil:
		log.Debug("stream closed", "cause", "request ended")
	case errors.As(err, &failure):
		log.Warn("stream ended", "call", failure.call, "outcome", failure.outcome, "reason", failure.reason)
	case errors.Is(err, errReset), errors.Is(err, errClientGone):
		log.Debug("stream closed", "cause", err.Error())
	default:
		log.Error("stream ended", "err", err)
	}
}

var (
	// errReset ends a stream whose view missed more than a group.
	errReset = errors.New("reset")
	// errClientGone is a failed write or flush. The tab has gone.
	errClientGone = errors.New("client gone")
)

// streamFailure is a bus call whose outcome ended the stream. The browser reconnects.
type streamFailure struct {
	call    string
	outcome string
	reason  string
}

func (f *streamFailure) Error() string {
	return f.call + " " + f.outcome + ": " + f.reason
}

// run applies res, then watches again until the stream ends, and returns why.
// Every message passes through the one cursor, so none is skipped or sent twice.
func (s Server) run(ctx context.Context, st *stream, res client.Result, out eventWriter) error {
	for {
		switch res.Outcome {
		case bus.OutcomeOK:
			if err := s.apply(ctx, st, res, out); err != nil {
				return err
			}
		case bus.OutcomeTimeout:
			if err := out.comment("keep-alive"); err != nil {
				return err
			}
		default:
			return &streamFailure{call: "watch", outcome: res.Outcome, reason: res.Reason}
		}
		var err error
		if res, err = s.watch(ctx, st); err != nil {
			return fmt.Errorf("watch: %w", err)
		}
	}
}

// apply sends the messages of one ok watch, then the list when rev changed.
func (s Server) apply(ctx context.Context, st *stream, res client.Result, out eventWriter) error {
	if res.Rev == nil {
		return &streamFailure{call: "watch", outcome: res.Outcome, reason: "result has no rev"}
	}
	var msgs []client.Message
	if res.Messages != nil {
		msgs = *res.Messages
	}
	if !st.hasRev && int64(len(msgs)) > groupLimit {
		if err := out.event(eventReset, "", ""); err != nil {
			return err
		}
		return errReset
	}
	for _, m := range msgs {
		frag, err := renderBlock("messageItem", newMessageItem(m.Conversation, m))
		if err != nil {
			return err
		}
		if err := out.event(eventMessage, strconv.FormatInt(m.Seq, 10), frag); err != nil {
			return err
		}
		st.cursor = m.Seq
	}
	if st.hasRev && *res.Rev == st.rev {
		return nil
	}
	if err := s.sendList(ctx, st, out); err != nil {
		return err
	}
	st.rev, st.hasRev = *res.Rev, true
	return nil
}

// sendList lists conversations and sends each fragment that changed.
// The first pass sends both, since the tab may hold a change made while no stream was open.
func (s Server) sendList(ctx context.Context, st *stream, out eventWriter) error {
	res, err := s.list(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if res.Outcome != bus.OutcomeOK {
		return &streamFailure{call: "list", outcome: res.Outcome, reason: res.Reason}
	}
	p := page{Name: st.name}
	applyList(&p, res.Conversations)
	sections, err := renderBlock("convSections", p)
	if err != nil {
		return err
	}
	index, err := renderBlock("convIndex", p)
	if err != nil {
		return err
	}
	if frag := sections + index; frag != st.conversations {
		if err := out.event(eventConversations, "", frag); err != nil {
			return err
		}
		st.conversations = frag
	}
	if st.view != viewTranscript {
		return nil
	}
	meta, err := renderBlock("convMeta", p)
	if err != nil {
		return err
	}
	if meta != st.meta {
		if err := out.event(eventMeta, "", meta); err != nil {
			return err
		}
		st.meta = meta
	}
	return nil
}

func (s Server) watch(ctx context.Context, st *stream) (client.Result, error) {
	return client.Do(ctx, s.BusAddress, "watch", s.Token, st.watchRequest())
}

func renderBlock(name string, data any) (string, error) {
	var b bytes.Buffer
	if err := pageTmpl.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return b.String(), nil
}

// refuseStream answers a stream that can never succeed. EventSource does not
// reconnect after a status other than 200.
func refuseStream(w http.ResponseWriter, reason string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, reason, http.StatusBadRequest)
}

func setStreamHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// eventWriter writes the event stream format and flushes each event.
type eventWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (e eventWriter) retry() error {
	return e.send("retry: " + strconv.Itoa(streamRetry) + "\n\n")
}

func (e eventWriter) comment(text string) error {
	return e.send(": " + text + "\n\n")
}

// event writes one event. An empty id leaves the browser's last event id unchanged.
// Each line of data is its own data field, so the browser joins them with newlines.
func (e eventWriter) event(name, id, data string) error {
	var b strings.Builder
	b.WriteString("event: " + name + "\n")
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	// The stream ends a line at CR as well. The HTML parser turns CR and CRLF into LF anyway.
	data = strings.ReplaceAll(strings.ReplaceAll(data, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}
	b.WriteString("\n")
	return e.send(b.String())
}

func (e eventWriter) send(text string) error {
	if _, err := io.WriteString(e.w, text); err != nil {
		return fmt.Errorf("%w: %w", errClientGone, err)
	}
	if err := e.rc.Flush(); err != nil {
		return fmt.Errorf("%w: %w", errClientGone, err)
	}
	return nil
}
