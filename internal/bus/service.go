// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

// ErrDropped means the caller went away or the process is stopping.
// The store is unchanged. This is not a protocol outcome.
var ErrDropped = errors.New("connection dropped")

// PublishInput is one publish after the JSON object has been decoded.
// BodyMissing distinguishes an absent body from an empty one.
type PublishInput struct {
	Conversation string
	From         string
	To           []string
	Body         string
	BodyMissing  bool
	TxID         string
	Kind         string
}

// Result is one completed protocol call.
type Result struct {
	Outcome        string
	Reason         string
	AlreadyStored  bool
	AlreadyExisted bool
	MatchSeq       int64
	Message        *store.Message
	Messages       []store.Message
	Conversation   *store.Conversation
	Conversations  []store.Conversation
	Member         *store.Member
	Members        []store.Member
	Session        *store.MemberSession
	Sessions       []store.MemberSession
}

type waiter struct {
	wake chan struct{}
}

// Service applies the bus rules and wakes blocked waits.
type Service struct {
	store   *store.Store
	maxBody int
	clock   func() time.Time
	log     *slog.Logger
	onBlock func(conversation, name string)
	schema  func(context.Context) (string, error)
	mu      sync.Mutex
	waiters map[string][]*waiter
	cancel  context.CancelFunc
	done    <-chan struct{}
}

func newService(st *store.Store, opt Options) *Service {
	if opt.MaxBody < 1 {
		opt.MaxBody = DefaultMaxBody
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	if opt.Clock == nil {
		opt.Clock = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		store:   st,
		maxBody: opt.MaxBody,
		clock:   opt.Clock,
		log:     opt.Logger,
		onBlock: opt.OnBlock,
		schema:  opt.schema,
		waiters: make(map[string][]*waiter),
		cancel:  cancel,
		done:    ctx.Done(),
	}
}

func (s *Service) stop() { s.cancel() }

func (s *Service) Create(ctx context.Context, name string, b store.Binding) (Result, error) {
	if name == "" {
		return refused(ReasonNameRequired), nil
	}
	if !validName(name) {
		return refused(ReasonBadName), nil
	}
	if err := validateBinding(b); err != nil {
		return asRefused(err)
	}
	conv, already, err := s.store.Create(ctx, name, b)
	if err != nil {
		return s.fail(err, "create")
	}
	return Result{
		Outcome:        OutcomeOK,
		AlreadyExisted: already,
		Conversation:   &conv,
	}, nil
}

func (s *Service) Publish(ctx context.Context, in PublishInput) (Result, error) {
	if err := validatePublish(in, s.maxBody); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	msg, conv, already, err := s.store.Publish(ctx, store.Publish{
		Conversation: in.Conversation,
		From:         in.From,
		To:           append([]string{}, in.To...),
		Body:         in.Body,
		TxID:         in.TxID,
		Kind:         in.Kind,
		Time:         s.clock().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return s.fail(err, "publish")
	}
	if !already {
		s.notify(in.Conversation)
	}
	return Result{
		Outcome:       OutcomeOK,
		AlreadyStored: already,
		Message:       &msg,
		Conversation:  &conv,
	}, nil
}

func (s *Service) Read(ctx context.Context, conv string, cursor int64, limit int, name string, hasName bool, kinds []string, hasKinds bool) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	if cursor < 0 {
		return refused(ReasonCursorRange), nil
	}
	if limit < 1 {
		return refused(ReasonLimitRange), nil
	}
	if hasName {
		if err := requireParticipant(name); err != nil {
			return asRefused(err)
		}
	}
	if err := validateKinds(kinds, hasKinds); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]store.Message, 0)
	_, err := s.store.Scan(ctx, conv, cursor, func(m store.Message) bool {
		keep, more := nextRead(m, name, hasName, kinds, hasKinds, len(msgs), limit)
		if keep {
			msgs = append(msgs, m)
		}
		return more
	})
	if err != nil {
		return s.fail(err, "read")
	}
	return Result{Outcome: OutcomeOK, Messages: msgs}, nil
}

func (s *Service) CloseConversation(ctx context.Context, name string) (Result, error) {
	if err := requireConversation(name); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, err := s.store.CloseConversation(ctx, name)
	if err != nil {
		return s.fail(err, "close")
	}
	// No message was appended, so a blocked wait has nothing new to match.
	return Result{
		Outcome:      OutcomeOK,
		Conversation: &conv,
	}, nil
}

func (s *Service) List(ctx context.Context) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	convs, err := s.store.List(ctx)
	if err != nil {
		return s.fail(err, "list")
	}
	if convs == nil {
		convs = []store.Conversation{}
	}
	return Result{Outcome: OutcomeOK, Conversations: convs}, nil
}

