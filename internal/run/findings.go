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
	"strconv"
	"strings"
	"sync"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// ErrUnresolved is returned when the stopping done left a finding unsettled or a settled change unfixed.
// The unsettled and unfixed lines and the outcome are already on stdout.
var ErrUnresolved = errors.New("findings are unresolved")

// window is what one member stored between its launch start and its exit.
type window struct {
	started bool
	msgs    []client.Message
}

func (w window) of(kinds ...string) []client.Message {
	var out []client.Message
	for _, m := range w.msgs {
		for _, k := range kinds {
			if m.Kind == k {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// fixed is the first fixed in the window whose re is absent or names finding. Any other fixed is not counted.
func (w window) fixed(finding int64) (client.Message, bool) {
	for _, m := range w.of(kindFixed) {
		if concerns(m, finding) {
			return m, true
		}
	}
	return client.Message{}, false
}

// launchWindow launches name and returns the messages it stored during the launch.
// The start is read inside the member's slot, so a message stored before the process started is outside the window.
func (e *engine) launchWindow(name string, kind launchKind, prompt string) (window, error) {
	var from int64
	started, err := e.launchUnless(name, kind, prompt, func() (bool, error) {
		h, err := e.head()
		from = h
		return false, err
	})
	if err != nil || !started {
		return window{}, err
	}
	if err := e.halting(); err != nil {
		return window{started: true}, err
	}
	msgs, err := e.readFrom(from)
	if err != nil {
		return window{}, e.busErr(err)
	}
	w := window{started: true}
	for _, m := range msgs {
		if m.From == name {
			w.msgs = append(w.msgs, m)
		}
	}
	return w, nil
}

// halting is the run's stop: the halt error, errDone once a stopping done was seen, or nil.
func (e *engine) halting() error {
	if err := e.stopped(); err != nil {
		return err
	}
	if e.shutting() {
		return errDone
	}
	return nil
}

// quiet reports whether the run starts no further launch: halted, closing, or winding down.
func (e *engine) quiet() bool {
	return e.halting() != nil || e.windingDown()
}

// ledgerOn reads the protocol kinds stored since the last build and resolves the follow-up target of every turn sender.
// The backup is read at most once per build, and for the fixers only while a settled change has no fix record.
func (e *engine) ledgerOn(ctx context.Context) (ledger, error) {
	e.mu.Lock()
	var cursor int64
	if n := len(e.protocol); n > 0 {
		cursor = e.protocol[n-1].Seq
	}
	e.mu.Unlock()
	fresh, err := e.readOn(ctx, cursor, ledgerKinds)
	if err != nil {
		return ledger{}, err
	}
	e.mu.Lock()
	for _, m := range fresh {
		// Another build may have appended the same messages while this read ran.
		if n := len(e.protocol); n == 0 || m.Seq > e.protocol[n-1].Seq {
			e.protocol = append(e.protocol, m)
		}
	}
	msgs := slices.Clip(e.protocol)
	recs := e.recs.clip()
	e.mu.Unlock()
	backup := sync.OnceValues(e.backup)
	targets := make(map[string]string)
	for _, m := range msgs {
		if m.Kind != kindTurn {
			continue
		}
		if _, ok := targets[m.From]; ok {
			continue
		}
		target, err := e.followVia(m.From, backup)
		if err != nil {
			return ledger{}, err
		}
		targets[m.From] = target
	}
	fixers := []string{e.seat}
	if recs.awaitsFix() {
		b, err := backup()
		if err != nil {
			return ledger{}, err
		}
		if b != "" {
			fixers = append(fixers, b)
		}
	}
	return newLedger(msgs, recs, rosterNames(e.roster), targets, fixers), nil
}

// loadRecords reads every decision record of the conversation. It runs once, before anything reads a ledger.
func (e *engine) loadRecords() error {
	res, err := e.call(e.root, "records", client.RecordsRequest{Conversation: e.name})
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		return fmt.Errorf("records: %s", res.Reason)
	}
	e.mu.Lock()
	e.recs.add(res)
	e.mu.Unlock()
	return nil
}

// writeRecord stores one decision record and keeps every record the result returns. A stored copy is success.
// A refusal stops the run as a refused publish does.
func (e *engine) writeRecord(w client.RecordWrite) (client.Result, error) {
	res, err := e.call(e.root, "record", client.RecordRequest{Holder: e.holder, Record: w})
	if err != nil {
		return res, err
	}
	if res.Outcome != bus.OutcomeOK {
		return res, fmt.Errorf("record: %s", res.Reason)
	}
	e.mu.Lock()
	e.recs.add(res)
	e.mu.Unlock()
	return res, nil
}

// announce publishes the settle to owner with re the agreed proposal. Its transaction id is fixed by the finding,
// so a continuation that announces again stores no second copy. The body comes only from the settle record and
// the stored proposal, so a live run and a continuation publish the same bytes.
func (e *engine) announce(l ledger, s client.Settle) error {
	proposer := l.sender(s.Proposal)
	if proposer == "" {
		return fmt.Errorf("announce finding %d: proposal %d is not in the transcript", s.Finding, s.Proposal)
	}
	_, err := e.send(post{
		kind: kindSettled,
		from: fromRun,
		to:   []string{fromOwner},
		body: announcement(s, proposer),
		re:   s.Proposal,
		tx:   "settled/" + seqText(s.Finding),
	})
	return err
}

// announcement is the settled body, built only from the settle record and its proposal's sender. Nothing parses it.
func announcement(s client.Settle, proposer string) string {
	outcome := "fix follows"
	if !changes(s) {
		outcome = "no change"
	}
	return fmt.Sprintf("settled finding %d: %s by %s, %s", s.Finding, s.ProposalKind, proposer, outcome)
}

// announceSettles publishes the announcement of every settle record again, before a continuation resumes any work
// or closes on a stored stopping done.
func (e *engine) announceSettles() error {
	if !e.canLaunch() {
		return nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	for _, s := range l.recs.settles {
		if err := e.announce(l, s); err != nil {
			return err
		}
	}
	return nil
}

// endLeftover records the head once a leftover's exit is recorded. Its messages up to that seq are history.
// The caller still holds the member's slot, so no launch of this process stores a message from it before the read.
// A stopping run leaves the end open, because nothing is judged after the stop.
func (e *engine) endLeftover(name string) {
	if e.child.Err() != nil {
		return
	}
	h, err := e.head()
	if err != nil {
		_ = e.noteHalt(err)
		return
	}
	e.mu.Lock()
	e.leftoverEnds[name] = h
	e.mu.Unlock()
}

// awaitLeftovers blocks until every process left from an earlier run has exited and been recorded.
func (e *engine) awaitLeftovers() error {
	select {
	case <-e.leftovers:
		return nil
	case <-e.child.Done():
		if err := e.halting(); err != nil {
			return err
		}
		return errStopped
	}
}

// history reports whether an earlier process judged m or a leftover process published it.
func (e *engine) history(m client.Message) bool {
	if !e.continuing {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if m.Seq <= e.historyHead {
		return true
	}
	end, ok := e.leftoverEnds[m.From]
	return ok && m.Seq <= end
}

func (e *engine) judge(msgs ...client.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range msgs {
		e.judged[m.Seq] = true
	}
}

func (e *engine) wasJudged(seq int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.judged[seq]
}

func (e *engine) holdHandoffs(on bool) {
	e.mu.Lock()
	e.holding = on
	e.mu.Unlock()
}

// judgeFinding reports a finding that opened no discussion. The derived transaction id stores that report once.
func (e *engine) judgeFinding(f client.Message) error {
	if !e.canLaunch() {
		return nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	if l.valid(f.Seq) {
		return nil
	}
	return e.factErr(f.From, classBadFinding, f.Seq, factTx(classBadFinding, f.From, seqText(f.Seq)))
}

// judgeStray reports a stance, pass, extend, or deadlock that no live launch window ruled on.
// Stored history is never judged again.
func (e *engine) judgeStray(m client.Message) error {
	if !e.canLaunch() || e.wasJudged(m.Seq) || e.history(m) {
		return nil
	}
	class := classBadStance
	switch m.Kind {
	case kindAggregate:
		class = classBadAggregate
	case kindPass:
		class = classBadPass
	case kindExtend:
		class = classBadExtend
	case kindDeadlock:
		class = classBadDeadlock
	}
	e.judge(m)
	return e.protocolErr(m.From, class, m.Seq)
}

// strayKinds are the protocol kinds judged against a live launch window.
var strayKinds = append([]string{kindAggregate, kindPass, kindExtend, kindDeadlock}, stanceKinds...)

// judgeBacklog reports each finding and stray protocol message stored since the last check.
// The transcript loop sees them only after the job, and a job often ends on a stopping done.
func (e *engine) judgeBacklog() error {
	if !e.canLaunch() {
		return nil
	}
	e.mu.Lock()
	from := e.backlog
	e.mu.Unlock()
	msgs, err := e.readOn(e.child, from, append([]string{kindFinding}, strayKinds...))
	if err != nil {
		return e.busErr(err)
	}
	for _, m := range msgs {
		if m.Kind == kindFinding {
			err = e.judgeFinding(m)
		} else {
			err = e.judgeStray(m)
		}
		if err != nil {
			return err
		}
	}
	if len(msgs) > 0 {
		e.mu.Lock()
		e.backlog = max(e.backlog, msgs[len(msgs)-1].Seq)
		e.mu.Unlock()
	}
	return nil
}

// finishSet writes the set record for a set whose recipients have all exited or timed out, settles its findings, and runs the follow-up.
func (e *engine) finishSet(turn client.Message, seen int64) error {
	if e.windingDown() {
		return nil
	}
	if err := e.stopForNewDone(seen); err != nil {
		return err
	}
	if err := e.judgeReplies(turn); err != nil {
		return err
	}
	e.holdHandoffs(true)
	err := e.closeSet(turn)
	if err == nil {
		err = e.judgeBacklog()
	}
	if err == nil {
		err = e.settleFindings(turn, false)
	}
	e.holdHandoffs(false)
	if err != nil {
		return err
	}
	if err := e.halting(); err != nil || e.windingDown() {
		return err
	}
	return e.followUp(turn, seen)
}

// closeSet writes the set record: the turn and the set's valid findings. A set with one finding also gets its grouping.
func (e *engine) closeSet(turn client.Message) error {
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	var findings []int64
	for _, f := range l.findingsOf(turn.Seq) {
		findings = append(findings, f.Seq)
	}
	_, err = e.writeRecord(client.SetRecord{Conversation: e.name, Turn: turn.Seq, Findings: findings})
	return err
}

// resumeSet continues a set an earlier process finished: its open discussion restarts, a missing fix runs again,
// and the follow-up runs unless the target already posted after the discussions ended.
func (e *engine) resumeSet(turn client.Message, seen int64) error {
	e.holdHandoffs(true)
	err := e.settleFindings(turn, true)
	e.holdHandoffs(false)
	if err != nil {
		return err
	}
	if err := e.halting(); err != nil || e.windingDown() {
		return err
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	if l.followedUp(turn) {
		return nil
	}
	return e.followUp(turn, seen)
}

// judgeReplies reports each live recipient of the set that published no answer to the turn.
// A recipient that timed out already has its failure published. The derived transaction id stores each report once.
func (e *engine) judgeReplies(turn client.Message) error {
	if !e.canLaunch() {
		return nil
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	for _, name := range l.unanswered(turn) {
		if e.localStatus(name) == statusTimedOut {
			continue
		}
		if err := e.factErr(name, classNoReply, turn.Seq, factTx(classNoReply, name, seqText(turn.Seq))); err != nil {
			return err
		}
	}
	return nil
}

// aggregateSet launches the acting seat to drop duplicate findings, then writes the set's grouping record.
// A seat that leaves no single valid aggregate makes each finding its own issue, so duplicates cost discussions and nothing is lost.
func (e *engine) aggregateSet(turn client.Message, findings []client.Message) error {
	if !e.canLaunch() {
		return nil
	}
	seqs := make([]int64, len(findings))
	for i, f := range findings {
		seqs[i] = f.Seq
	}
	seat, err := e.actingOrStop()
	if err != nil {
		return err
	}
	w, err := e.launchWindow(seat, launchAggregate, e.aggregatePrompt(seat, turn, seqs))
	if err != nil {
		return err
	}
	if !w.started && e.quiet() {
		return e.halting()
	}
	groups := singles(seqs)
	aggs := w.of(kindAggregate)
	e.judge(aggs...)
	failed := e.localStatus(seat) == statusTimedOut
	switch {
	case len(aggs) == 1:
		if g, ok := aggregate(aggs[0].Body, seqs); ok && concerns(aggs[0], turn.Seq) {
			groups = g
		} else if err := e.protocolErr(seat, classBadAggregate, aggs[0].Seq); err != nil {
			return err
		}
	case len(aggs) > 1:
		for _, m := range aggs {
			if err := e.protocolErr(seat, classBadAggregate, m.Seq); err != nil {
				return err
			}
		}
	case w.started && !failed:
		if err := e.protocolErr(seat, classNoAggregate, 0); err != nil {
			return err
		}
	}
	if err := e.judgeBacklog(); err != nil {
		return err
	}
	if e.quiet() {
		return e.halting()
	}
	if _, err := e.writeRecord(client.GroupingRecord{Conversation: e.name, Turn: turn.Seq, Drops: drops(groups)}); err != nil {
		return err
	}
	for _, g := range groups {
		group := [][2]string{{"issue", seqText(g[0])}}
		for _, dup := range g[1:] {
			group = append(group, [2]string{"dropped", seqText(dup)})
		}
		e.out.record(group...)
	}
	return nil
}

// settleFindings runs the set's discussions one at a time in seq order, after its grouping.
// A set with findings waits until every leftover has exited and reads the transcript again, so a fixed a leftover published counts.
func (e *engine) settleFindings(turn client.Message, resumed bool) error {
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	if len(l.findingsOf(turn.Seq)) == 0 {
		return nil
	}
	if err := e.awaitLeftovers(); err != nil {
		return err
	}
	if l, err = e.ledgerOn(e.child); err != nil {
		return e.busErr(err)
	}
	if l.needsAggregate(turn.Seq) {
		if err := e.aggregateSet(turn, l.findingsOf(turn.Seq)); err != nil {
			return err
		}
		if l, err = e.ledgerOn(e.child); err != nil {
			return e.busErr(err)
		}
	}
	if !l.isGrouped(turn.Seq) {
		return e.halting()
	}
	restart := resumed
	for _, f := range l.findingsOf(turn.Seq) {
		if e.quiet() {
			return e.halting()
		}
		if s, ok := l.settledOf(f.Seq); ok {
			if err := e.fixSettled(l, f, s); err != nil {
				return err
			}
			continue
		}
		if err := e.discuss(f, restart); err != nil {
			return err
		}
		restart = false
	}
	return nil
}

// fixSettled finishes a settled change with no fix record. A creditable fixed becomes the fix record, so a change
// already applied is not launched again. Only a change with none gets its fix launch again.
func (e *engine) fixSettled(l ledger, f client.Message, s client.Settle) error {
	if !changes(s) || l.hasFixRecord(f.Seq) {
		return nil
	}
	if fixed, ok := l.creditable(f.Seq); ok {
		_, err := e.writeRecord(client.FixRecord{Conversation: e.name, Finding: f.Seq, Fixed: fixed})
		return err
	}
	return e.fix(f.Seq, f.From, s.Proposal)
}

func (e *engine) syncDead(d *discussion) {
	for _, name := range d.roster {
		if e.localStatus(name) == statusTimedOut {
			d.markDead(name)
		}
	}
}

// discuss runs one finding until it settles and is fixed, or the run stops.
func (e *engine) discuss(f client.Message, restarted bool) error {
	d := newDiscussion(rosterNames(e.roster), e.maxSays, f.Seq, f.From)
	start, dups, err := e.discussionStart(f.Seq)
	if err != nil {
		return err
	}
	d.start = start
	d.duplicates = dups
	e.out.record([2]string{"finding", seqText(f.Seq)}, [2]string{"from", f.From})
	pass := ""
	for {
		if e.quiet() {
			return e.halting()
		}
		e.syncDead(d)
		if d.settled() {
			return e.settle(d)
		}
		speaker := ""
		if pass != "" && d.eligible(pass) {
			speaker = pass
		}
		pass = ""
		if speaker == "" {
			if next, ok := d.next(); ok {
				speaker = next
			}
		}
		if speaker == "" {
			more, err := e.stall(d)
			if err != nil || !more {
				return err
			}
			continue
		}
		w, err := e.launchWindow(speaker, launchDiscussion, e.discussionPrompt(speaker, d, restarted))
		if err != nil {
			return err
		}
		if !w.started {
			if e.localStatus(speaker) == statusTimedOut {
				continue
			}
			return e.halting()
		}
		d.spend(speaker)
		pass, err = e.judgeTurn(d, speaker, w)
		if err != nil {
			return err
		}
		if err := e.judgeBacklog(); err != nil {
			return err
		}
	}
}

// discussionStart is the seq a discussion of finding begins after: the transcript's start, raised past every message
// a continuation treats as history. Stances at or before it do not count. It also returns the finding's dropped duplicates.
func (e *engine) discussionStart(finding int64) (int64, []int64, error) {
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return 0, nil, e.busErr(err)
	}
	start, ok := l.discussionStart(finding)
	if !ok {
		start = finding
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	start = max(start, e.historyHead)
	for _, end := range e.leftoverEnds {
		if end != math.MaxInt64 {
			start = max(start, end)
		}
	}
	return start, l.duplicatesOf(finding), nil
}

// judgeTurn rules on one discussion launch and returns the member its accepted pass names.
// A speaker that ended timed-out already has its failure published, so its stance count is not a protocol error
// and its pass is ignored. A single stance it stored is still judged.
func (e *engine) judgeTurn(d *discussion, speaker string, w window) (string, error) {
	stances := w.of(stanceKinds...)
	passes := w.of(kindPass)
	e.judge(stances...)
	e.judge(passes...)
	failed := e.localStatus(speaker) == statusTimedOut
	switch len(stances) {
	case 0:
		if failed {
			break
		}
		if err := e.protocolErr(speaker, classNoStance, 0); err != nil {
			return "", err
		}
	case 1:
		s := stances[0]
		if !concerns(s, d.finding) || !d.stance(speaker, s.Kind, s.Seq) {
			if err := e.protocolErr(speaker, classBadStance, s.Seq); err != nil {
				return "", err
			}
			break
		}
		e.out.record([2]string{"stance", s.Kind}, [2]string{"from", speaker}, [2]string{"seq", seqText(s.Seq)})
	default:
		if failed {
			break
		}
		if err := e.protocolErr(speaker, classExtraStance, 0); err != nil {
			return "", err
		}
	}
	if len(passes) == 0 || failed {
		return "", nil
	}
	if len(passes) > 1 || len(stances) != 1 || len(passes[0].To) != 1 || !concerns(passes[0], d.finding) || !d.passable(speaker, passes[0].To[0]) {
		for _, p := range passes {
			if err := e.protocolErr(speaker, classBadPass, p.Seq); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	to := passes[0].To[0]
	switch e.takeTurn() {
	case turnWindingDown:
		return "", nil
	case turnCapped:
		if err := e.publishErr("", classTurnCap); err != nil {
			return "", err
		}
		return "", e.noteHalt(errors.New(classTurnCap))
	}
	e.out.record([2]string{"pass", to}, [2]string{"from", speaker})
	return to, nil
}

// settle writes the settle record and announces it before any fix launch, then has the finder apply a settled change.
func (e *engine) settle(d *discussion) error {
	res, err := e.writeRecord(client.SettleRecord{Conversation: e.name, Finding: d.finding, Proposal: d.proposal, ProposalKind: d.kind})
	if err != nil {
		return err
	}
	if res.Settle == nil {
		return errors.New("record: the settle result holds no settle")
	}
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	if err := e.announce(l, *res.Settle); err != nil {
		return err
	}
	e.out.line("settled", seqText(d.finding))
	if !d.change() {
		return nil
	}
	return e.fix(d.finding, d.finder, d.proposal)
}

// actingSeat is the seat until it is timed-out, then the backup. Empty means nobody is eligible.
func (e *engine) actingSeat() (string, error) {
	return e.followTarget(e.seat)
}

// actingOrStop returns the acting seat, or stops the run with no backup.
func (e *engine) actingOrStop() (string, error) {
	seat, err := e.actingSeat()
	if err != nil || seat != "" {
		return seat, err
	}
	if err := e.publishErr("", classNoBackup); err != nil {
		return "", err
	}
	return "", e.noteHalt(errors.New(classNoBackup))
}

// fix launches the finder to apply the agreed proposal and writes the fix record for the first counted fixed.
// A launch with no fixed for the finding hands the fix to the acting seat once.
func (e *engine) fix(finding int64, finder string, proposal int64) error {
	w, err := e.launchWindow(finder, launchFix, e.fixPrompt(finder, finding, proposal))
	if err == nil {
		err = e.judgeBacklog()
	}
	if err != nil {
		return err
	}
	if m, ok := w.fixed(finding); ok {
		return e.fixed(finding, m, finder)
	}
	if e.quiet() {
		return e.halting()
	}
	seat, err := e.actingOrStop()
	if err != nil {
		return err
	}
	w, err = e.launchWindow(seat, launchFix, e.fixPrompt(seat, finding, proposal))
	if err == nil {
		err = e.judgeBacklog()
	}
	if err != nil {
		return err
	}
	if m, ok := w.fixed(finding); ok {
		return e.fixed(finding, m, seat)
	}
	if e.quiet() {
		return e.halting()
	}
	if err := e.protocolErr(seat, classNoFix, 0); err != nil {
		return err
	}
	return e.noteHalt(errors.New(classNoFix))
}

// fixed writes the fix record for a counted fixed and prints its status.
func (e *engine) fixed(finding int64, m client.Message, from string) error {
	if _, err := e.writeRecord(client.FixRecord{Conversation: e.name, Finding: finding, Fixed: m.Seq}); err != nil {
		return err
	}
	e.out.record([2]string{"fixed", seqText(finding)}, [2]string{"from", from})
	return nil
}

// stall launches the acting seat to extend or deadlock. It reports whether the discussion continues.
// A seat that ends timed-out with no single decision hands the stall to the backup.
func (e *engine) stall(d *discussion) (bool, error) {
	e.out.line("stall", seqText(d.finding))
	for {
		seat, err := e.actingOrStop()
		if err != nil {
			return false, err
		}
		l, err := e.ledgerOn(e.child)
		if err != nil {
			return false, e.busErr(err)
		}
		w, err := e.launchWindow(seat, launchStall, e.stallPrompt(seat, d, l.asksOf(d.finding)))
		if err != nil {
			return false, err
		}
		if !w.started {
			if e.quiet() {
				return false, e.halting()
			}
			// The seat timed out before its process started, so the backup decides.
			continue
		}
		failed := e.localStatus(seat) == statusTimedOut
		extends := w.of(kindExtend)
		deadlocks := w.of(kindDeadlock)
		e.judge(extends...)
		e.judge(deadlocks...)
		if err := e.judgeBacklog(); err != nil {
			return false, err
		}
		decided := len(extends)+len(deadlocks) == 1
		if decided && len(deadlocks) == 1 && !concerns(deadlocks[0], d.finding) {
			if err := e.protocolErr(seat, classBadDeadlock, deadlocks[0].Seq); err != nil {
				return false, err
			}
			decided = false
		}
		if !decided {
			// A seat that timed out already has its failure published, so the backup decides.
			if failed {
				if e.quiet() {
					return false, e.halting()
				}
				continue
			}
			if err := e.protocolErr(seat, classNoDecision, 0); err != nil {
				return false, err
			}
			return false, e.noteHalt(errors.New(classNoDecision))
		}
		if len(deadlocks) == 1 {
			e.out.record([2]string{"deadlock", seqText(d.finding)}, [2]string{"from", seat})
			if _, err := e.writeRecord(client.HaltRecord{Conversation: e.name, Finding: d.finding}); err != nil {
				return false, err
			}
			return false, e.report(d)
		}
		return e.extend(d, seat, extends[0])
	}
}

// extend refreshes the budget of each live roster name in the extend's to. Each other name is reported.
// An extend whose re names another message refreshes nobody and is reported like one addressed to nobody.
// Every extend counts toward the turn cap, including one that refreshes nobody.
func (e *engine) extend(d *discussion, seat string, ext client.Message) (bool, error) {
	var refreshed []string
	addressed := len(ext.To) > 0 && concerns(ext, d.finding)
	if addressed {
		e.syncDead(d)
		var rejected []string
		refreshed, rejected = d.extend(ext.To)
		for _, name := range rejected {
			if err := e.protocolErr(name, classBadExtend, ext.Seq); err != nil {
				return false, err
			}
		}
	} else if err := e.protocolErr(seat, classBadExtend, ext.Seq); err != nil {
		return false, err
	}
	switch e.takeTurn() {
	case turnWindingDown:
		return false, nil
	case turnCapped:
		if err := e.publishErr("", classTurnCap); err != nil {
			return false, err
		}
		return false, e.noteHalt(errors.New(classTurnCap))
	}
	if len(refreshed) == 0 {
		return true, nil
	}
	group := [][2]string{{"extend", seqText(d.finding)}}
	for _, name := range refreshed {
		group = append(group, [2]string{"to", name})
	}
	e.out.record(group...)
	return true, nil
}

// report launches the acting seat to post the outcome: hold done after a deadlock. d is nil when a continuation reports again.
func (e *engine) report(d *discussion) error {
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return e.busErr(err)
	}
	for {
		seat, err := e.actingOrStop()
		if err != nil {
			return err
		}
		w, err := e.launchWindow(seat, launchReport, e.reportPrompt(seat, d, l))
		if err != nil {
			return err
		}
		if !w.started {
			if e.quiet() {
				return e.halting()
			}
			continue
		}
		done, stop, err := e.firstStop(w.of(kindDone))
		if err != nil {
			return err
		}
		if stop {
			return e.finishDone(done)
		}
		if e.quiet() {
			return e.halting()
		}
		if err := e.protocolErr(seat, classNoReport, 0); err != nil {
			return err
		}
		return e.noteHalt(errors.New(classNoReport))
	}
}

// reportAgain answers a halt record with no stopping done: the report launch runs again and nothing else.
func (e *engine) reportAgain() (bool, error) {
	l, err := e.ledgerOn(e.child)
	if err != nil {
		return false, e.busErr(err)
	}
	if _, ok := l.halted(); !ok {
		return false, nil
	}
	if !e.canLaunch() {
		return true, nil
	}
	if err := e.awaitLeftovers(); err != nil {
		return true, err
	}
	return true, e.report(nil)
}

// reportStop prints the stop result from the messages and records before the done the stop record names.
// An unsettled stop with no halt publishes one unsettled error about that done, under a derived transaction id.
func (e *engine) reportStop() error {
	l, err := e.ledgerOn(e.root)
	if err != nil {
		return err
	}
	stop, ok := l.recs.stopped()
	if !ok {
		return errors.New("closed with no stop record")
	}
	cut := l.before(stop.Done)
	unsettled, unfixed := cut.unresolved()
	for _, seq := range unsettled {
		e.out.line("unsettled", seqText(seq))
	}
	for _, seq := range unfixed {
		e.out.line("unfixed", seqText(seq))
	}
	if len(unsettled) == 0 && len(unfixed) == 0 {
		e.printOutcome("done")
		return nil
	}
	if _, halted := cut.halted(); halted {
		e.printOutcome("deadlock")
		return ErrUnresolved
	}
	if err := e.factErr("", classUnsettled, stop.Done, factTx(classUnsettled, seqText(stop.Done))); err != nil {
		return err
	}
	e.printOutcome("unsettled")
	return ErrUnresolved
}

func seqText(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

func seqList(seqs []int64) string {
	parts := make([]string, len(seqs))
	for i, s := range seqs {
		parts[i] = seqText(s)
	}
	return strings.Join(parts, " ")
}

func nameList(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, " ")
}

// readCommands are the two reads a protocol prompt gives: findings and stances unnamed by kind, and the rest by name.
func (e *engine) readCommands(b *strings.Builder, name string, cursor int64) {
	fmt.Fprintf(b, "cursor: %d\n", cursor)
	b.WriteString("Read findings and stances with: hotseat read --conversation " + e.name + " --cursor " + seqText(cursor) + " --limit 200")
	for _, k := range append([]string{kindFinding}, stanceKinds...) {
		b.WriteString(" --kind " + k)
	}
	b.WriteByte('\n')
	fmt.Fprintf(b, "Read everything else with: hotseat read --conversation %s --cursor %d --limit 200 --name %s\n", e.name, cursor, name)
}

// publishCommand writes the publish command a prompt gives for what, with the re run judges it by.
func (e *engine) publishCommand(b *strings.Builder, what, name, kind string, re int64) {
	fmt.Fprintf(b, "Publish %s with: hotseat publish --conversation %s --from %s --kind %s --re %d, then --to and the body.\n", what, e.name, name, kind, re)
}

func (e *engine) discussionPrompt(name string, d *discussion, restarted bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "This is a discussion launch for finding %d.\n", d.finding)
	fmt.Fprintf(&b, "finding: %d\nproposal: %d\nproposer: %s\n", d.finding, d.proposal, d.proposer)
	duplicatesNote(&b, d.duplicates)
	startNote(&b, d)
	e.readCommands(&b, name, slices.Min(append([]int64{d.finding}, d.duplicates...))-1)
	if restarted {
		b.WriteString("This discussion restarted. Stances published before the restart do not count.\n")
	}
	if d.dead[d.proposer] {
		fmt.Fprintf(&b, "The proposer %s is timed-out. propose or dismiss is how you change the proposal.\n", d.proposer)
	}
	if name == d.proposer {
		b.WriteString("You are the proposer. Publish exactly one stance: stand to keep the proposal and answer the objection, propose to amend it, or dismiss to withdraw it.\n")
	} else {
		b.WriteString("Publish exactly one stance: agree, object, propose, or dismiss.\n")
	}
	fmt.Fprintf(&b, "Address the stance to every roster name: %s.\n", strings.Join(d.roster, " "))
	b.WriteString("When you need one member's answer next, also publish one pass addressed to that one member. Do not use turn for this.\n")
	e.publishCommand(&b, "the stance", name, "<stance>", d.finding)
	e.publishCommand(&b, "a pass", name, kindPass, d.finding)
	b.WriteString(e.busNote())
	return b.String()
}

// duplicatesNote names the findings the seat dropped as the same issue. They are read for what they add and get no discussion of their own.
func duplicatesNote(b *strings.Builder, dups []int64) {
	if len(dups) == 0 {
		return
	}
	fmt.Fprintf(b, "duplicates: %s\n", seqList(dups))
	b.WriteString("The acting seat dropped each duplicate as the same issue as this finding. Read them. A proposal covers every location they name.\n")
}

func (e *engine) aggregatePrompt(name string, turn client.Message, findings []int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "This is an aggregate launch for the set opened by turn %d. You are the acting seat. Discussion has not started.\n", turn.Seq)
	fmt.Fprintf(&b, "turn: %d\nfindings: %s\n", turn.Seq, seqList(findings))
	e.readCommands(&b, name, turn.Seq)
	b.WriteString("Read every finding. Two findings are duplicates when they describe the same issue, even when one names more locations than the other.\n")
	fmt.Fprintf(&b, "Publish exactly one aggregate addressed to every roster name: %s.\n", strings.Join(rosterNames(e.roster), " "))
	b.WriteString("The body has one line per issue: the seq of the finding to keep, then the seq of each duplicate it drops, separated by spaces. Keep the most complete finding.\n")
	b.WriteString("Every finding listed above is on exactly one line. A finding with no duplicate is a line of its own. Nothing else is in the body.\n")
	b.WriteString("Do not rewrite a finding, take a stance, pass, or edit the document. Each kept finding is discussed in seq order.\n")
	e.publishCommand(&b, "the aggregate", name, kindAggregate, turn.Seq)
	b.WriteString(e.busNote())
	return b.String()
}

// startNote names where the discussion's stances begin. The read starts at the finding, so earlier discussions show too.
func startNote(b *strings.Builder, d *discussion) {
	fmt.Fprintf(b, "start: %d\n", d.start)
	b.WriteString("Stances with a seq above start belong to this discussion. Earlier stances are history.\n")
}

func (e *engine) stallPrompt(name string, d *discussion, asks []int64) string {
	agreed, not := d.split()
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "This is a stall launch for finding %d. The speaker order found nobody and the finding has not settled. You are the acting seat and you decide.\n", d.finding)
	fmt.Fprintf(&b, "finding: %d\nproposal: %d\nproposer: %s\n", d.finding, d.proposal, d.proposer)
	fmt.Fprintf(&b, "agreed: %s\nnot agreed: %s\nobjecting: %s\nout of budget: %s\ntimed-out: %s\n",
		nameList(agreed), nameList(not), nameList(d.objectors()), nameList(d.outOfBudget()), nameList(e.timedOut()))
	for _, seq := range asks {
		fmt.Fprintf(&b, "ask: %d\n", seq)
	}
	startNote(&b, d)
	e.readCommands(&b, name, d.finding-1)
	b.WriteString("Publish exactly one of these, not both:\n")
	b.WriteString("- extend with to naming each member whose budget is refreshed. The discussion continues from the same proposal and agreements.\n")
	fmt.Fprintf(&b, "- deadlock addressed to every roster name and owner: %s owner. The run stops.\n", strings.Join(d.roster, " "))
	e.publishCommand(&b, "the extend or deadlock", name, "<extend or deadlock>", d.finding)
	b.WriteString(e.busNote())
	return b.String()
}

func (e *engine) fixPrompt(name string, finding, proposal int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "This is a fix launch for finding %d. The roster agreed proposal %d.\n", finding, proposal)
	fmt.Fprintf(&b, "finding: %d\nproposal: %d\n", finding, proposal)
	e.readCommands(&b, name, finding-1)
	fmt.Fprintf(&b, "Apply the agreed proposal at seq %d. You decide how to apply it. The decision to fix is made.\n", proposal)
	fmt.Fprintf(&b, "Then publish fixed addressed to every roster name: %s. The body says what changed. Do not pass.\n", strings.Join(rosterNames(e.roster), " "))
	e.publishCommand(&b, "the fixed", name, kindFixed, finding)
	b.WriteString(e.busNote())
	return b.String()
}

// reportPrompt carries the deadlock and the state of every finding. d is nil when a continuation reports again.
func (e *engine) reportPrompt(name string, d *discussion, l ledger) string {
	finding, _ := l.halted()
	var settled, fixed, undiscussed []int64
	for _, f := range l.findings() {
		s, ok := l.settledOf(f.Seq)
		switch {
		case ok && changes(s) && !l.needsFix(f.Seq):
			fixed = append(fixed, f.Seq)
			settled = append(settled, f.Seq)
		case ok:
			settled = append(settled, f.Seq)
		case f.Seq != finding:
			undiscussed = append(undiscussed, f.Seq)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "This is a report launch. A deadlock on finding %d stopped the run.\n", finding)
	fmt.Fprintf(&b, "finding: %d\n", finding)
	if d != nil {
		_, not := d.split()
		fmt.Fprintf(&b, "proposal: %d\nproposer: %s\ndisagree: %s\n", d.proposal, d.proposer, nameList(not))
	} else {
		b.WriteString("The discussion state before the restart is in the transcript.\n")
	}
	fmt.Fprintf(&b, "settled: %s\nfixed: %s\nundiscussed: %s\ndropped: %s\ntimed-out: %s\n",
		listOrNone(settled), listOrNone(fixed), listOrNone(undiscussed), listOrNone(l.droppedAll()), nameList(e.timedOut()))
	for _, seq := range l.asksOf(finding) {
		fmt.Fprintf(&b, "ask: %d\n", seq)
	}
	e.readCommands(&b, name, finding-1)
	b.WriteString("Publish done addressed to owner. The body starts with outcome: hold and then a report that lists the deadlocked finding, who disagrees, the settled and fixed findings, each undiscussed finding, each dropped duplicate, the timed-out members, and each ask.\n")
	b.WriteString(e.busNote())
	return b.String()
}

func listOrNone(seqs []int64) string {
	if len(seqs) == 0 {
		return "none"
	}
	return seqList(seqs)
}

func (e *engine) timedOut() []string {
	var out []string
	for _, a := range e.roster {
		if e.localStatus(a.name) == statusTimedOut {
			out = append(out, a.name)
		}
	}
	return out
}
