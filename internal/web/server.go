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
	routeConversation = "/c"
	routePublish      = "/publish"

	// DefaultListen is the loopback address of the page when the operator does not set one.
	DefaultListen = "127.0.0.1:4728"
)

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

// Handler serves the list, the transcript, and publish.
// It calls only those bus operations.
// A request whose host is not ListenHost is refused before any of those calls.
func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleList)
	mux.HandleFunc("GET "+routeConversation, s.handleConversation)
	mux.HandleFunc("POST "+routeConversation, s.handleRead)
	mux.HandleFunc("POST "+routePublish, s.handlePublish)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.wrongHost(r) {
			s.logger().Info("call", "op", "page", "outcome", bus.OutcomeRefused, "reason", "wrong host")
			s.render(w, page{
				Title:      "Page",
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
		}.Handler(),
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

func (s Server) handleList(w http.ResponseWriter, r *http.Request) {
	res, err := s.list(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		s.render(w, page{Kind: kindList, Title: "Conversations", Notice: err.Error()})
		return
	}
	p := page{Kind: kindList, Title: "Conversations"}
	if res.Outcome != bus.OutcomeOK {
		p.ListOutcome = res.Outcome
		p.ListReason = res.Reason
		s.render(w, p)
		return
	}
	p.Conversations = rows(res.Conversations)
	s.render(w, p)
}

func (s Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("conversation")
	if !q.Has("conversation") || name == "" {
		s.render(w, page{Title: "Conversation", Notice: "conversation is required"})
		return
	}
	p := conversationPage(name)
	if err := s.fillStatus(r.Context(), &p); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		s.render(w, p)
		return
	}
	asked := q.Has("cursor") || q.Has("limit")
	if asked {
		p.Cursor = q.Get("cursor")
		p.Limit = q.Get("limit")
	}
	if err := s.applyCursor(r.Context(), &p, asked); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
	}
	s.render(w, p)
}

func (s Server) handleRead(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.logger().Error("read form", "err", err)
		s.render(w, page{Title: "Conversation", Notice: err.Error()})
		return
	}
	name := r.PostForm.Get("conversation")
	if name == "" {
		s.render(w, page{Title: "Conversation", Notice: "conversation is required"})
		return
	}
	p := conversationPage(name)
	fillPosted(&p, r)
	if err := s.fillStatus(r.Context(), &p); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		s.render(w, p)
		return
	}
	if err := s.applyCursor(r.Context(), &p, true); err != nil {
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
			Title:      "Publish",
			Notice:     "this publish did not come from this page",
			HTTPStatus: http.StatusForbidden,
		})
		return
	}
	if err := r.ParseForm(); err != nil {
		s.logger().Error("publish form", "err", err)
		s.render(w, page{Title: "Publish", Notice: err.Error()})
		return
	}
	name := r.PostForm.Get("conversation")
	if name == "" {
		p := page{Title: "Publish", Notice: "conversation is required"}
		fillPosted(&p, r)
		s.render(w, p)
		return
	}
	p := conversationPage(name)
	fillPosted(&p, r)
	if err := s.fillStatus(r.Context(), &p); err != nil {
		if r.Context().Err() != nil {
			return
		}
		p.Notice = err.Error()
		s.render(w, p)
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
		s.render(w, p)
		return
	}
	s.logCall("publish", name, res, start)
	view := &publishView{Outcome: res.Outcome, Reason: res.Reason}
	if res.AlreadyStored != nil && *res.AlreadyStored {
		view.AlreadyStored = true
	}
	if res.Outcome == bus.OutcomeOK && res.Message != nil {
		msg := *res.Message
		view.Message = &msg
		view.Stored = !view.AlreadyStored
	}
	p.Publish = view
	if res.Conversation != nil {
		p.Status = res.Conversation.Status
		p.StatusKnown = true
	}
	if err := s.applyCursor(r.Context(), &p, false); err != nil {
		if r.Context().Err() != nil {
			return
		}
		if p.Notice == "" {
			p.Notice = err.Error()
		}
	}
	if res.Outcome == bus.OutcomeOK {
		p.TxID = ""
	}
	s.render(w, p)
}

func conversationPage(name string) page {
	return page{
		Kind:          kindConversation,
		Title:         name,
		Name:          name,
		ReadAction:    routeConversation,
		PublishAction: routePublish,
	}
}

func fillPosted(p *page, r *http.Request) {
	p.Cursor = r.PostForm.Get("cursor")
	p.Limit = r.PostForm.Get("limit")
	p.From = r.PostForm.Get("from")
	p.ToText = r.PostForm.Get("to")
	p.MessageKind = r.PostForm.Get("kind")
	p.Body = r.PostForm.Get("body")
	p.TxID = r.PostForm.Get("txid")
}

// applyCursor reads when the request asked for a transcript.
// readAsked is set for a read. A publish leaves it false and reads only
// when the request carried a cursor or a limit. The values stay on the
// page for the response and are not kept.
func (s Server) applyCursor(ctx context.Context, p *page, readAsked bool) error {
	if !readAsked && p.Cursor == "" && p.Limit == "" {
		return nil
	}
	if p.Cursor == "" || p.Limit == "" {
		p.Notice = "cursor and limit are both required"
		return nil
	}
	cursor, limit, ok := parseCursorLimit(p.Cursor, p.Limit)
	if !ok {
		p.Notice = "cursor and limit must be integers"
		return nil
	}
	return s.fillRead(ctx, p, cursor, limit)
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
	if res.Conversations == nil {
		return nil
	}
	for _, c := range *res.Conversations {
		if c.Name == p.Name {
			p.Status = c.Status
			p.StatusKnown = true
			return nil
		}
	}
	return nil
}

func (s Server) fillRead(ctx context.Context, p *page, cursor, limit int64) error {
	start := time.Now()
	res, err := client.Do(ctx, s.BusAddress, "read", s.Token, client.ReadRequest{
		Conversation: p.Name,
		Cursor:       cursor,
		Limit:        limit,
	})
	if err != nil {
		return err
	}
	s.logCall("read", p.Name, res, start)
	view := &readView{Outcome: res.Outcome, Reason: res.Reason}
	if res.Messages != nil {
		view.Messages = *res.Messages
	}
	if res.Outcome == bus.OutcomeOK && limit > 0 && int64(len(view.Messages)) == limit && len(view.Messages) > 0 {
		last := view.Messages[len(view.Messages)-1].Seq
		view.Full = true
		view.NextCursor = last
		view.NextPage = readHref(p.Name, strconv.FormatInt(last, 10), strconv.FormatInt(limit, 10))
	}
	p.Read = view
	return nil
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

func rows(cs *[]client.Conversation) []conversationRow {
	if cs == nil {
		return nil
	}
	out := make([]conversationRow, 0, len(*cs))
	for _, c := range *cs {
		out = append(out, conversationRow{
			Name:   c.Name,
			Status: c.Status,
			Href:   conversationHref(c.Name),
		})
	}
	return out
}

func conversationHref(name string) string {
	v := url.Values{}
	v.Set("conversation", name)
	return routeConversation + "?" + v.Encode()
}

func readHref(name, cursor, limit string) string {
	v := url.Values{}
	v.Set("conversation", name)
	v.Set("cursor", cursor)
	v.Set("limit", limit)
	return routeConversation + "?" + v.Encode()
}

func parseCursorLimit(cursorRaw, limitRaw string) (int64, int64, bool) {
	cursor, err := strconv.ParseInt(cursorRaw, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	limit, err := strconv.ParseInt(limitRaw, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return cursor, limit, true
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
