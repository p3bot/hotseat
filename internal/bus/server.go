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
	"path/filepath"
	"strings"
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
	// Tests use it to send only after the waiter is parked.
	OnBlock func(chat, name string)
	// Token is the shared capability secret for this listener.
	// Empty means the listener does not require one. A non-empty token is
	// required on every operation and is not logged or stored.
	// GET /health does not read it.
	Token string
	// schema, when set, replaces the meta read. Nil reads the open store.
	schema func(context.Context) (string, error)
}

// Serve accepts connections on ln until ctx is cancelled.
// Cancelling ctx stops the process the way a signal does: blocked waits end
// as dropped connections and the store is left unchanged. st stays open.
func Serve(ctx context.Context, ln net.Listener, st *store.Store, opt Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	svc, err := newService(ctx, st, opt)
	if err != nil {
		return errors.Join(err, ln.Close())
	}
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
	mux.HandleFunc("POST "+PathSend, s.handleSend)
	mux.HandleFunc("POST "+PathRead, s.handleRead)
	mux.HandleFunc("POST "+PathWait, s.handleWait)
	mux.HandleFunc("POST "+PathWatch, s.handleWatch)
	mux.HandleFunc("POST "+PathClose, s.handleClose)
	mux.HandleFunc("POST "+PathList, s.handleList)
	mux.HandleFunc("POST "+PathMessages, s.handleMessages)
	mux.HandleFunc("POST "+PathJoin, s.handleJoin)
	mux.HandleFunc("POST "+PathMember, s.handleMember)
	mux.HandleFunc("POST "+PathSession, s.handleSession)
	mux.HandleFunc("POST "+PathMembers, s.handleMembers)
	mux.HandleFunc("POST "+PathRecord, s.handleRecord)
	mux.HandleFunc("POST "+PathRecords, s.handleRecords)
	mux.HandleFunc("POST "+PathLease, s.handleLease)
	mux.HandleFunc("/", s.handleUnknown)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before the token check, so a missing credential never sees this route.
		if r.Method == http.MethodGet && r.URL.Path == PathHealth {
			s.handleHealth(w, r)
			return
		}
		if s.token != "" {
			// Before the operation, so a refusal cannot block in wait or write.
			switch checkToken(r.Header, s.token) {
			case tokenOK:
			case tokenMissing:
				s.finish(w, opName(r.URL.Path), "", time.Now(), refused(ReasonTokenRequired), nil)
				return
			default:
				s.finish(w, opName(r.URL.Path), "", time.Now(), refused(ReasonTokenRejected), nil)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (svc *Service) readSchema(ctx context.Context) (string, error) {
	if svc.schema != nil {
		return svc.schema(ctx)
	}
	return svc.store.Schema(ctx)
}

func healthErrText(err error, st *store.Store, token string) string {
	msg := err.Error()
	var hidden []string
	if st != nil {
		if path := st.Path(); path != "" {
			hidden = append(hidden, path)
			if dir := filepath.Dir(path); dir != "" && dir != "." && dir != "/" {
				hidden = append(hidden, dir)
			}
		}
	}
	if token != "" {
		hidden = append(hidden, token)
	}
	for _, secret := range hidden {
		if strings.Contains(msg, secret) {
			return "schema"
		}
	}
	return msg
}

func healthDecision(version string, err error) (code int, word string) {
	if err == nil && version == store.SchemaVersion {
		return http.StatusOK, "pass"
	}
	return http.StatusServiceUnavailable, "fail"
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	version, err := s.svc.readSchema(r.Context())
	// The caller leaving is not a schema result. A cancelled read would otherwise be fail.
	if r.Context().Err() != nil {
		s.svc.log.Info("call", "op", "health", "outcome", "dropped", "duration", time.Since(start))
		abort(w)
		return
	}
	code, word := healthDecision(version, err)
	if err != nil {
		// A driver error can name the database or the token. Keep the text only when it does not.
		s.svc.log.Error("call", "op", "health", "outcome", word, "err", healthErrText(err, s.svc.store, s.token), "duration", time.Since(start))
	} else {
		s.svc.log.Info("call", "op", "health", "outcome", word, "duration", time.Since(start))
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	if _, werr := io.WriteString(w, word+"\n"); werr != nil {
		s.svc.log.Error("write response", "op", "health", "err", werr)
	}
}

func opName(path string) string {
	switch path {
	case PathCreate:
		return "create"
	case PathSend:
		return "send"
	case PathRead:
		return "read"
	case PathWait:
		return "wait"
	case PathWatch:
		return "watch"
	case PathClose:
		return "close"
	case PathList:
		return "list"
	case PathMessages:
		return "messages"
	case PathJoin:
		return "join"
	case PathMember:
		return "member"
	case PathSession:
		return "session"
	case PathMembers:
		return "members"
	case PathRecord:
		return "record"
	case PathRecords:
		return "records"
	case PathLease:
		return "lease"
	default:
		return path
	}
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Name       *string         `json:"name"`
		Task       string          `json:"task"`
		Ticket     string          `json:"ticket"`
		Seat       string          `json:"seat"`
		Directory  string          `json:"directory"`
		Prompts    json.RawMessage `json:"prompts"`
		Custom     string          `json:"custom"`
		Roster     json.RawMessage `json:"roster"`
		Permission string          `json:"permission"`
	}
	if !s.decode(w, r, "create", "", start, &req) {
		return
	}
	if req.Name == nil {
		s.finish(w, "create", "", start, refused(ReasonNameRequired), nil)
		return
	}
	prompts, err := parsePrompts(req.Prompts)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "create", *req.Name, start, res, rerr)
		return
	}
	roster, err := parseRoster(req.Roster)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "create", *req.Name, start, res, rerr)
		return
	}
	res, err := s.svc.Create(r.Context(), *req.Name, store.Binding{
		Task:       req.Task,
		Ticket:     req.Ticket,
		Seat:       req.Seat,
		Directory:  req.Directory,
		Prompts:    prompts,
		Custom:     req.Custom,
		Roster:     roster,
		Permission: req.Permission,
	})
	s.finish(w, "create", *req.Name, start, res, err)
}

