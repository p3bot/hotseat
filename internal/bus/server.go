// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/p3bot/hotseat/internal/store"
)

const requestOverhead = 64 * 1024

// Options configures a running listener.
type Options struct {
	// MaxBody is the maximum message body in bytes. Zero uses DefaultMaxBody.
	MaxBody int
	// Logger receives one line per completed call. Nil discards logs.
	Logger *slog.Logger
	// Clock supplies the bus time stored on an accepted message. Nil uses time.Now.
	Clock func() time.Time
	// OnBlock is called, without the service lock, when a wait starts blocking.
	// Tests use it to publish only after the waiter is parked.
	OnBlock func(conversation, name string)
	// Token is the shared capability secret for this listener.
	// Empty means the listener does not require one. A non-empty token is
	// required on every request and is not logged or stored.
	Token string
}

// Serve accepts connections on ln until ctx is cancelled.
// Cancelling ctx stops the process the way a signal does: blocked waits end
// as dropped connections and the store is left unchanged. st stays open.
func Serve(ctx context.Context, ln net.Listener, st *store.Store, opt Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	svc := newService(st, opt)
	defer svc.stop()
	httpSrv := &http.Server{
		Handler:           (&server{svc: svc, token: opt.Token}).routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(svc.log.Handler(), slog.LevelError),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		svc.stop()
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
			return nil
		}
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type server struct {
	svc   *Service
	token string
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathCreate, s.handleCreate)
	mux.HandleFunc("POST "+PathPublish, s.handlePublish)
	mux.HandleFunc("POST "+PathRead, s.handleRead)
	mux.HandleFunc("POST "+PathWait, s.handleWait)
	mux.HandleFunc("POST "+PathClose, s.handleClose)
	mux.HandleFunc("POST "+PathList, s.handleList)
	mux.HandleFunc("/", s.handleUnknown)
	if s.token == "" {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before the operation, so a refusal cannot block in wait or write.
		switch checkToken(r.Header, s.token) {
		case tokenOK:
			mux.ServeHTTP(w, r)
		case tokenMissing:
			s.finish(w, opName(r.URL.Path), "", time.Now(), refused(ReasonTokenRequired), nil)
		default:
			s.finish(w, opName(r.URL.Path), "", time.Now(), refused(ReasonTokenRejected), nil)
		}
	})
}

func opName(path string) string {
	switch path {
	case PathCreate:
		return "create"
	case PathPublish:
		return "publish"
	case PathRead:
		return "read"
	case PathWait:
		return "wait"
	case PathClose:
		return "close"
	case PathList:
		return "list"
	default:
		return path
	}
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Name *string `json:"name"`
	}
	if !s.decode(w, r, "create", "", start, &req) {
		return
	}
	if req.Name == nil {
		s.finish(w, "create", "", start, refused(ReasonNameRequired), nil)
		return
	}
	res, err := s.svc.Create(r.Context(), *req.Name)
	s.finish(w, "create", *req.Name, start, res, err)
}

func (s *server) handlePublish(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Conversation   *string         `json:"conversation"`
		From           *string         `json:"from"`
		To             json.RawMessage `json:"to"`
		Body           *string         `json:"body"`
		IdempotencyKey *string         `json:"idempotency_key"`
	}
	if !s.decode(w, r, "publish", "", start, &req) {
		return
	}
	in := PublishInput{BodyMissing: req.Body == nil}
	if req.Conversation != nil {
		in.Conversation = *req.Conversation
	}
	if req.From != nil {
		in.From = *req.From
	}
	if req.Body != nil {
		in.Body = *req.Body
	}
	if req.IdempotencyKey != nil {
		in.Key = *req.IdempotencyKey
	}
	names, err := parseTo(req.To)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "publish", in.Conversation, start, res, rerr)
		return
	}
	in.To = names
	res, err := s.svc.Publish(r.Context(), in)
	s.finish(w, "publish", in.Conversation, start, res, err)
}

