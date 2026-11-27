// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/p3bot/hotseat/internal/client"
)

func (e *engine) drive() error {
	var wg sync.WaitGroup
	err := e.loadRecords()
	if err != nil {
		return err
	}
	if e.continuing {
		err = e.resume(&wg)
	} else {
		close(e.leftovers)
		e.boot(&wg)
	}
	if err == nil {
		err = e.messages()
	}
	err = e.halt(err)
	e.cancel()
	wg.Wait()
	e.mu.Lock()
	closed, halt := e.closed, e.haltErr
	e.mu.Unlock()
	// The first halt is the stop, including one after the close, such as a lost lease, which left the stop unrecorded.
	if halt != nil {
		return halt
	}
	if closed {
		// Members killed by the close write their lines during the wait above.
		return e.reportStop()
	}
	if errors.Is(err, errDone) {
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

// resume settles dead leftovers, stops on the stopping done, and only then checks the task minimum.
// A stop record names that done. With none, the stop rule decides it after settling, because whether a backup's
// done stops the run depends on the seat being timed-out. A roster below the minimum leaves a live leftover
// unwatched and running.
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
	e.noteRuledOut(ruledOutDones(msgs)...)
	done, stop, err := e.firstStop(msgs)
	if err != nil {
		return err
	}
	if stop {
		e.watchLeftovers(wg, live)
		// A settle whose announcement a crash lost is still stated, even though nothing resumes.
		if err := e.announceSettles(); err != nil {
			return err
		}
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
			adopted := time.Now()
			e.events.Info("adopt", "member", proc.name)
			err := e.watchLocked(proc)
			// A process left running, such as on a lost lease, has not exited.
			if !sameLaunch(proc.id) {
				e.exitEvent(proc.name, errNotChild, time.Since(adopted))
			}
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
				return e.halt(err)
			}
		case <-e.idle:
			return e.endWindDown(job, eye, cursor, pending)
		}
	}
}

// endWindDown ends the watch once a wind-down has stored its error and has no member process in flight.
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
	return e.halt(e.windErr())
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

// endJob ends the watch on err. An error stop is noted before the job is cancelled and waited for.
func (e *engine) endJob(job chan error, eye *busEye, err error) error {
	err = e.halt(err)
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
		if err := e.announceSettles(); err != nil {
			return err
		}
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
			resumed, err := e.resumeIfRecorded(msg, batch[len(batch)-1].Seq)
			if err != nil {
				return err
			}
			if resumed {
				continue
			}
			superseded, err := e.turnSuperseded(msg, batch[i+1:])
			if err != nil {
				return err
			}
			if superseded {
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
			if err := e.actTurn(msg, historical, batch[len(batch)-1].Seq); err != nil {
				return err
			}
		}
	}
	return nil
}

// resumeIfRecorded continues a set whose set record an earlier process wrote, and reports whether it did.
func (e *engine) resumeIfRecorded(turn client.Message, seen int64) (bool, error) {
	if _, _, ok := parseMode(turn.Body); !ok {
		return false, nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return false, e.busErr(err)
	}
	if !l.recorded(turn.Seq) {
		return false, nil
	}
	return true, e.resumeSet(turn, seen)
}

// turnSuperseded is the rule for a set with no set record that is skipped: every recipient has exited or timed out,
// the follow-up target has a later turn or done, and the ledger gives the set no finding. A bootstrap exit is not that proof.
func (e *engine) turnSuperseded(msg client.Message, later []client.Message) (bool, error) {
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
	if !slices.ContainsFunc(later, func(m client.Message) bool {
		return m.From == target && (m.Kind == kindTurn || m.Kind == kindDone)
	}) {
		return false, nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return false, e.busErr(err)
	}
	return len(l.findingsOf(msg.Seq)) == 0, nil
}