func (s *server) handleSend(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat   *string         `json:"chat"`
		From   *string         `json:"from"`
		To     json.RawMessage `json:"to"`
		Body   *string         `json:"body"`
		TxID   *string         `json:"txid"`
		Kind   *string         `json:"kind"`
		Re     json.RawMessage `json:"re"`
		Holder *string         `json:"holder"`
	}
	if !s.decode(w, r, "send", "", start, &req) {
		return
	}
	in := SendInput{BodyMissing: req.Body == nil, Holder: req.Holder}
	if req.Chat != nil {
		in.Chat = *req.Chat
	}
	if req.From != nil {
		in.From = *req.From
	}
	if req.Body != nil {
		in.Body = *req.Body
	}
	if req.TxID != nil {
		in.TxID = *req.TxID
	}
	if req.Kind != nil {
		in.Kind = *req.Kind
	}
	names, err := parseTo(req.To)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "send", in.Chat, start, res, rerr)
		return
	}
	in.To = names
	re, hasRe, err := parsePositive(req.Re, ReasonReRange)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "send", in.Chat, start, res, rerr)
		return
	}
	if hasRe {
		in.Re = &re
	}
	res, err := s.svc.Send(r.Context(), in)
	s.finish(w, "send", in.Chat, start, res, err)
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Limit  *int64 `json:"limit"`
		Before *int64 `json:"before"`
	}
	if !s.decode(w, r, "messages", "", start, &req) {
		return
	}
	if req.Limit == nil {
		s.finish(w, "messages", "", start, refused(ReasonLimitRequired), nil)
		return
	}
	if *req.Limit > math.MaxInt {
		s.finish(w, "messages", "", start, refused(ReasonLimitRange), nil)
		return
	}
	res, err := s.svc.Messages(r.Context(), int(*req.Limit), req.Before)
	s.finish(w, "messages", "", start, res, err)
}

func (s *server) handleRead(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat   *string         `json:"chat"`
		Cursor *int64          `json:"cursor"`
		Limit  *int64          `json:"limit"`
		Name   *string         `json:"name"`
		Kinds  json.RawMessage `json:"kinds"`
	}
	if !s.decode(w, r, "read", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	if req.Cursor == nil {
		s.finish(w, "read", chat, start, refused(ReasonCursorRequired), nil)
		return
	}
	if req.Limit == nil {
		s.finish(w, "read", chat, start, refused(ReasonLimitRequired), nil)
		return
	}
	if *req.Limit > MaxReadLimit {
		s.finish(w, "read", chat, start, refused(ReasonLimitCap), nil)
		return
	}
	name, hasName := optionalName(req.Name)
	kinds, hasKinds, err := parseKinds(req.Kinds)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "read", chat, start, res, rerr)
		return
	}
	res, err := s.svc.Read(r.Context(), chat, *req.Cursor, int(*req.Limit), name, hasName, kinds, hasKinds)
	s.finish(w, "read", chat, start, res, err)
}