func (s *server) handleRead(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Conversation *string `json:"conversation"`
		Cursor       *int64  `json:"cursor"`
		Limit        *int64  `json:"limit"`
		Name         *string `json:"name"`
	}
	if !s.decode(w, r, "read", "", start, &req) {
		return
	}
	conv := deref(req.Conversation)
	if req.Cursor == nil {
		s.finish(w, "read", conv, start, refused(ReasonCursorRequired), nil)
		return
	}
	if req.Limit == nil {
		s.finish(w, "read", conv, start, refused(ReasonLimitRequired), nil)
		return
	}
	if *req.Limit > math.MaxInt {
		s.finish(w, "read", conv, start, refused(ReasonLimitRange), nil)
		return
	}
	name, hasName := optionalName(req.Name)
	res, err := s.svc.Read(r.Context(), conv, *req.Cursor, int(*req.Limit), name, hasName)
	s.finish(w, "read", conv, start, res, err)
}

func (s *server) handleWait(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Conversation *string         `json:"conversation"`
		Cursor       *int64          `json:"cursor"`
		Name         *string         `json:"name"`
		Deadline     json.RawMessage `json:"deadline"`
	}
	if !s.decode(w, r, "wait", "", start, &req) {
		return
	}
	conv := deref(req.Conversation)
	if req.Cursor == nil {
		s.finish(w, "wait", conv, start, refused(ReasonCursorRequired), nil)
		return
	}
	limit, err := parseDeadline(req.Deadline)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "wait", conv, start, res, rerr)
		return
	}
	name, hasName := optionalName(req.Name)
	res, err := s.svc.Wait(r.Context(), conv, *req.Cursor, name, hasName, limit)
	s.finish(w, "wait", conv, start, res, err)
}

func (s *server) handleClose(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Conversation *string `json:"conversation"`
	}
	if !s.decode(w, r, "close", "", start, &req) {
		return
	}
	conv := deref(req.Conversation)
	res, err := s.svc.CloseConversation(r.Context(), conv)
	s.finish(w, "close", conv, start, res, err)
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct{}
	if !s.decode(w, r, "list", "", start, &req) {
		return
	}
	res, err := s.svc.List(r.Context())
	s.finish(w, "list", "", start, res, err)
}

func (s *server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	s.finish(w, r.URL.Path, "", time.Now(), refused(ReasonUnknownOp), nil)
}

func (s *server) decode(w http.ResponseWriter, r *http.Request, op, conv string, start time.Time, dst any) bool {
	raw, err := readPayload(w, r, s.svc.maxBody)
	if err != nil {
		if errors.Is(err, ErrDropped) {
			s.finish(w, op, conv, start, Result{}, err)
			return false
		}
		res, rerr := asRefused(err)
		if rerr != nil {
			s.finish(w, op, conv, start, Result{}, rerr)
			return false
		}
		s.finish(w, op, conv, start, res, nil)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dst); err != nil || dec.More() {
		s.finish(w, op, conv, start, refused(ReasonBadJSON), nil)
		return false
	}
	return true
}

func readPayload(w http.ResponseWriter, r *http.Request, maxBody int) (raw []byte, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, requestLimit(maxBody))
	defer func() {
		cerr := r.Body.Close()
		if err == nil && cerr != nil {
			err = cerr
		}
	}()
	raw, err = io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, rule(ReasonRequestSize)
		}
		if errors.Is(err, context.Canceled) {
			return nil, ErrDropped
		}
		return nil, rule(ReasonBadJSON)
	}
	if !utf8.Valid(raw) {
		return nil, rule(ReasonRequestUTF8)
	}
	return raw, nil
}

func requestLimit(maxBody int) int64 {
	if maxBody < 1 {
		maxBody = DefaultMaxBody
	}
	// A JSON string encodes one body byte as at most six bytes (\u00XX).
	// requestOverhead is the envelope and the idempotency key.
	const jsonEscape = 6
	if int64(maxBody) > (math.MaxInt64-requestOverhead)/jsonEscape {
		return math.MaxInt64
	}
	return int64(maxBody)*jsonEscape + requestOverhead
}

