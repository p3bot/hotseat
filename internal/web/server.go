// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	routeMessages      = "/"
	routeConversations = "/conversations"

	// DefaultListen is the loopback address of the page when the operator does not set one.
	DefaultListen = "127.0.0.1:4728"
)

// URL is the address a browser opens for a page bound at hostport.
func URL(hostport string) string {
	return "http://" + hostport
}

// Server calls one bus.
// Token is the capability secret. Empty omits the header.
// Page responses do not include Token.
// ListenHost is the host:port this page answers as.
// Any other host is refused before a bus call. An empty ListenHost refuses every request.
// On a loopback address, localhost with the same port is that host.
type Server struct {
	BusAddress string
	Token      string
	Log        *slog.Logger
	ListenHost string
}

// Handler serves the messages page, the conversation list, a transcript,
// publish, and the GET /events stream.
// It calls only list, messages, read, publish, and watch.
// A request whose host is not ListenHost is refused before any of those calls.
// A stream ends when its request ends. Serve also ends every stream on shutdown.
func (s Server) Handler() http.Handler {
	return s.handler(context.Background())
}

// handler is Handler with stop, which ends every open stream when it is done.
func (s Server) handler(stop context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+routeMessages+"{$}", s.handleMessages)
	mux.HandleFunc("GET "+routeConversations, s.handleList)
	mux.HandleFunc("GET "+routeConversations+"/{name}", s.handleConversation)
	mux.HandleFunc("POST "+routeConversations+"/{name}/messages", s.handlePublish)
	mux.HandleFunc("GET "+routeEvents, func(w http.ResponseWriter, r *http.Request) {
		s.handleEvents(stop, w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.wrongHost(r) {
			s.logger().Info("call", "op", "page", "outcome", bus.OutcomeRefused, "reason", "wrong host")
			s.render(w, page{
				Notice:     "this page does not serve that host",
				HTTPStatus: http.StatusForbidden,
			})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Serve accepts page connections on ln until ctx is cancelled.
// It does not open the store. busAddr is the running bus.
// A socket that is not loopback is closed and returned as an error.
// The page holds the bus token, so a public socket would hand that token to the network.
func Serve(ctx context.Context, ln net.Listener, busAddr, token string, log *slog.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if _, _, err := net.SplitHostPort(busAddr); err != nil {
		cerr := ln.Close()
		if cerr != nil {
			return fmt.Errorf("bus address: %w", errors.Join(err, cerr))
		}
		return fmt.Errorf("bus address: %w", err)
	}
	if !bus.AddrLoopback(ln.Addr()) {
		err := fmt.Errorf("page listen address must be loopback: %s", ln.Addr().String())
		if cerr := ln.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return err
	}
	httpSrv := &http.Server{
		Handler: Server{
			BusAddress: busAddr,
			Token:      token,
			Log:        log,
			ListenHost: ln.Addr().String(),
		}.handler(ctx),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	bound := ln.Addr().String()
	log.Info("listening", "addr", bound, "bus", busAddr, "token_configured", token != "")

	select {
	case <-ctx.Done():
		return finish(httpSrv, errCh, bound, log)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			log.Info("stopped", "addr", bound)
			return nil
		}
		return err
	}
}

func finish(httpSrv *http.Server, errCh <-chan error, bound string, log *slog.Logger) error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shErr := httpSrv.Shutdown(shutCtx)
	cancel()
	if shErr != nil {
		if cerr := httpSrv.Close(); cerr != nil {
			shErr = errors.Join(shErr, cerr)
		}
	}
	err := <-errCh
	if shErr != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(err, shErr)
	}
	if errors.Is(err, http.ErrServerClosed) {
		log.Info("stopped", "addr", bound)
		return nil
	}
	return err
}

func (s Server) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	res, err := s.list(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.render(w, page{Kind: kindMessages, Notice: err.Error()})
		return
	}
	p := page{Kind: kindMessages}
	if res.Outcome != bus.OutcomeOK {
		p.ListOutcome = res.Outcome
		p.ListReason = res.Reason
		s.render(w, p)
		return
	}
	applyList(&p, res.Conversations)
	before, newest, ok := queryBefore(r.URL.Query())
	if !ok {
		p.Notice = bus.ReasonBeforeRange
		s.render(w, p)
		return
	}
	if err := s.fillMessages(r.Context(), &p, before, newest); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
	}
	s.render(w, p)
}

