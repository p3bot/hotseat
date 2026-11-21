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
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// procID identifies one member process: its pid and the host's start token for it.
// The zero value is no process. An empty start is a process whose token was not read.
type procID struct {
	pid   int
	start string
}

// launchedProc reads the start token of a process run just started. A process already gone gets none.
func launchedProc(pid int) procID {
	start, _ := startToken(pid)
	return procID{pid: pid, start: start}
}

// liveProc is a member process still running from an earlier run.
type liveProc struct {
	name string
	id   procID
}

var (
	errLimit     = errors.New("process limit")
	errNotLaunch = errors.New("pid is not the launched process")
)

// sameLaunch reports whether id's pid is still the process its row recorded: the live start token equals the stored one.
// A row with no token never matches, and neither does a pid whose token cannot be read.
func sameLaunch(id procID) bool {
	if id.pid <= 0 || id.start == "" {
		return false
	}
	start, ok := startToken(id.pid)
	return ok && start == id.start
}

func (e *engine) ensureInstructions(conv client.Conversation) error {
	msgs, err := e.readFrom(0)
	if err != nil {
		return err
	}
	for _, msg := range msgs {
		if msg.Kind == kindInstructions && msg.TxID == txInstructions {
			return nil
		}
	}
	text, err := e.moduleText(conv.Task)
	if err != nil {
		return err
	}
	body := instructionBody(text, conv.Seat, conv.Ticket, conv.Prompts, conv.Custom)
	if len(body) > bus.DefaultMaxBody {
		return fmt.Errorf("instruction body exceeds %d bytes", bus.DefaultMaxBody)
	}
	return e.publishInstructions(body, conv.Roster)
}

func (e *engine) loadMembers() error {
	for _, a := range e.roster {
		row, err := e.memberRow(a.name)
		if err != nil {
			return err
		}
		if row != nil {
			e.setLocal(a.name, row.Status)
		}
		id, err := e.readSession(a.name)
		if err != nil {
			return err
		}
		if id != "" {
			e.setSession(a.name, id)
		}
	}
	return nil
}