// optionalName reports a participant name. Nil, including JSON null, means the
// caller did not name one. An empty string is present and fails the name rule.
func optionalName(name *string) (string, bool) {
	if name == nil {
		return "", false
	}
	return *name, true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *server) finish(w http.ResponseWriter, op, conv string, start time.Time, res Result, err error) {
	if errors.Is(err, ErrDropped) {
		s.svc.log.Info("call", "op", op, "conversation", conv, "outcome", "dropped", "duration", time.Since(start))
		abort(w)
		return
	}
	if err != nil {
		s.svc.log.Error("call", "op", op, "conversation", conv, "err", err, "duration", time.Since(start))
		res = Result{Outcome: OutcomeUnavailable, Reason: ReasonUnavailable}
	}
	attrs := []any{"op", op, "conversation", conv, "outcome", res.Outcome, "duration", time.Since(start)}
	if res.Reason != "" {
		attrs = append(attrs, "reason", res.Reason)
	}
	if res.Message != nil {
		attrs = append(attrs, "seq", res.Message.Seq, "already_stored", res.AlreadyStored)
	}
	if op == "create" {
		attrs = append(attrs, "already_existed", res.AlreadyExisted)
	}
	if res.MatchSeq != 0 {
		attrs = append(attrs, "match_seq", res.MatchSeq)
	}
	if res.Outcome == OutcomeUnavailable {
		s.svc.log.Error("call", attrs...)
	} else {
		s.svc.log.Info("call", attrs...)
	}
	if werr := writeJSON(w, toWire(op, res)); werr != nil {
		s.svc.log.Error("write response", "op", op, "err", werr)
	}
}

func abort(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if err := conn.Close(); err != nil {
		return
	}
}

type wire struct {
	Outcome        string              `json:"outcome"`
	Reason         string              `json:"reason,omitempty"`
	AlreadyStored  *bool               `json:"already_stored,omitempty"`
	AlreadyExisted *bool               `json:"already_existed,omitempty"`
	MatchSeq       *int64              `json:"match_seq,omitempty"`
	Message        *wireMessage        `json:"message,omitempty"`
	Messages       *[]wireMessage      `json:"messages,omitempty"`
	Conversation   *wireConversation   `json:"conversation,omitempty"`
	Conversations  *[]wireConversation `json:"conversations,omitempty"`
}

type wireMessage struct {
	Seq  int64    `json:"seq"`
	Time string   `json:"time"`
	From string   `json:"from"`
	To   []string `json:"to"`
	Body string   `json:"body"`
	Key  string   `json:"idempotency_key"`
}

type wireConversation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func toWire(op string, res Result) wire {
	out := wire{Outcome: res.Outcome, Reason: res.Reason}
	switch res.Outcome {
	case OutcomeOK:
		switch op {
		case "publish":
			out.Message = wireMsg(res.Message)
			already := res.AlreadyStored
			out.AlreadyStored = &already
			out.Conversation = wireConv(res.Conversation)
		case "create":
			out.Conversation = wireConv(res.Conversation)
			existed := res.AlreadyExisted
			out.AlreadyExisted = &existed
		case "close":
			out.Conversation = wireConv(res.Conversation)
		case "list":
			cs := wireConvs(res.Conversations)
			out.Conversations = &cs
		case "read", "wait":
			ms := wireMsgs(res.Messages)
			out.Messages = &ms
			if op == "wait" && res.MatchSeq != 0 {
				seq := res.MatchSeq
				out.MatchSeq = &seq
			}
		}
	}
	return out
}

func wireMsg(m *store.Message) *wireMessage {
	if m == nil {
		return nil
	}
	to := m.To
	if to == nil {
		to = []string{}
	}
	return &wireMessage{
		Seq:  m.Seq,
		Time: m.Time,
		From: m.From,
		To:   to,
		Body: m.Body,
		Key:  m.Key,
	}
}

func wireMsgs(in []store.Message) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	for i := range in {
		msg := wireMsg(&in[i])
		out = append(out, *msg)
	}
	return out
}

func wireConv(c *store.Conversation) *wireConversation {
	if c == nil {
		return nil
	}
	return &wireConversation{Name: c.Name, Status: c.Status}
}

func wireConvs(in []store.Conversation) []wireConversation {
	out := make([]wireConversation, 0, len(in))
	for _, c := range in {
		out = append(out, wireConversation{Name: c.Name, Status: c.Status})
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