func (e *engine) actTurn(msg client.Message, historical bool, seen int64) error {
	mode, phase, ok := parseMode(msg.Body)
	// A stored rejected turn never takes a turn from the cap, so a continuation only reports it.
	if !ok && historical {
		return e.rejectTurn(msg)
	}
	switch e.takeTurn() {
	case turnWindingDown:
		return nil
	case turnCapped:
		return e.haltProtocol("", classTurnCap)
	}
	if !ok {
		return e.rejectTurn(msg)
	}
	e.events.Info("turn", "seq", msg.Seq, "mode", mode, "to", strings.Join(msg.To, " "))
	for _, name := range msg.To {
		if name != "" && !nameIn(e.roster, name) {
			if _, _, err := e.storeErr(name, classBadTurn, msg.Seq, factTx(classBadTurn, name, seqText(msg.Seq))); err != nil {
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

// rejectTurn reports a turn whose body run rejects. Its derived transaction id stores one error however often it is read.
func (e *engine) rejectTurn(turn client.Message) error {
	_, _, err := e.storeErr("", classBadTurn, turn.Seq, factTx(classBadTurn, seqText(turn.Seq)))
	return err
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

// runParallel launches each due member at once. One member's error stop halts the run there, which stops its siblings.
func (e *engine) runParallel(due []string, msg client.Message, mode, phase string, seen int64) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(due))
	for _, name := range due {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			errCh <- e.halt(e.parallelLaunch(name, msg, mode, phase, seen))
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

// parallelLaunch is one member's part of a parallel turn.
func (e *engine) parallelLaunch(name string, msg client.Message, mode, phase string, seen int64) error {
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	if err := e.launchForTurn(name, msg, e.turnPrompt(name, msg, mode, phase)); err != nil {
		return err
	}
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	return e.drainHandoff()
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
			return e.haltProtocol("", classNoBackup)
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
	if err := e.launchMember(target, launchFollow, msg.Seq, e.followPrompt(target, l.fixesOf(msg.Seq), noReply, l.droppedOf(msg.Seq))); err != nil {
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

// stops reports whether msg is the done that stops the run. Each done is judged once: once a stop is decided or
// recorded only its done stops, and a done a stored bad done names never does unless the seat sent it. Any other
// done is judged on current member rows, and one ruled out is stored as bad done so the ruling outlives those rows.
// Concurrent callers read member rows a moment apart, so the whole ruling holds the rule lock.
func (e *engine) stops(msg client.Message) (bool, error) {
	if msg.Kind != kindDone {
		return false, nil
	}
	e.rule.Lock()
	defer e.rule.Unlock()
	e.mu.Lock()
	stop, decided := e.recs.stopped()
	if e.doneSeq != 0 {
		stop, decided = client.Stop{Done: e.doneSeq}, true
	}
	ruled := e.ruledOut[msg.Seq]
	e.mu.Unlock()
	if decided {
		return msg.Seq == stop.Done, nil
	}
	if ruled && msg.From != e.seat {
		return false, nil
	}
	ok, err := e.stopsByRows(msg)
	if err != nil {
		return false, err
	}
	if ok {
		e.mu.Lock()
		e.doneSeq = msg.Seq
		e.mu.Unlock()
		return true, nil
	}
	if err := e.factErr(msg.From, classBadDone, msg.Seq, factTx(classBadDone, seqText(msg.Seq))); err != nil {
		return false, err
	}
	e.noteRuledOut(msg.Seq)
	return false, nil
}

// stopsByRows is the stop rule on current member rows: the seat's done, or the backup's while the seat is timed-out.
func (e *engine) stopsByRows(msg client.Message) (bool, error) {
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

func (e *engine) noteRuledOut(seqs ...int64) {
	e.mu.Lock()
	for _, seq := range seqs {
		e.ruledOut[seq] = true
	}
	e.mu.Unlock()
}

// finishDone stores the stop record, then closes the chat and stops the run on the stopping done.
// No launch starts once closing is set, and its sender alone keeps running for the grace period.
func (e *engine) finishDone(done client.Message) error {
	e.mu.Lock()
	if e.closed || e.closing {
		e.mu.Unlock()
		return errDone
	}
	e.closing = true
	e.doneFrom = done.From
	e.doneSeq = done.Seq
	e.mu.Unlock()
	if _, err := e.writeRecord(client.StopRecord{Chat: e.name, Done: done.Seq}); err != nil {
		return err
	}
	if _, err := e.call(e.root, "close", client.CloseRequest{Chat: e.name}); err != nil {
		return err
	}
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.cancel()
	return errDone
}

func (e *engine) readFrom(cursor int64) ([]client.Message, error) {
	return e.readOn(e.child, cursor, nil)
}

// readOn reads every message after cursor, or every message of kinds when kinds is set.
func (e *engine) readOn(ctx context.Context, cursor int64, kinds []string) ([]client.Message, error) {
	msgs, last, err := readAll(ctx, e.call, e.name, cursor, kinds)
	if err != nil {
		return nil, err
	}
	e.noteSeen(last)
	return msgs, nil
}

// caller makes one bus call. Any outcome but ok is an error.
type caller func(ctx context.Context, op string, body any) (client.Result, error)

// readPage is the read limit. A shorter page is the last.
const readPage = 200

// readAll reads every message of the chat name after cursor, or every message of kinds when kinds is set, following the cursor
// to the end. It returns the messages and the last cursor it read from.
func readAll(ctx context.Context, call caller, name string, cursor int64, kinds []string) ([]client.Message, int64, error) {
	var all []client.Message
	req := client.ReadRequest{Chat: name, Limit: readPage}
	if kinds != nil {
		req.Kinds = &kinds
	}
	for {
		req.Cursor = cursor
		res, err := call(ctx, "read", req)
		if err != nil {
			return nil, 0, err
		}
		if res.Messages == nil || len(*res.Messages) == 0 {
			return all, cursor, nil
		}
		batch := *res.Messages
		all = append(all, batch...)
		cursor = batch[len(batch)-1].Seq
		if len(batch) < readPage {
			return all, cursor, nil
		}
	}
}

func (e *engine) noteSeen(seq int64) {
	e.mu.Lock()
	e.seen = max(e.seen, seq)
	e.mu.Unlock()
}

// head is the newest seq in the chat.
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
		Chat:   e.name,
		Cursor: cursor,
	})
	if err != nil {
		return client.Message{}, err
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