// reapDead settles a member whose process is already gone. It does not apply the task minimum, which resume checks once.
// A live process from this launch is returned so the transcript can be watched while it runs.
func (e *engine) reapDead() ([]liveProc, error) {
	var live []liveProc
	for _, a := range e.roster {
		row, err := e.memberRow(a.name)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		e.setLocal(a.name, row.Status)
		switch row.Status {
		case statusExited, statusTimedOut:
			continue
		case statusLaunched, statusRunning, statusRegistered:
		default:
			continue
		}
		var id procID
		if row.PID != nil {
			id = procID{pid: int(*row.PID), start: row.Start}
		}
		if alive(id.pid) && sameLaunch(id) {
			live = append(live, liveProc{name: a.name, id: id})
			continue
		}
		if err := e.recordTimeout(a.name, classProcessExit); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// watchLocked waits for a leftover process. The caller holds that member's slot.
// Once the lease is lost it stops watching and leaves the process running, unsignalled and unrecorded,
// for the next holder to adopt: this run holds no pipe for it, and every write it could make is refused.
func (e *engine) watchLocked(proc liveProc) error {
	err := e.waitForeign(proc.id)
	if e.leaseLost() {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return e.settleCancelled(proc)
	}
	if errors.Is(err, errNotLaunch) {
		return e.markTimeout(proc.name, classProcessExit)
	}
	if errors.Is(err, errLimit) {
		return e.limitForeign(proc)
	}
	if err != nil {
		return err
	}
	if e.shutting() {
		return e.settleShutdown(proc.name)
	}
	return e.exitedForeign(proc.name)
}

func (e *engine) limitForeign(proc liveProc) error {
	if e.child.Err() != nil || e.shutting() {
		return e.settleCancelled(proc)
	}
	serr := e.stopForeign(proc.id)
	if errors.Is(serr, errNotLaunch) {
		return e.markTimeout(proc.name, classProcessExit)
	}
	if serr != nil || alive(proc.id.pid) {
		return e.noteHalt(errors.New(classProcessLimit))
	}
	return e.markTimeout(proc.name, classProcessLimit)
}

// No captured stdout, so a clean exit is only a session id already stored.
func (e *engine) exitedForeign(name string) error {
	id, err := e.readSession(name)
	if err != nil {
		return err
	}
	if id == "" {
		return e.markTimeout(name, classProcessExit)
	}
	e.setSession(name, id)
	return e.writeStatus(name, statusExited, procID{})
}

// settleCancelled stops a leftover process because the run is ending, and does not record a process-limit failure.
// The sender of the stopping done first gets its grace period to exit. A lease lost meanwhile leaves the process running.
func (e *engine) settleCancelled(proc liveProc) error {
	if d := e.graceFor(proc.name); d > 0 {
		waitDead(e.held, proc.id.pid, d)
	}
	if e.leaseLost() {
		return nil
	}
	if err := e.stopForeign(proc.id); err != nil && !errors.Is(err, errNotLaunch) && alive(proc.id.pid) {
		return err
	}
	if !e.shutting() {
		return nil
	}
	return e.settleShutdown(proc.name)
}

// settleShutdown records a leftover process that ended while the run closes.
// No stdout was captured, so it is exited only with a session id already stored. It publishes no error and queues no handoff.
func (e *engine) settleShutdown(name string) error {
	id, err := e.readSession(name)
	if err != nil {
		return err
	}
	if id != "" {
		e.setSession(name, id)
		return e.writeStatus(name, statusExited, procID{})
	}
	switch e.localStatus(name) {
	case statusExited, statusTimedOut:
		return nil
	}
	return e.writeStatus(name, statusTimedOut, procID{})
}

// stopForeign ends the process from this launch. The pid is not reported.
// A process whose start token is not this launch's is left alone.
func (e *engine) stopForeign(id procID) error {
	pid := id.pid
	if !alive(pid) {
		return nil
	}
	if !sameLaunch(id) {
		return errNotLaunch
	}
	// A cancelled run is already done, so this wait cannot use that context.
	if e.child.Err() != nil {
		if err := signalTree(pid, syscall.SIGKILL); err != nil && alive(pid) {
			return err
		}
		if !waitDead(context.Background(), pid, 2*time.Second) && alive(pid) {
			return errors.New(classProcessLimit)
		}
		return nil
	}
	if err := signalTree(pid, syscall.SIGTERM); err != nil && alive(pid) {
		return err
	}
	if waitDead(e.child, pid, 2*time.Second) {
		return nil
	}
	if !alive(pid) {
		return nil
	}
	if !sameLaunch(id) {
		return errNotLaunch
	}
	if err := signalTree(pid, syscall.SIGKILL); err != nil && alive(pid) {
		return err
	}
	if !waitDead(e.child, pid, 2*time.Second) && alive(pid) {
		return errors.New(classProcessLimit)
	}
	return nil
}

// waitDead reports whether pid is gone within limit. It returns false at once when ctx ends.
func waitDead(ctx context.Context, pid int, limit time.Duration) bool {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for alive(pid) {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-tick.C:
		}
	}
	return true
}

func signalTree(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return syscall.EPERM
	}
	gerr := syscall.Kill(-pid, sig)
	perr := syscall.Kill(pid, sig)
	if perr == nil || errors.Is(perr, syscall.ESRCH) {
		return nil
	}
	if gerr == nil && !alive(pid) {
		return nil
	}
	return perr
}

func (e *engine) waitForeign(id procID) error {
	pid := id.pid
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var deadline time.Time
	if e.limit > 0 {
		deadline = time.Now().Add(e.limit)
	}
	for {
		if !alive(pid) {
			return nil
		}
		if !sameLaunch(id) {
			return errNotLaunch
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return errLimit
		}
		select {
		case <-e.child.Done():
			return e.child.Err()
		case <-tick.C:
		}
	}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	// A zombie still accepts signal 0, and an unreadable status stays alive.
	return !zombie(pid)
}

func (e *engine) memberRow(name string) (*client.Member, error) {
	res, err := e.call(e.root, "member", client.MemberRequest{Conversation: e.name, Name: name})
	if err != nil {
		return nil, err
	}
	if res.Outcome == bus.OutcomeRefused && res.Reason == bus.ReasonMemberMissing {
		return nil, nil
	}
	if res.Outcome != bus.OutcomeOK || res.Member == nil {
		return nil, fmt.Errorf("member %s: %s", name, res.Reason)
	}
	return res.Member, nil
}

func (e *engine) readSession(name string) (string, error) {
	n := name
	res, err := e.call(e.root, "session", client.SessionRequest{
		Conversation: e.name,
		Name:         &n,
	})
	if err != nil {
		return "", err
	}
	if res.Outcome == bus.OutcomeRefused && res.Reason == bus.ReasonMemberMissing {
		return "", nil
	}
	if res.Outcome != bus.OutcomeOK || res.Session == nil {
		return "", fmt.Errorf("session %s: %s", name, res.Reason)
	}
	return res.Session.Session, nil
}