func (s Server) handleList(w http.ResponseWriter, r *http.Request) {
	res, err := s.list(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.render(w, page{Kind: kindList, Notice: err.Error()})
		return
	}
	p := page{Kind: kindList}
	if res.Outcome != bus.OutcomeOK {
		p.ListOutcome = res.Outcome
		p.ListReason = res.Reason
		s.render(w, p)
		return
	}
	applyList(&p, res.Conversations)
	s.render(w, p)
}

func (s Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := r.PathValue("name")
	p := conversationPage(name)
	if err := s.fillStatus(r.Context(), &p); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		s.render(w, p)
		return
	}
	before, newest, ok := queryBefore(q)
	if !ok {
		p.Notice = bus.ReasonBeforeRange
		s.render(w, p)
		return
	}
	if err := s.fillRecent(r.Context(), &p, before, newest); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
	}
	s.render(w, p)
}

func (s Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if foreignPublish(r) {
		s.logger().Info("call", "op", "publish", "outcome", bus.OutcomeRefused, "reason", "cross-site request")
		s.render(w, page{
			Notice:     "this publish did not come from this page",
			HTTPStatus: http.StatusForbidden,
		})
		return
	}
	if err := r.ParseForm(); err != nil {
		s.logger().Error("publish form", "err", err)
		s.render(w, page{Notice: err.Error()})
		return
	}
	name := r.PathValue("name")
	p := conversationPage(name)
	fillPosted(&p, r)
	if err := s.fillStatus(r.Context(), &p); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		renderPublish(s, w, p)
		return
	}
	start := time.Now()
	res, err := client.Do(r.Context(), s.BusAddress, "publish", s.Token, client.PublishRequest{
		Conversation: name,
		From:         p.From,
		To:           namesFromLines(p.ToText),
		Body:         p.Body,
		TxID:         p.TxID,
		Kind:         p.MessageKind,
	})
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		renderPublish(s, w, p)
		return
	}
	s.logCall("publish", name, res, start)
	if res.Outcome == bus.OutcomeOK {
		setPageHeaders(w)
		http.Redirect(w, r, conversationPath(name), http.StatusSeeOther)
		return
	}
	p.Publish = &publishView{Outcome: res.Outcome, Reason: res.Reason}
	if res.Conversation != nil {
		p.Status = res.Conversation.Status
		p.StatusKnown = true
	}
	if err := s.fillRecent(r.Context(), &p, 0, true); err != nil {
		if r.Context().Err() != nil {
			return
		}
		if p.Notice == "" {
			p.Notice = err.Error()
		}
	}
	renderPublish(s, w, p)
}

// renderPublish opens Send when the attempt did not store.
func renderPublish(s Server, w http.ResponseWriter, p page) {
	if p.Publish == nil || p.Publish.Outcome != bus.OutcomeOK {
		p.SendOpen = true
	}
	s.render(w, p)
}

func conversationPage(name string) page {
	return page{
		Kind:          kindConversation,
		Name:          name,
		PublishAction: messagesPath(name),
	}
}

func fillPosted(p *page, r *http.Request) {
	p.From = r.PostForm.Get("from")
	p.ToText = r.PostForm.Get("to")
	p.MessageKind = r.PostForm.Get("kind")
	p.Body = r.PostForm.Get("body")
	p.TxID = r.PostForm.Get("txid")
}

// groupLimit is the size of one message group on either page. The next group is older.
var groupLimit int64 = 50