func (s *server) handleWait(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat     *string         `json:"chat"`
		Cursor   *int64          `json:"cursor"`
		Name     *string         `json:"name"`
		Deadline json.RawMessage `json:"deadline"`
		Kinds    json.RawMessage `json:"kinds"`
	}
	if !s.decode(w, r, "wait", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	if req.Cursor == nil {
		s.finish(w, "wait", chat, start, refused(ReasonCursorRequired), nil)
		return
	}
	limit, err := parseDeadline(req.Deadline)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "wait", chat, start, res, rerr)
		return
	}
	name, hasName := optionalName(req.Name)
	kinds, hasKinds, err := parseKinds(req.Kinds)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "wait", chat, start, res, rerr)
		return
	}
	res, err := s.svc.Wait(r.Context(), chat, *req.Cursor, name, hasName, kinds, hasKinds, limit)
	s.finish(w, "wait", chat, start, res, err)
}

func (s *server) handleWatch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat     *string         `json:"chat"`
		Cursor   *int64          `json:"cursor"`
		Rev      *uint64         `json:"rev"`
		Limit    *int64          `json:"limit"`
		Deadline json.RawMessage `json:"deadline"`
	}
	if !s.decode(w, r, "watch", "", start, &req) {
		return
	}
	q := watchQuery{
		chat:      deref(req.Chat),
		hasChat:   req.Chat != nil,
		cursor:    derefInt(req.Cursor),
		hasCursor: req.Cursor != nil,
		limit:     derefInt(req.Limit),
		hasLimit:  req.Limit != nil,
		hasRev:    req.Rev != nil,
	}
	if req.Rev != nil {
		q.rev = *req.Rev
	}
	deadline, err := parseDeadline(req.Deadline)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "watch", q.chat, start, res, rerr)
		return
	}
	q.deadline = deadline
	res, err := s.svc.Watch(r.Context(), q)
	s.finish(w, "watch", q.chat, start, res, err)
}

func (s *server) handleClose(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat *string `json:"chat"`
	}
	if !s.decode(w, r, "close", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	res, err := s.svc.CloseChat(r.Context(), chat)
	s.finish(w, "close", chat, start, res, err)
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

func (s *server) handleJoin(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat *string `json:"chat"`
		Name *string `json:"name"`
	}
	if !s.decode(w, r, "join", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	if req.Name == nil {
		s.finish(w, "join", chat, start, refused(ReasonNameRequired), nil)
		return
	}
	res, err := s.svc.Join(r.Context(), chat, *req.Name)
	s.finish(w, "join", chat, start, res, err)
}

func (s *server) handleMember(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat   *string         `json:"chat"`
		Name   *string         `json:"name"`
		Status *string         `json:"status"`
		PID    json.RawMessage `json:"pid"`
		Start  *string         `json:"start"`
		Holder *string         `json:"holder"`
	}
	if !s.decode(w, r, "member", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	if req.Name == nil {
		s.finish(w, "member", chat, start, refused(ReasonNameRequired), nil)
		return
	}
	pid, hasPID, err := parsePositive(req.PID, ReasonPID)
	if err != nil {
		res, rerr := asRefused(err)
		s.finish(w, "member", chat, start, res, rerr)
		return
	}
	res, err := s.svc.Member(r.Context(), chat, *req.Name, req.Status, pid, hasPID, req.Start, req.Holder)
	s.finish(w, "member", chat, start, res, err)
}

func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat    *string `json:"chat"`
		Name    *string `json:"name"`
		Session *string `json:"session"`
		Holder  *string `json:"holder"`
	}
	if !s.decode(w, r, "session", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	name, hasName := optionalName(req.Name)
	session, hasSession := optionalName(req.Session)
	res, err := s.svc.Session(r.Context(), chat, name, hasName, session, hasSession, req.Holder)
	s.finish(w, "session", chat, start, res, err)
}

func (s *server) handleMembers(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat *string `json:"chat"`
	}
	if !s.decode(w, r, "members", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	res, err := s.svc.Members(r.Context(), chat)
	s.finish(w, "members", chat, start, res, err)
}

func (s *server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	s.finish(w, r.URL.Path, "", time.Now(), refused(ReasonUnknownOp), nil)
}

