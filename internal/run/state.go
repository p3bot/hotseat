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

// liveProc is a member process still running from an earlier run.
type liveProc struct {
	name     string
	pid      int
	launched string
}

var (
	errLimit     = errors.New("process limit")
	errNotLaunch = errors.New("pid is not the launched process")
)

// launchSkew covers clock grain between process start and the stored launch time.
const launchSkew = 2 * time.Second

// sameLaunch reports whether pid began at or before launched.
// The row is written after the process starts. An unreadable start time does not match.
func sameLaunch(pid int, launched string) bool {
	if pid <= 0 || launched == "" {
		return false
	}
	start, ok := processStart(pid)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, launched)
	if err != nil {
		return false
	}
	return !start.After(at.Add(launchSkew))
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

// reapDead settles a member whose process is already gone.
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
		pid := 0
		if row.PID != nil {
			pid = int(*row.PID)
		}
		if pid > 0 && alive(pid) {
			if !sameLaunch(pid, row.Launched) {
				if err := e.markTimeout(a.name, classProcessExit); err != nil {
					return nil, err
				}
				continue
			}
			live = append(live, liveProc{name: a.name, pid: pid, launched: row.Launched})
			continue
		}
		if err := e.markTimeout(a.name, classProcessExit); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// watchLocked waits for a leftover process. The caller holds that member's slot.
func (e *engine) watchLocked(proc liveProc) error {
	err := e.waitForeign(proc.pid, proc.launched)
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
	return e.exitedForeign(proc.name)
}

func (e *engine) limitForeign(proc liveProc) error {
	if e.child.Err() != nil || e.shutting() {
		return e.settleCancelled(proc)
	}
	serr := e.stopForeign(proc.pid, proc.launched)
	if errors.Is(serr, errNotLaunch) {
		return e.markTimeout(proc.name, classProcessExit)
	}
	if serr != nil || alive(proc.pid) {
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
	return e.writeStatus(name, statusExited, 0, false)
}

// settleCancelled stops a leftover process because the run is ending, and does not record a process-limit failure.
func (e *engine) settleCancelled(proc liveProc) error {
	if err := e.stopForeign(proc.pid, proc.launched); err != nil && !errors.Is(err, errNotLaunch) && alive(proc.pid) {
		return err
	}
	if !e.shutting() {
		return nil
	}
	id, err := e.readSession(proc.name)
	if err != nil {
		return err
	}
	if id != "" {
		e.setSession(proc.name, id)
		return e.writeStatus(proc.name, statusExited, 0, false)
	}
	switch e.localStatus(proc.name) {
	case statusExited, statusTimedOut:
		return nil
	}
	return e.writeStatus(proc.name, statusTimedOut, 0, false)
}

// stopForeign ends the process from this launch. The pid is not reported.
// A start time that is not this launch is left alone.
func (e *engine) stopForeign(pid int, launched string) error {
	if !alive(pid) {
		return nil
	}
	if !sameLaunch(pid, launched) {
		return errNotLaunch
	}
	// A cancelled run is already done, so this wait cannot use that context.
	if e.child.Err() != nil {
		if err := signalTree(pid, syscall.SIGKILL); err != nil && alive(pid) {
			return err
		}
		if err := waitDead(pid, 2*time.Second); err != nil && alive(pid) {
			return errors.New(classProcessLimit)
		}
		return nil
	}
	if err := signalTree(pid, syscall.SIGTERM); err != nil && alive(pid) {
		return err
	}
	if err := e.waitUntilDead(pid, 2*time.Second); err == nil {
		return nil
	}
	if !alive(pid) {
		return nil
	}
	if !sameLaunch(pid, launched) {
		return errNotLaunch
	}
	if err := signalTree(pid, syscall.SIGKILL); err != nil && alive(pid) {
		return err
	}
	if err := e.waitUntilDead(pid, 2*time.Second); err != nil && alive(pid) {
		return errors.New(classProcessLimit)
	}
	return nil
}

func waitDead(pid int, limit time.Duration) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for alive(pid) {
		select {
		case <-timer.C:
			return errLimit
		case <-tick.C:
		}
	}
	return nil
}

func (e *engine) waitUntilDead(pid int, limit time.Duration) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for alive(pid) {
		select {
		case <-e.child.Done():
			return e.child.Err()
		case <-timer.C:
			return errLimit
		case <-tick.C:
		}
	}
	return nil
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

func (e *engine) waitForeign(pid int, launched string) error {
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
		if !sameLaunch(pid, launched) {
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

func (e *engine) writeStatus(name, status string, pid int64, hasPID bool) error {
	st := status
	req := client.MemberRequest{Conversation: e.name, Name: name, Status: &st}
	if hasPID {
		p := pid
		req.PID = &p
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
	if e.localStatus(sender) != statusTimedOut && nameIn(e.roster, sender) {
		return sender, nil
	}
	return e.backup()
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

func (e *engine) hasHandoff() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.handoffFrom != ""
}

func (e *engine) noteHandoff(name string) {
	e.mu.Lock()
	e.handoffFrom = name
	e.mu.Unlock()
}

func (e *engine) takeHandoff() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.handoffFrom == "" {
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

func (e *engine) followPrompt(name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s in conversation %s.\n", name, e.name)
	b.WriteString("The set has finished.\n")
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

func (e *engine) actedCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.acted
}

func (e *engine) addActed() {
	e.mu.Lock()
	e.acted++
	e.mu.Unlock()
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
	return e.closing || e.outcome
}
