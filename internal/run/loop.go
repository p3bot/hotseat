// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func (e *engine) drive() error {
	e.out.line("conversation", e.name)
	var wg sync.WaitGroup
	if e.continuing {
		live, err := e.reapDead()
		if err != nil {
			return err
		}
		// The slot is held before the transcript watch, so a launch cannot pass a live process.
		for _, proc := range live {
			sl := e.slot(proc.name)
			sl.mu.Lock()
			wg.Add(1)
			go func(proc liveProc, sl *slot) {
				defer wg.Done()
				defer sl.mu.Unlock()
				if err := e.watchLocked(proc); err != nil {
					if herr := e.noteHalt(err); herr != nil {
						return
					}
				}
			}(proc, sl)
		}
	} else {
		for _, a := range e.roster {
			wg.Add(1)
			go func(a agent) {
				defer wg.Done()
				err := e.bootstrap(a)
				if herr := e.drainHandoff(); herr != nil {
					err = herr
				}
				if err != nil {
					if herr := e.noteHalt(err); herr != nil {
						return
					}
				}
			}(a)
		}
	}
	err := e.messages()
	e.cancel()
	wg.Wait()
	e.mu.Lock()
	closed := e.outcome
	e.mu.Unlock()
	if errors.Is(err, errDone) || closed {
		// Members killed by the close write their lines during the wait above.
		if closed {
			e.out.line("outcome", "done")
		}
		return nil
	}
	return err
}

// messages keeps the transcript watch. Member work runs outside this loop.
func (e *engine) messages() error {
	cursor := int64(0)
	historical := e.continuing
	var pending []client.Message
	var job chan error
	var eye *busEye
	for {
		if err := e.stopped(); err != nil {
			return e.endJob(job, eye, err)
		}
		if job == nil {
			if len(pending) > 0 {
				rest, err := e.readFrom(cursor)
				if err != nil {
					return e.endJob(nil, eye, e.busErr(err))
				}
				if len(rest) > 0 {
					pending = append(pending, rest...)
					cursor = rest[len(rest)-1].Seq
				}
			} else {
				batch, err := e.readFrom(cursor)
				if err != nil {
					return e.endJob(nil, eye, e.busErr(err))
				}
				if len(batch) > 0 {
					cursor = batch[len(batch)-1].Seq
					pending = batch
				}
			}
			if len(pending) > 0 {
				batch := pending
				pending = nil
				hist := historical
				historical = false
				job = e.goJob(func() error { return e.apply(batch, hist) })
				continue
			}
			historical = false
			if e.hasHandoff() {
				job = e.goJob(e.drainHandoff)
				continue
			}
		}
		if eye == nil {
			eye = e.watch(cursor)
		}
		select {
		case got := <-eye.ch:
			eye = nil
			if got.err != nil {
				return e.endJob(job, nil, e.busErr(got.err))
			}
			stop, err := e.stops(got.msg)
			if err != nil {
				return e.endJob(job, nil, err)
			}
			if stop {
				return e.endJob(job, nil, e.finishDone())
			}
			pending = append(pending, got.msg)
			cursor = got.msg.Seq
		case err := <-job:
			job = nil
			e.dropEye(eye)
			eye = nil
			if err != nil {
				return err
			}
		}
	}
}

type busEye struct {
	cancel context.CancelFunc
	ch     chan busHit
}

type busHit struct {
	msg client.Message
	err error
}

func (e *engine) watch(cursor int64) *busEye {
	ctx, cancel := context.WithCancel(e.child)
	eye := &busEye{cancel: cancel, ch: make(chan busHit, 1)}
	go func() {
		msg, err := e.waitOn(ctx, cursor)
		eye.ch <- busHit{msg, err}
	}()
	return eye
}

func (e *engine) dropEye(eye *busEye) {
	if eye == nil {
		return
	}
	eye.cancel()
	<-eye.ch
}

func (e *engine) goJob(fn func() error) chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func (e *engine) endJob(job chan error, eye *busEye, err error) error {
	if eye != nil {
		eye.cancel()
	}
	if job != nil && e.child.Err() == nil {
		e.cancel()
	}
	if job != nil {
		werr := <-job
		if errors.Is(err, errDone) || e.shutting() {
			if eye != nil {
				<-eye.ch
			}
			return errDone
		}
		if werr != nil && !errors.Is(werr, errStopped) && !errors.Is(werr, context.Canceled) {
			err = werr
		}
	}
	if eye != nil {
		<-eye.ch
	}
	return err
}