// queryBefore reads the older-group boundary. An absent value selects the newest group.
func queryBefore(q url.Values) (before int64, newest, ok bool) {
	if !q.Has("before") {
		return 0, true, true
	}
	n, err := strconv.ParseInt(q.Get("before"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false, false
	}
	return n, false, true
}

func (s Server) list(ctx context.Context) (client.Result, error) {
	start := time.Now()
	res, err := client.Do(ctx, s.BusAddress, "list", s.Token, struct{}{})
	if err != nil {
		return client.Result{}, err
	}
	s.logCall("list", "", res, start)
	return res, nil
}

func (s Server) fillStatus(ctx context.Context, p *page) error {
	res, err := s.list(ctx)
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		p.ListOutcome = res.Outcome
		p.ListReason = res.Reason
		return nil
	}
	applyList(p, res.Conversations)
	return nil
}

// fillMessages shows one group across every conversation, newest message first.
// A full group may have older messages below it, so it links the next group.
func (s Server) fillMessages(ctx context.Context, p *page, before int64, newest bool) error {
	req := client.MessagesRequest{Limit: groupLimit}
	if !newest {
		req.Before = &before
	}
	start := time.Now()
	res, err := client.Do(ctx, s.BusAddress, "messages", s.Token, req)
	if err != nil {
		return err
	}
	s.logCall("messages", "", res, start)
	view := &messageGroup{Outcome: res.Outcome, Reason: res.Reason}
	p.Group = view
	if res.Outcome != bus.OutcomeOK || res.Messages == nil {
		return nil
	}
	msgs := *res.Messages
	for _, m := range msgs {
		view.Messages = append(view.Messages, newMessageItem(m.Conversation, m))
	}
	if int64(len(msgs)) == groupLimit {
		view.Full = true
		view.NextPage = olderHref(routeMessages, msgs[len(msgs)-1].Seq)
	}
	return nil
}

// fillRecent shows one group, newest message first.
// The bus read walks oldest first, so this keeps a window of groupLimit.
func (s Server) fillRecent(ctx context.Context, p *page, before int64, newest bool) error {
	view, err := s.recent(ctx, p.Name, before, newest)
	if err != nil {
		return err
	}
	p.Group = view
	return nil
}

func (s Server) recent(ctx context.Context, name string, before int64, newest bool) (*messageGroup, error) {
	var window []client.Message
	more := false
	cursor := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := time.Now()
		res, err := client.Do(ctx, s.BusAddress, "read", s.Token, client.ReadRequest{
			Conversation: name,
			Cursor:       cursor,
			Limit:        groupLimit,
		})
		if err != nil {
			return nil, err
		}
		s.logCall("read", name, res, start)
		view := &messageGroup{Outcome: res.Outcome, Reason: res.Reason}
		if res.Outcome != bus.OutcomeOK {
			return view, nil
		}
		if res.Messages == nil || len(*res.Messages) == 0 {
			break
		}
		for _, m := range *res.Messages {
			if !newest && m.Seq >= before {
				return transcriptGroup(name, window, more), nil
			}
			if int64(len(window)) == groupLimit {
				copy(window, window[1:])
				window[len(window)-1] = m
				more = true
				continue
			}
			window = append(window, m)
		}
		if int64(len(*res.Messages)) < groupLimit {
			break
		}
		cursor = (*res.Messages)[len(*res.Messages)-1].Seq
	}
	return transcriptGroup(name, window, more), nil
}

// transcriptGroup turns an oldest-first window into items, newest first.
// A read message carries no conversation, so each item takes name.
func transcriptGroup(name string, window []client.Message, more bool) *messageGroup {
	view := &messageGroup{Outcome: bus.OutcomeOK}
	for i := len(window) - 1; i >= 0; i-- {
		view.Messages = append(view.Messages, newMessageItem(name, window[i]))
	}
	if more && len(window) > 0 {
		view.Full = true
		view.NextPage = olderHref(conversationHref(name), window[0].Seq)
	}
	return view
}

func newMessageItem(conversation string, m client.Message) messageItem {
	return messageItem{Conversation: conversation, Href: conversationHref(conversation), Message: m}
}

func (s Server) logCall(op, conv string, res client.Result, start time.Time) {
	attrs := []any{"op", op, "outcome", res.Outcome, "duration", time.Since(start)}
	if conv != "" {
		attrs = append(attrs, "conversation", conv)
	}
	if res.Reason != "" {
		attrs = append(attrs, "reason", res.Reason)
	}
	if op == "publish" && res.AlreadyStored != nil {
		attrs = append(attrs, "already_stored", *res.AlreadyStored)
	}
	s.logger().Info("call", attrs...)
}

