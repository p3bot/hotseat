// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"context"
	"math"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

// watchQuery is one watch after the JSON object has been decoded.
// Each has field is false when the caller omitted that field.
type watchQuery struct {
	conversation    string
	hasConversation bool
	cursor          int64
	hasCursor       bool
	limit           int64
	hasLimit        bool
	rev             uint64
	hasRev          bool
	deadline        waitLimit
}

func (q watchQuery) validate() error {
	if q.hasConversation {
		if err := requireConversation(q.conversation); err != nil {
			return err
		}
	}
	if q.hasCursor && q.cursor < 0 {
		return rule(ReasonCursorRange)
	}
	if q.hasCursor && !q.hasLimit {
		return rule(ReasonLimitRequired)
	}
	if q.hasLimit && (q.limit < 1 || q.limit > math.MaxInt) {
		return rule(ReasonLimitRange)
	}
	return nil
}

// Watch returns messages after the cursor and the current rev.
// It returns at once when a matching message exists, when rev is omitted, or
// when rev differs from the current counter. Otherwise it blocks until one of
// those holds, the deadline passes, ctx ends, or the bus stops. The waiter is registered
// before the store is read, under the same lock as every commit.
func (s *Service) Watch(ctx context.Context, q watchQuery) (Result, error) {
	if err := q.validate(); err != nil {
		return asRefused(err)
	}
	var timerC <-chan time.Time
	if q.deadline.set {
		timer := time.NewTimer(q.deadline.remaining(s.clock()))
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
		s.removeWatch(w)
		s.mu.Unlock()
	}()
	s.addWatch(w)

	for {
		res, pending, err := s.evalWatch(ctx, q)
		if err != nil || !pending {
			return res, err
		}
		s.mu.Unlock()
		held = false
		s.log.Debug("watch blocking", "conversation", q.conversation)
		if s.onBlock != nil {
			s.onBlock(q.conversation, "")
		}
		action := actWake
		select {
		case <-ctx.Done():
			action = actDrop
		case <-s.done:
			action = actDrop
		case <-w.wake:
		case <-timerC:
			action = actTimer
		}
		s.mu.Lock()
		held = true
		switch action {
		case actDrop:
			return Result{}, ErrDropped
		case actTimer:
			res, pending, err = s.evalWatch(ctx, q)
			if err != nil || !pending {
				return res, err
			}
			return Result{Outcome: OutcomeTimeout}, nil
		}
	}
}

// evalWatch reads the watch result. The caller holds s.mu.
func (s *Service) evalWatch(ctx context.Context, q watchQuery) (Result, bool, error) {
	res := Result{Outcome: OutcomeOK, Messages: []store.Message{}, Rev: s.rev}
	if q.hasCursor {
		msgs, more, err := s.store.After(ctx, q.conversation, q.cursor, int(q.limit))
		if err != nil {
			fres, ferr := s.fail(err, "watch")
			return fres, false, ferr
		}
		res.Messages, res.More = msgs, more
	}
	pending := len(res.Messages) == 0 && q.hasRev && q.rev == s.rev
	return res, pending, nil
}