// writeStatus writes name's status, and id's pid and start token when id is a process.
func (e *engine) writeStatus(name, status string, id procID) error {
	st := status
	req := client.MemberRequest{Conversation: e.name, Name: name, Status: &st, Holder: e.holder}
	if id.pid > 0 {
		p := int64(id.pid)
		req.PID = &p
		req.Start = id.start
	}
	res, err := e.call(e.root, "member", req)
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		return fmt.Errorf("member %s: %s", name, res.Reason)
	}
	e.setLocal(name, status)
	e.out.record([2]string{"member", name}, [2]string{"status", status})
	return nil
}

func (e *engine) storeSession(name, id string) error {
	n := name
	s := id
	res, err := e.call(e.root, "session", client.SessionRequest{
		Conversation: e.name,
		Name:         &n,
		Session:      &s,
		Holder:       e.holder,
	})
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		return fmt.Errorf("session %s: %s", name, res.Reason)
	}
	e.setSession(name, id)
	return nil
}

// backup returns the first eligible name. An empty name means nobody eligible was read.
func (e *engine) backup() (string, error) {
	names := rosterNames(e.roster)
	sort.Strings(names)
	for _, name := range names {
		if name == e.seat {
			continue
		}
		row, err := e.memberRow(name)
		if err != nil {
			return "", err
		}
		if row == nil || row.Registered == "" {
			continue
		}
		switch row.Status {
		case statusRegistered, statusRunning, statusExited:
			return name, nil
		}
	}
	return "", nil
}

func (e *engine) followTarget(sender string) (string, error) {
	return e.followVia(sender, e.backup)
}

// followVia is the follow-up target of sender: the sender while it is a live roster member, otherwise the backup.
func (e *engine) followVia(sender string, backup func() (string, error)) (string, error) {
	if e.localStatus(sender) != statusTimedOut && nameIn(e.roster, sender) {
		return sender, nil
	}
	return backup()
}

func (e *engine) bootPrompt(a agent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", a.name, e.name)
	b.WriteString("Read the instructions message addressed to you.\n")
	b.WriteString("start get each address in its trailer.\n")
	b.WriteString("Register with hotseat member register and exit.\n")
	if a.name == e.seat {
		b.WriteString("Publish the first turn without waiting for anyone else to register.\n")
	}
	b.WriteString(e.busNote())
	return b.String()
}

func (e *engine) turnPrompt(name string, msg client.Message, mode, phase string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "cursor: %d\n", msg.Seq-1)
	fmt.Fprintf(&b, "mode: %s\n", mode)
	b.WriteString(phase)
	if phase != "" && !strings.HasSuffix(phase, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Register, publish a say addressed at least to %s, and exit.\n", msg.From)
	e.publishCommand(&b, "the say", name, kindSay, msg.Seq)
	e.publishCommand(&b, "each finding", name, kindFinding, msg.Seq)
	b.WriteString(e.busNote())
	return b.String()
}

func (e *engine) isActing(name string) (bool, error) {
	switch e.localStatus(e.seat) {
	case statusLaunched, statusRegistered, statusRunning, statusExited:
		return name == e.seat, nil
	}
	backup, err := e.backup()
	if err != nil {
		return false, err
	}
	return backup == name, nil
}

// hasHandoff reports a queued handoff that may launch now. One held during a set's discussions waits.
func (e *engine) hasHandoff() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.handoffFrom != "" && !e.holding
}

// noteHandoff queues a backup launch for name. A winding-down run starts no backup, so it queues none.
func (e *engine) noteHandoff(name string) {
	e.mu.Lock()
	if e.windClass == "" {
		e.handoffFrom = name
	}
	e.mu.Unlock()
}

// takeHandoff leaves a handoff queued while a set's discussions run, so a free-form backup prompt cannot land mid-discussion.
func (e *engine) takeHandoff() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handoffFrom == "" || e.holding {
		return "", false
	}
	from := e.handoffFrom
	e.handoffFrom = ""
	return from, true
}

func (e *engine) handoffPrompt(from, name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	fmt.Fprintf(&b, "%s is timed-out. You are the acting seat.\n", from)
	b.WriteString(e.busNote())
	return b.String()
}