// applyList fills the index, then splits the rows into Open and Closed.
// Each section keeps the bus order, newest activity first.
func applyList(p *page, cs *[]client.Conversation) {
	if cs == nil {
		return
	}
	p.Index = make([]indexEntry, 0, len(*cs))
	for _, c := range *cs {
		href := conversationHref(c.Name)
		p.Index = append(p.Index, indexEntry{
			Name: c.Name, Status: c.Status, Task: c.Task, Ticket: c.Ticket, Href: href,
		})
		row := conversationRow{Name: c.Name, Href: href, Task: c.Task, Ticket: c.Ticket, Seat: c.Seat}
		if c.Status == "open" {
			p.Open = append(p.Open, row)
		} else {
			p.Closed = append(p.Closed, row)
		}
		if p.Name != "" && c.Name == p.Name {
			p.Status = c.Status
			p.StatusKnown = true
			p.Task = c.Task
			p.Ticket = c.Ticket
			p.Seat = c.Seat
			p.Directory = c.Directory
		}
	}
}

// conversationPath is the transcript URL. The name is one path segment.
func conversationPath(name string) string {
	return routeConversations + "/" + url.PathEscape(name)
}

// messagesPath is the publish target for one conversation.
func messagesPath(name string) string {
	return conversationPath(name) + "/messages"
}

// conversationHref is the link for a conversation.
// A name of "." or ".." has no link. The router cleans those segments before a handler runs.
func conversationHref(name string) string {
	if name == "." || name == ".." {
		return ""
	}
	return conversationPath(name)
}

// olderHref is the group below before on the page at href. An empty href has no link.
func olderHref(href string, before int64) string {
	if href == "" {
		return ""
	}
	return href + "?before=" + strconv.FormatInt(before, 10)
}

// wrongHost reports a request whose host is not this page.
// The bound address is the host. On loopback, localhost with that port is too.
// The names are compared as sent. A name is not resolved, so a name that
// points at this socket is still a different host.
func (s Server) wrongHost(r *http.Request) bool {
	scheme := requestScheme(r)
	if sameAuthority(s.ListenHost, r.Host, scheme) {
		return false
	}
	return !loopbackLocalhost(s.ListenHost, r.Host, scheme)
}

// loopbackLocalhost reports localhost on the same port as a loopback bind.
func loopbackLocalhost(bound, host, scheme string) bool {
	bh, bp, ok1 := splitAuthority(bound, scheme)
	hh, hp, ok2 := splitAuthority(host, scheme)
	if !ok1 || !ok2 || bp != hp || !strings.EqualFold(hh, "localhost") {
		return false
	}
	ip := net.ParseIP(bh)
	return ip != nil && ip.IsLoopback()
}

// foreignPublish reports a browser post from another site.
// No Origin and no Sec-Fetch-Site means a non-browser client.
// The host gate has already required this page's address.
func foreignPublish(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	if origin == "null" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Scheme != requestScheme(r) {
		return true
	}
	return !sameAuthority(u.Host, r.Host, requestScheme(r))
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// sameAuthority compares host and port. An origin that omits the port means
// the scheme default, so http://host and Host: host name the same server.
// A bracketed IPv6 host without a port is that address, not a different name.
func sameAuthority(originHost, reqHost, scheme string) bool {
	oh, op, ok1 := splitAuthority(originHost, scheme)
	rh, rp, ok2 := splitAuthority(reqHost, scheme)
	if !ok1 || !ok2 {
		return false
	}
	return strings.EqualFold(oh, rh) && op == rp
}

func splitAuthority(hostport, scheme string) (string, string, bool) {
	if hostport == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p, p != ""
	}
	host := hostport
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if net.ParseIP(inner) == nil {
			return "", "", false
		}
		host = inner
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return host, port, true
}

// namesFromLines keeps order, including repeats. A blank line is not a name.
// Lines are not trimmed, so a space inside a name still reaches the bus.
func namesFromLines(text string) []string {
	if text == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}
