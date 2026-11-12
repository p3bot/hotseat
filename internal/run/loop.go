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
	"math"
	"slices"
	"sync"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func (e *engine) drive() error {
	e.out.line("conversation", e.name)
	var wg sync.WaitGroup
	var err error
	if e.continuing {
		err = e.resume(&wg)
	} else {
		close(e.leftovers)
		e.boot(&wg)
	}
	if err == nil {
		err = e.messages()
	}
	e.cancel()
	wg.Wait()
	e.mu.Lock()
	closed := e.outcome
	e.mu.Unlock()
	if errors.Is(err, errDone) || closed {
		// Members killed by the close write their lines during the wait above.
		if closed {
			return e.reportStop()
		}
		return nil
	}
	return err
}

func (e *engine) boot(wg *sync.WaitGroup) {
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

// resume settles dead leftovers, stops on a stored stopping done, and only then checks the task minimum.
// Settling comes first because whether a backup's done stops the run depends on the seat being timed-out.
// A roster below the minimum leaves a live leftover unwatched and running.
func (e *engine) resume(wg *sync.WaitGroup) error {
	h, err := e.head()
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.historyHead = h
	e.mu.Unlock()
	live, err := e.reapDead()
	if err != nil {
		return err
	}
	msgs, err := e.readFrom(0)
	if err != nil {
		return err
	}
	done, stop, err := e.firstStop(msgs)
	if err != nil {
		return err
	}
	if stop {
		e.watchLeftovers(wg, live)
		return e.finishDone(done)
	}
	if err := e.requireMinimum(e.binding.Task); err != nil {
		return err
	}
	e.watchLeftovers(wg, live)
	return e.enforceMinimum()
}

// watchLeftovers holds each slot before the transcript watch, so a launch cannot pass a live process.
// The continuation barrier ends once every leftover is recorded.
func (e *engine) watchLeftovers(wg *sync.WaitGroup, live []liveProc) {
	var left sync.WaitGroup
	for _, proc := range live {
		sl := e.slot(proc.name)
		sl.mu.Lock()
		e.mu.Lock()
		e.leftoverEnds[proc.name] = math.MaxInt64
		e.mu.Unlock()
		e.addInFlight()
		wg.Add(1)
		left.Add(1)
		go func(proc liveProc, sl *slot) {
			defer wg.Done()
			defer left.Done()
			defer sl.mu.Unlock()
			defer e.doneInFlight()
			err := e.watchLocked(proc)
			e.endLeftover(proc.name)
			if err != nil {
				if herr := e.noteHalt(err); herr != nil {
					return
				}
			}
		}(proc, sl)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		left.Wait()
		close(e.leftovers)
	}()
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
				return e.endJob(job, nil, e.finishDone(got.msg))
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
		case <-e.idle:
			return e.endWindDown(job, eye, cursor, pending)
		}
	}
}