const (
	actDrop = iota
	actWake
	actTimer
)

// Wait blocks until a match is stored, the deadline passes, or ctx ends.
// The waiter is registered before the transcript is read, under the same lock
// as publish, so a match cannot land unseen between the read and the wait.
func (s *Service) Wait(ctx context.Context, conv string, cursor int64, name string, hasName bool, kinds []string, hasKinds bool, limit waitLimit) (Result, error) {
	if err := requireConversation(conv); err != nil {
		return asRefused(err)
	}
	if cursor < 0 {
		return refused(ReasonCursorRange), nil
	}
	if hasName {
		if err := requireParticipant(name); err != nil {
			return asRefused(err)
		}
	}
	if err := validateKinds(kinds, hasKinds); err != nil {
		return asRefused(err)
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	if limit.set {
		// An absolute end is turned into a remainder once, on this clock.
		// A retry that sends the same end time keeps what is left.
		timer = time.NewTimer(limit.remaining(s.clock()))
		timerC = timer.C
		defer timer.Stop()
	}

	w := &waiter{wake: make(chan struct{}, 1)}
	s.mu.Lock()
	held := true
	defer func() {
		if !held {
			s.mu.Lock()
		}
		s.remove(conv, w)
		s.mu.Unlock()
		held = false
	}()
	s.add(conv, w)

	for {
		res, pending, err := s.eval(ctx, conv, cursor, name, hasName, kinds, hasKinds)
		if err != nil || !pending {
			return res, err
		}
		s.mu.Unlock()
		held = false
		s.log.Debug("wait blocking", "conversation", conv, "name", name)
		if s.onBlock != nil {
			s.onBlock(conv, name)
		}
		action := actWake
		select {
		case <-ctx.Done():
			action = actDrop
		case <-s.done:
			action = actDrop
		case <-w.wake:
			action = actWake
		case <-timerC:
			action = actTimer
		}
		s.mu.Lock()
		held = true
		if action == actDrop {
			return Result{}, ErrDropped
		}
		if action == actWake {
			continue
		}
		res, pending, err = s.eval(ctx, conv, cursor, name, hasName, kinds, hasKinds)
		if err != nil || !pending {
			return res, err
		}
		return Result{Outcome: OutcomeTimeout}, nil
	}
}

func (s *Service) eval(ctx context.Context, conv string, cursor int64, name string, hasName bool, kinds []string, hasKinds bool) (Result, bool, error) {
	msgs := make([]store.Message, 0)
	_, err := s.store.Scan(ctx, conv, cursor, func(m store.Message) bool {
		if !hasName {
			if !kindListed(m.Kind, kinds, hasKinds) {
				return true
			}
			msgs = append(msgs, m)
			return false
		}
		msgs = append(msgs, m)
		// Rows after the match are not part of the span.
		return !addressed(m, name) || !kindListed(m.Kind, kinds, hasKinds)
	})
	if err != nil {
		res, ferr := s.fail(err, "wait")
		return res, false, ferr
	}
	res, pending := decideWait(msgs, name, hasName, kinds, hasKinds)
	return res, pending, nil
}

func (s *Service) fail(err error, op string) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{}, ErrDropped
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return refused(ReasonNotFound), nil
	case errors.Is(err, store.ErrConflict):
		return refused(ReasonKeyConflict), nil
	case errors.Is(err, store.ErrNotRoster):
		return refused(ReasonNotInRoster), nil
	case errors.Is(err, store.ErrNotMember):
		return refused(ReasonMemberMissing), nil
	case errors.Is(err, store.ErrLaunched):
		return refused(ReasonAlreadyLaunched), nil
	default:
		s.log.Error("store", "op", op, "err", err)
		return Result{Outcome: OutcomeUnavailable, Reason: ReasonUnavailable}, nil
	}
}

func (s *Service) notify(conv string) {
	for _, w := range s.waiters[conv] {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) add(conv string, w *waiter) {
	s.waiters[conv] = append(s.waiters[conv], w)
}

func (s *Service) remove(conv string, w *waiter) {
	ws := s.waiters[conv]
	for i, cur := range ws {
		if cur != w {
			continue
		}
		s.waiters[conv] = append(ws[:i], ws[i+1:]...)
		break
	}
	if len(s.waiters[conv]) == 0 {
		delete(s.waiters, conv)
	}
}

func refused(reason string) Result {
	return Result{Outcome: OutcomeRefused, Reason: reason}
}

func asRefused(err error) (Result, error) {
	if reason, ok := reasonOf(err); ok {
		return refused(reason), nil
	}
	return Result{}, err
}