// followPrompt names what the set left for the report: the set's fix records, recipients that did not reply, dropped duplicates, and timed-out members.
func (e *engine) followPrompt(name string, fixed int, noReply []string, dropped []int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	b.WriteString("The set has finished.\n")
	fmt.Fprintf(&b, "fixed: %d\n", fixed)
	fmt.Fprintf(&b, "no reply: %s\ndropped: %s\ntimed-out: %s\n", nameList(noReply), listOrNone(dropped), nameList(e.timedOut()))
	if len(noReply) > 0 {
		b.WriteString("A member under no reply published no say or finding for this set. Do not report its verdicts.\n")
	}
	b.WriteString("Read what was addressed to you and post the next turn, or done if you are the acting seat.\n")
	b.WriteString(e.busNote())
	return b.String()
}

func (e *engine) busNote() string {
	var b strings.Builder
	if e.addr != bus.DefaultListen {
		fmt.Fprintf(&b, "Bus address: %s\n", e.addr)
	}
	if e.tokenFile != "" {
		fmt.Fprintf(&b, "Token file: %s\n", e.tokenFile)
	}
	return b.String()
}

func (e *engine) agent(name string) (agent, bool) {
	for _, a := range e.roster {
		if a.name == name {
			return a, true
		}
	}
	return agent{}, false
}

func (e *engine) slot(name string) *slot {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.slots[name]
	if s == nil {
		s = &slot{}
		e.slots[name] = s
	}
	return s
}

func (e *engine) setLocal(name, status string) {
	e.mu.Lock()
	e.status[name] = status
	e.mu.Unlock()
}

func (e *engine) localStatus(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status[name]
}

func (e *engine) setSession(name, id string) {
	e.mu.Lock()
	e.sessions[name] = id
	e.mu.Unlock()
}

func (e *engine) sessionOf(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions[name]
}

type turnTake int

const (
	turnTaken turnTake = iota
	turnWindingDown
	turnCapped
)

// takeTurn counts one turn unless the run is winding down or at the turn cap.
// It decides under the lock windDown takes, so a turn counted here was accepted before the stop.
func (e *engine) takeTurn() turnTake {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.windClass != "" {
		return turnWindingDown
	}
	if e.acted >= e.maxTurns {
		return turnCapped
	}
	e.acted++
	return turnTaken
}

func (e *engine) noteHalt(err error) error {
	if err == nil || errors.Is(err, errStopped) || errors.Is(err, errDone) {
		return e.stopped()
	}
	e.mu.Lock()
	if e.haltErr == nil {
		e.haltErr = err
	}
	e.mu.Unlock()
	e.cancel()
	return err
}

func (e *engine) stopped() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.haltErr
}

func (e *engine) shutting() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closing || e.closed
}

// windDown stops new launches for class and reports whether this call began it.
// Unlike noteHalt it cancels nothing, so running members finish and are recorded.
func (e *engine) windDown(class string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.windDownLocked(class)
}

func (e *engine) windDownLocked(class string) bool {
	if e.windClass != "" {
		return false
	}
	e.windClass = class
	e.idleIfQuietLocked()
	return true
}

func (e *engine) windingDown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.windClass != ""
}

func (e *engine) windErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return errors.New(e.windClass)
}

// addInFlight counts a leftover process this run watches. A launch is counted where it starts.
func (e *engine) addInFlight() {
	e.mu.Lock()
	e.inFlight++
	e.mu.Unlock()
}

// doneInFlight is called once the process is waited and recorded.
func (e *engine) doneInFlight() {
	e.mu.Lock()
	e.inFlight--
	e.idleIfQuietLocked()
	e.mu.Unlock()
}

func (e *engine) idleIfQuietLocked() {
	if e.windClass != "" && e.inFlight == 0 && !e.idleClosed {
		close(e.idle)
		e.idleClosed = true
	}
}

// enforceMinimum winds the run down and publishes below minimum when fewer roster members than the task minimum are live.
func (e *engine) enforceMinimum() error {
	if !e.windBelowMinimum() {
		return nil
	}
	return e.publishErr("", classBelowMinimum)
}

// windBelowMinimum winds the run down when fewer roster members than the task minimum are live, and reports whether this call began it.
// A minimum of 1 adds nothing, since no live member already stops on the no backup rule.
// A stop the run made itself is not a member failure, so it is not checked.
func (e *engine) windBelowMinimum() bool {
	if e.minimum <= 1 || !e.canLaunch() || e.shutting() || e.stopped() != nil {
		return false
	}
	live := 0
	for _, a := range e.roster {
		if e.localStatus(a.name) != statusTimedOut {
			live++
		}
	}
	return live < e.minimum && e.windDown(classBelowMinimum)
}
