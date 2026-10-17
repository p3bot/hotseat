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
	p.Conversations = rows(res.Conversations)
	s.render(w, p)
}

func (s Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("conversation")
	if !q.Has("conversation") || name == "" {
		s.render(w, page{Notice: "conversation is required"})
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
	before, newest, ok := queryBefore(q)
	if !ok {
		p.Notice = "before must be an integer"
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

func (s Server) handleRead(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.logger().Error("read form", "err", err)
		s.render(w, page{Notice: err.Error()})
		return
	}
	name := r.PostForm.Get("conversation")
	if name == "" {
		s.render(w, page{Notice: "conversation is required"})
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
	if err := s.fillRecent(r.Context(), &p, 0, true); err != nil {
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
	name := r.PostForm.Get("conversation")
	if name == "" {
		p := page{Notice: "conversation is required"}
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
	if err := s.fillRecent(r.Context(), &p, 0, true); err != nil {
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
		Name:          name,
		ReadAction:    routeConversation,
		PublishAction: routePublish,
	}
}

func fillPosted(p *page, r *http.Request) {
	p.From = r.PostForm.Get("from")
	p.ToText = r.PostForm.Get("to")
	p.MessageKind = r.PostForm.Get("kind")
	p.Body = r.PostForm.Get("body")
	p.TxID = r.PostForm.Get("txid")
}

// transcriptLimit is one group. The newest group is the one on screen. The next group is older.
var transcriptLimit int64 = 50

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

// fillRecent shows one group, newest message first.
// The bus read walks oldest first, so this keeps a window of transcriptLimit.
func (s Server) fillRecent(ctx context.Context, p *page, before int64, newest bool) error {
	view, err := s.recent(ctx, p.Name, before, newest)
	if err != nil {
		return err
	}
	p.Read = view
	return nil
}

func (s Server) recent(ctx context.Context, name string, before int64, newest bool) (*readView, error) {
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
			Limit:        transcriptLimit,
		})
		if err != nil {
			return nil, err
		}
		s.logCall("read", name, res, start)
		view := &readView{Outcome: res.Outcome, Reason: res.Reason}
		if res.Outcome != bus.OutcomeOK {
			return view, nil
		}
		if res.Messages == nil || len(*res.Messages) == 0 {
			break
		}
		for _, m := range *res.Messages {
			if !newest && m.Seq >= before {
				return groupView(name, window, more), nil
			}
			if int64(len(window)) == transcriptLimit {
				copy(window, window[1:])
				window[len(window)-1] = m
				more = true
				continue
			}
			window = append(window, m)
		}
		if int64(len(*res.Messages)) < transcriptLimit {
			break
		}
		cursor = (*res.Messages)[len(*res.Messages)-1].Seq
	}
	return groupView(name, window, more), nil
}

func groupView(name string, window []client.Message, more bool) *readView {
	view := &readView{Outcome: bus.OutcomeOK, Messages: newestFirst(window)}
	if more && len(window) > 0 {
		view.Full = true
		view.NextPage = olderHref(name, window[0].Seq)
	}
	return view
}

func newestFirst(msgs []client.Message) []client.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]client.Message, len(msgs))
	for i := range msgs {
		out[len(msgs)-1-i] = msgs[i]
	}
	return out
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

func olderHref(name string, before int64) string {
	v := url.Values{}
	v.Set("conversation", name)
	v.Set("before", strconv.FormatInt(before, 10))
	return routeConversation + "?" + v.Encode()
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