// endWindDown ends the watch once a wind-down has no member process in flight.
// No process can start by then, so the job in hand returns without one. A stopping done stored by then still closes.
func (e *engine) endWindDown(job chan error, eye *busEye, cursor int64, pending []client.Message) error {
	e.dropEye(eye)
	if job != nil {
		if err := <-job; err != nil {
			return err
		}
	}
	if e.shutting() {
		return errDone
	}
	rest, err := e.readFrom(cursor)
	if err != nil {
		return e.busErr(err)
	}
	done, stop, err := e.firstStop(append(pending, rest...))
	if err != nil {
		return err
	}
	if stop {
		return e.finishDone(done)
	}
	return e.windErr()
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
	if historical {
		if halted, err := e.reportAgain(); halted || err != nil {
			return err
		}
	}
	for i, msg := range batch {
		if err := e.stopped(); err != nil {
			return err
		}
		// A wind-down acts only on a stopping done. A turn is not counted and launches nothing.
		if e.windingDown() && msg.Kind != kindDone {
			continue
		}
		switch msg.Kind {
		case kindDone:
			stop, err := e.stops(msg)
			if err != nil {
				return err
			}
			if stop {
				return e.finishDone(msg)
			}
		case kindFinding:
			if err := e.judgeFinding(msg); err != nil {
				return err
			}
		case kindAggregate, kindPass, kindExtend, kindDeadlock, kindPropose, kindDismiss, kindAgree, kindObject, kindStand:
			if err := e.judgeStray(msg); err != nil {
				return err
			}
		case kindTurn:
			resumed, err := e.resumeIfDiscussed(msg, batch[len(batch)-1].Seq)
			if err != nil {
				return err
			}
			if resumed {
				continue
			}
			settled, err := e.turnSettled(msg, batch[i+1:])
			if err != nil {
				return err
			}
			if settled {
				continue
			}
			// Launches run inside actTurn, so a done already in hand must close first.
			done, stop, err := e.firstStop(batch[i+1:])
			if err != nil {
				return err
			}
			if stop {
				return e.finishDone(done)
			}
			if err := e.actTurn(msg, historical, batch[len(batch)-1].Seq, batch[i+1:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// resumeIfDiscussed continues a set whose discuss marker an earlier process stored, and reports whether it did.
func (e *engine) resumeIfDiscussed(turn client.Message, seen int64) (bool, error) {
	if !e.canLaunch() {
		return false, nil
	}
	if _, _, ok := parseMode(turn.Body); !ok {
		return false, nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return false, e.busErr(err)
	}
	if _, ok := l.discussOf(turn.Seq); !ok {
		return false, nil
	}
	return true, e.resumeSet(turn, seen)
}

// turnSettled is the rule for a set with no discuss: every recipient has exited or timed out, the follow-up target
// has a later turn or done, and no recipient published a finding before it. A bootstrap exit is not that proof.
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
		if m.Kind == kindFinding && slices.Contains(recips, m.From) {
			return false, nil
		}
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
	switch e.takeTurn() {
	case turnWindingDown:
		return nil
	case turnCapped:
		if err := e.publishErr("", classTurnCap); err != nil {
			return err
		}
		return e.noteHalt(errors.New(classTurnCap))
	}
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
	due := e.due(msg.To)
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
	return e.finishSet(msg, seen)
}

// stopForNewDone closes when a stopping done was stored after the batch in hand.
func (e *engine) stopForNewDone(seen int64) error {
	msgs, err := e.readFrom(seen)
	if err != nil {
		return err
	}
	done, stop, err := e.firstStop(msgs)
	if err != nil || !stop {
		return err
	}
	return e.finishDone(done)
}

// due is every roster recipient that has not timed out. Whether one already answered is checked at launch.
func (e *engine) due(to []string) []string {
	var due []string
	for _, name := range e.rosterTo(to) {
		if e.localStatus(name) != statusTimedOut {
			due = append(due, name)
		}
	}
	return due
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
		if err := e.launchForTurn(name, msg, e.turnPrompt(name, msg, mode, phase)); err != nil {
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
			if err := e.launchForTurn(name, msg, e.turnPrompt(name, msg, mode, phase)); err != nil {
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
	if e.windingDown() {
		return nil
	}
	if err := e.judgeBacklog(); err != nil {
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
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	var noReply []string
	for _, name := range l.unanswered(msg) {
		if e.localStatus(name) != statusTimedOut {
			noReply = append(noReply, name)
		}
	}
	if err := e.launchMember(target, launchFollow, e.followPrompt(target, l.fixedSince(msg.Seq), noReply, l.droppedOf(msg.Seq))); err != nil {
		return err
	}
	return e.drainHandoff()
}

// firstStop returns the first done in msgs that stops the run.
func (e *engine) firstStop(msgs []client.Message) (client.Message, bool, error) {
	for _, msg := range msgs {
		stop, err := e.stops(msg)
		if err != nil {
			return client.Message{}, false, err
		}
		if stop {
			return msg, true, nil
		}
	}
	return client.Message{}, false, nil
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

// finishDone closes the conversation and stops the run on the stopping done.
// No launch starts once closing is set, and its sender alone keeps running for the grace period.
func (e *engine) finishDone(done client.Message) error {
	e.mu.Lock()
	if e.outcome || e.closing {
		e.mu.Unlock()
		return errDone
	}
	e.closing = true
	e.doneFrom = done.From
	e.doneSeq = done.Seq
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
	return e.readOn(e.child, cursor, nil)
}

// readOn reads every message after cursor, or every message of kinds when kinds is set.
func (e *engine) readOn(ctx context.Context, cursor int64, kinds []string) ([]client.Message, error) {
	var all []client.Message
	req := client.ReadRequest{Conversation: e.name, Limit: 200}
	if kinds != nil {
		req.Kinds = &kinds
	}
	for {
		req.Cursor = cursor
		res, err := e.call(ctx, "read", req)
		if err != nil {
			return nil, err
		}
		if res.Outcome != bus.OutcomeOK {
			return nil, fmt.Errorf("read: %s", res.Reason)
		}
		if res.Messages == nil || len(*res.Messages) == 0 {
			e.noteSeen(cursor)
			return all, nil
		}
		batch := *res.Messages
		all = append(all, batch...)
		cursor = batch[len(batch)-1].Seq
		if int64(len(batch)) < 200 {
			e.noteSeen(cursor)
			return all, nil
		}
	}
}

func (e *engine) noteSeen(seq int64) {
	e.mu.Lock()
	e.seen = max(e.seen, seq)
	e.mu.Unlock()
}

// head is the newest seq in the conversation.
func (e *engine) head() (int64, error) {
	e.mu.Lock()
	from := e.seen
	e.mu.Unlock()
	msgs, err := e.readFrom(from)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return from, nil
	}
	return msgs[len(msgs)-1].Seq, nil
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