func (e *engine) busErr(err error) error {
	if e.shutting() {
		return errDone
	}
	if stop := e.stopped(); stop != nil {
		return stop
	}
	return err
}

func (e *engine) apply(batch []client.Message, historical bool) error {
	for i, msg := range batch {
		if err := e.stopped(); err != nil {
			return err
		}
		switch msg.Kind {
		case kindDone:
			stop, err := e.stops(msg)
			if err != nil {
				return err
			}
			if stop {
				return e.finishDone()
			}
		case kindTurn:
			settled, err := e.turnSettled(msg, batch[i+1:])
			if err != nil {
				return err
			}
			if settled {
				continue
			}
			// Launches run inside actTurn, so a done already in hand must close first.
			stop, err := e.laterDone(batch[i+1:])
			if err != nil {
				return err
			}
			if stop {
				return e.finishDone()
			}
			if err := e.actTurn(msg, historical, batch[len(batch)-1].Seq, batch[i+1:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// turnSettled requires a later turn or done from the follow-up target.
// A bootstrap exit is not that proof.
func (e *engine) turnSettled(msg client.Message, later []client.Message) (bool, error) {
	recips := e.rosterTo(msg.To)
	if len(recips) == 0 {
		return false, nil
	}
	for _, name := range recips {
		switch e.localStatus(name) {
		case statusExited, statusTimedOut:
		default:
			return false, nil
		}
	}
	target, err := e.followTarget(msg.From)
	if err != nil {
		return false, err
	}
	if target == "" {
		return false, nil
	}
	for _, m := range later {
		if m.From == target && (m.Kind == kindTurn || m.Kind == kindDone) {
			return true, nil
		}
	}
	return false, nil
}

// badTurnReported is the stored rejection. A later resume does not reject the turn again.
func badTurnReported(later []client.Message) bool {
	for _, m := range later {
		if m.Kind == kindTurn {
			return false
		}
		if m.Kind == kindError && m.From == fromRun && m.Body == classBadTurn {
			return true
		}
	}
	return false
}

func (e *engine) actTurn(msg client.Message, historical bool, seen int64, later []client.Message) error {
	if !e.canLaunch() {
		return nil
	}
	// A live error can be stored after a newer turn. Only the stored transcript is settled.
	if historical && badTurnReported(later) {
		return nil
	}
	if e.actedCount() >= e.maxTurns {
		if err := e.publishErr("", classTurnCap); err != nil {
			return err
		}
		return e.noteHalt(errors.New(classTurnCap))
	}
	e.addActed()
	mode, phase, ok := parseMode(msg.Body)
	if !ok {
		return e.publishErr("", classBadTurn)
	}
	turnLine := make([][2]string, 0, 1+len(msg.To))
	turnLine = append(turnLine, [2]string{"turn", mode})
	for _, name := range msg.To {
		turnLine = append(turnLine, [2]string{"to", name})
	}
	e.out.record(turnLine...)
	for _, name := range msg.To {
		if name != "" && !nameIn(e.roster, name) {
			if err := e.publishErr(name, classBadTurn); err != nil {
				return err
			}
		}
	}
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	if err := e.drainHandoff(); err != nil {
		return err
	}
	due := e.due(msg.To, historical, later)
	var err error
	if mode == modeSerial {
		err = e.runSerial(due, msg, mode, phase, seen)
	} else {
		err = e.runParallel(due, msg, mode, phase, seen)
	}
	if err != nil {
		return err
	}
	if e.stopped() != nil {
		return e.stopped()
	}
	return e.followUp(msg, seen)
}

// stopForNewDone closes when a stopping done was stored after the batch in hand.
func (e *engine) stopForNewDone(seen int64) error {
	msgs, err := e.readFrom(seen)
	if err != nil {
		return err
	}
	for _, msg := range msgs {
		stop, err := e.stops(msg)
		if err != nil {
			return err
		}
		if stop {
			return e.finishDone()
		}
	}
	return nil
}

func (e *engine) due(to []string, historical bool, later []client.Message) []string {
	var due []string
	for _, name := range e.rosterTo(to) {
		st := e.localStatus(name)
		if st == statusTimedOut {
			continue
		}
		// Startup leaves the same exited row as a finished turn.
		if historical && st == statusExited && spokeSince(name, later) {
			continue
		}
		due = append(due, name)
	}
	return due
}

func spokeSince(name string, later []client.Message) bool {
	for _, msg := range later {
		if msg.From == name {
			return true
		}
	}
	return false
}

func (e *engine) rosterTo(to []string) []string {
	var names []string
	for _, name := range to {
		if nameIn(e.roster, name) {
			names = append(names, name)
		}
	}
	return names
}

func (e *engine) runSerial(due []string, msg client.Message, mode, phase string, seen int64) error {
	for _, name := range due {
		if err := e.stopped(); err != nil {
			return err
		}
		if err := e.stopForNewDone(seen); err != nil {
			return err
		}
		if err := e.launchMember(name, launchTurn, e.turnPrompt(name, msg, mode, phase)); err != nil {
			return err
		}
		if err := e.stopForNewDone(seen); err != nil {
			return err
		}
		if err := e.drainHandoff(); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) runParallel(due []string, msg client.Message, mode, phase string, seen int64) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(due))
	for _, name := range due {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if err := e.stopForNewDone(seen); err != nil {
				errCh <- err
				return
			}
			if err := e.launchMember(name, launchTurn, e.turnPrompt(name, msg, mode, phase)); err != nil {
				errCh <- err
				return
			}
			if err := e.stopForNewDone(seen); err != nil {
				errCh <- err
				return
			}
			if err := e.drainHandoff(); err != nil {
				errCh <- err
			}
		}(name)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) followUp(msg client.Message, seen int64) error {
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	if err := e.drainHandoff(); err != nil {
		return err
	}
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	target := msg.From
	if e.localStatus(target) == statusTimedOut || !nameIn(e.roster, target) {
		next, err := e.backup()
		if err != nil {
			return err
		}
		if next == "" {
			if err := e.publishErr("", classNoBackup); err != nil {
				return err
			}
			return e.noteHalt(errors.New(classNoBackup))
		}
		target = next
	}
	if err := e.launchMember(target, launchFollow, e.followPrompt(target)); err != nil {
		return err
	}
	return e.drainHandoff()
}

func (e *engine) laterDone(later []client.Message) (bool, error) {
	for _, msg := range later {
		stop, err := e.stops(msg)
		if err != nil {
			return false, err
		}
		if stop {
			return true, nil
		}
	}
	return false, nil
}

func (e *engine) stops(msg client.Message) (bool, error) {
	if msg.Kind != kindDone {
		return false, nil
	}
	if msg.From == e.seat {
		return true, nil
	}
	if e.localStatus(e.seat) != statusTimedOut {
		return false, nil
	}
	backup, err := e.backup()
	if err != nil {
		return false, err
	}
	return msg.From == backup, nil
}

func (e *engine) finishDone() error {
	e.mu.Lock()
	if e.outcome || e.closing {
		e.mu.Unlock()
		return errDone
	}
	e.closing = true
	e.mu.Unlock()
	res, err := e.call(e.root, "close", client.CloseRequest{Conversation: e.name})
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		return fmt.Errorf("close: %s", res.Reason)
	}
	e.mu.Lock()
	e.outcome = true
	e.mu.Unlock()
	e.cancel()
	return errDone
}

func (e *engine) readFrom(cursor int64) ([]client.Message, error) {
	var all []client.Message
	for {
		res, err := e.call(e.child, "read", client.ReadRequest{
			Conversation: e.name,
			Cursor:       cursor,
			Limit:        200,
		})
		if err != nil {
			return nil, err
		}
		if res.Outcome != bus.OutcomeOK {
			return nil, fmt.Errorf("read: %s", res.Reason)
		}
		if res.Messages == nil || len(*res.Messages) == 0 {
			return all, nil
		}
		batch := *res.Messages
		all = append(all, batch...)
		cursor = batch[len(batch)-1].Seq
		if int64(len(batch)) < 200 {
			return all, nil
		}
	}
}

func (e *engine) waitOn(ctx context.Context, cursor int64) (client.Message, error) {
	res, err := e.call(ctx, "wait", client.WaitRequest{
		Conversation: e.name,
		Cursor:       cursor,
	})
	if err != nil {
		return client.Message{}, err
	}
	if res.Outcome != bus.OutcomeOK {
		return client.Message{}, fmt.Errorf("wait: %s", res.Reason)
	}
	if res.Message != nil {
		return *res.Message, nil
	}
	if res.Messages != nil && len(*res.Messages) > 0 {
		return (*res.Messages)[len(*res.Messages)-1], nil
	}
	return client.Message{}, errors.New("wait returned no message")
}

var errDone = errors.New("done")