func (s *server) decode(w http.ResponseWriter, r *http.Request, op, chat string, start time.Time, dst any) bool {
	raw, err := readPayload(w, r, s.svc.maxBody)
	if err != nil {
		if errors.Is(err, ErrDropped) {
			s.finish(w, op, chat, start, Result{}, err)
			return false
		}
		res, rerr := asRefused(err)
		if rerr != nil {
			s.finish(w, op, chat, start, Result{}, rerr)
			return false
		}
		s.finish(w, op, chat, start, res, nil)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dst); err != nil || dec.More() {
		s.finish(w, op, chat, start, refused(ReasonBadJSON), nil)
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
	// requestOverhead is the envelope and the transaction id.
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

func derefInt(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *server) finish(w http.ResponseWriter, op, chat string, start time.Time, res Result, err error) {
	if errors.Is(err, ErrDropped) {
		s.svc.log.Info("call", "op", op, "chat", chat, "outcome", "dropped", "duration", time.Since(start))
		abort(w)
		return
	}
	if err != nil {
		s.svc.log.Error("call", "op", op, "chat", chat, "err", err, "duration", time.Since(start))
		res = Result{Outcome: OutcomeUnavailable, Reason: ReasonUnavailable}
	}
	attrs := []any{"op", op, "chat", chat, "outcome", res.Outcome, "duration", time.Since(start)}
	if res.Reason != "" {
		attrs = append(attrs, "reason", res.Reason)
	}
	if res.Message != nil {
		attrs = append(attrs, "seq", res.Message.Seq, "already_stored", res.AlreadyStored)
	}
	// Record content stays out of the log. Whether a write was a replay is outcome.
	if op == "record" && res.Outcome == OutcomeOK {
		attrs = append(attrs, "already_stored", res.AlreadyStored)
	}
	if op == "create" {
		attrs = append(attrs, "already_existed", res.AlreadyExisted)
	}
	// The holder is a capability once writes are fenced, so only the selector is logged.
	if op == "lease" && res.Lease != nil {
		attrs = append(attrs, "lease", res.Lease.Op)
	}
	if res.MatchSeq != 0 {
		attrs = append(attrs, "match_seq", res.MatchSeq)
	}
	if op == "watch" && res.Outcome == OutcomeOK {
		attrs = append(attrs, "messages", len(res.Messages), "rev", res.Rev)
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
	Chat           *wireChat           `json:"chat,omitempty"`
	Chats          *[]wireChat         `json:"chats,omitempty"`
	Member         *wireMember         `json:"member,omitempty"`
	Members        *[]wireMemberPublic `json:"members,omitempty"`
	Session        *wireSession        `json:"session,omitempty"`
	Sessions       *[]wireSession      `json:"sessions,omitempty"`
	More           *bool               `json:"more,omitempty"`
	Rev            *uint64             `json:"rev,omitempty"`
	Set            *wireSet            `json:"set,omitempty"`
	Grouping       *wireGrouping       `json:"grouping,omitempty"`
	Settle         *wireSettle         `json:"settle,omitempty"`
	Fix            *wireFix            `json:"fix,omitempty"`
	Halt           *wireHalt           `json:"halt,omitempty"`
	Stop           *wireStop           `json:"stop,omitempty"`
	Sets           *[]wireSet          `json:"sets,omitempty"`
	Groupings      *[]wireGrouping     `json:"groupings,omitempty"`
	Settles        *[]wireSettle       `json:"settles,omitempty"`
	Fixes          *[]wireFix          `json:"fixes,omitempty"`
	Halts          *[]wireHalt         `json:"halts,omitempty"`
	Stops          *[]wireStop         `json:"stops,omitempty"`
	wireLease
}

type wireMessage struct {
	Seq  int64    `json:"seq"`
	Time string   `json:"time"`
	From string   `json:"from"`
	To   []string `json:"to"`
	Body string   `json:"body"`
	TxID string   `json:"txid"`
	Kind string   `json:"kind"`
	Re   *int64   `json:"re"`
	Chat string   `json:"chat,omitempty"`
}

type wireChat struct {
	Name       string       `json:"name"`
	Status     string       `json:"status"`
	Task       string       `json:"task"`
	Ticket     string       `json:"ticket"`
	Seat       string       `json:"seat"`
	Directory  string       `json:"directory"`
	Prompts    []string     `json:"prompts"`
	Custom     string       `json:"custom"`
	Roster     []wireRoster `json:"roster"`
	Permission string       `json:"permission"`
}

// wireRoster is one roster element. module and model are always present, empty when not given.
type wireRoster struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	Model  string `json:"model"`
}

type wireMemberPublic struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Launched string `json:"launched"`
	Joined   string `json:"joined"`
}

// wireMember is a member row. PID and Start are set only on a one-row member read or write.
type wireMember struct {
	wireMemberPublic
	PID   *int64  `json:"pid,omitempty"`
	Start *string `json:"start,omitempty"`
}

type wireSession struct {
	Name    string `json:"name"`
	Session string `json:"session"`
}

func toWire(op string, res Result) wire {
	out := wire{Outcome: res.Outcome, Reason: res.Reason}
	switch res.Outcome {
	case OutcomeOK:
		switch op {
		case "send":
			out.Message = wirePtr(res.Message, toWireMessage)
			already := res.AlreadyStored
			out.AlreadyStored = &already
			out.Chat = wirePtr(res.Chat, toWireChat)
		case "create":
			out.Chat = wirePtr(res.Chat, toWireChat)
			existed := res.AlreadyExisted
			out.AlreadyExisted = &existed
		case "close":
			out.Chat = wirePtr(res.Chat, toWireChat)
		case "list":
			out.Chats = wireList(res.Chats, toWireChat)
		case "messages", "read", "wait":
			out.Messages = wireList(res.Messages, toWireMessage)
			if op == "read" {
				more := res.More
				out.More = &more
			}
			if op == "wait" && res.MatchSeq != 0 {
				seq := res.MatchSeq
				out.MatchSeq = &seq
			}
		case "watch":
			out.Messages = wireList(res.Messages, toWireMessage)
			more, rev := res.More, res.Rev
			out.More = &more
			out.Rev = &rev
		case "join":
			out.Member = wirePtr(res.Member, func(m store.Member) wireMember {
				return wireMember{wireMemberPublic: publicMember(m)}
			})
		case "member":
			out.Member = wirePtr(res.Member, toWireMember)
		case "members":
			out.Members = wireList(res.Members, publicMember)
		case "session":
			out.Session = wirePtr(res.Session, toWireSession)
			if res.Sessions != nil {
				out.Sessions = wireList(res.Sessions, toWireSession)
			}
		case "record", "records":
			recordWire(op, res, &out)
		}
	}
	if op == "lease" {
		leaseWire(res, &out)
	}
	return out
}

func toWireMessage(m store.Message) wireMessage {
	to := m.To
	if to == nil {
		to = []string{}
	}
	return wireMessage{
		Seq:  m.Seq,
		Time: m.Time,
		From: m.From,
		To:   to,
		Body: m.Body,
		TxID: m.TxID,
		Kind: m.Kind,
		Re:   m.Re,
		Chat: m.Chat,
	}
}

func toWireChat(c store.Chat) wireChat {
	prompts := c.Prompts
	if prompts == nil {
		prompts = []string{}
	}
	roster := make([]wireRoster, len(c.Roster))
	for i, r := range c.Roster {
		roster[i] = wireRoster(r)
	}
	return wireChat{
		Name:       c.Name,
		Status:     c.Status,
		Task:       c.Task,
		Ticket:     c.Ticket,
		Seat:       c.Seat,
		Directory:  c.Directory,
		Prompts:    prompts,
		Custom:     c.Custom,
		Roster:     roster,
		Permission: c.Permission,
	}
}

func toWireMember(m store.Member) wireMember {
	out := wireMember{wireMemberPublic: publicMember(m)}
	if m.HasPID {
		pid := m.PID
		out.PID = &pid
	}
	if m.Start != "" {
		start := m.Start
		out.Start = &start
	}
	return out
}

func publicMember(m store.Member) wireMemberPublic {
	return wireMemberPublic{
		Name:     m.Name,
		Status:   m.Status,
		Launched: m.Launched,
		Joined:   m.Joined,
	}
}

func toWireSession(item store.MemberSession) wireSession {
	return wireSession{Name: item.Name, Session: item.Session}
}

// wireList converts every element. The result is empty rather than nil, so JSON carries [].
func wireList[T, W any](in []T, chat func(T) W) *[]W {
	out := make([]W, 0, len(in))
	for _, v := range in {
		out = append(out, chat(v))
	}
	return &out
}

func wirePtr[T, W any](in *T, chat func(T) W) *W {
	if in == nil {
		return nil
	}
	out := chat(*in)
	return &out
}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
