// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/p3bot/hotseat/internal/client"
)

type launchKind int

const (
	launchBoot launchKind = iota
	launchTurn
	launchFollow
	launchDiscussion
	launchStall
	launchFix
	launchReport
	launchAggregate
	launchHandoff
)

// String is the launch kind as an event names it.
func (k launchKind) String() string {
	switch k {
	case launchBoot:
		return "boot"
	case launchTurn:
		return "turn"
	case launchFollow:
		return "follow"
	case launchDiscussion:
		return "discussion"
	case launchStall:
		return "stall"
	case launchFix:
		return "fix"
	case launchReport:
		return "report"
	case launchAggregate:
		return "aggregate"
	case launchHandoff:
		return "handoff"
	}
	return fmt.Sprintf("launchKind(%d)", int(k))
}

var (
	errStopped = errors.New("stopped")
	// errLaunchCap is returned by the start that reached the cap, so that caller alone reports it.
	errLaunchCap = errors.New(classLaunchCap)
)

func (e *engine) bootstrap(a agent) error {
	err := e.launchMember(a.name, launchBoot, e.taskSeq, e.bootPrompt(a))
	if errors.Is(err, errStopped) {
		return nil
	}
	return err
}

// launchMember launches name for kind. seq is the message the launch concerns.
func (e *engine) launchMember(name string, kind launchKind, seq int64, prompt string) error {
	_, err := e.launchUnless(name, kind, seq, prompt, nil)
	return err
}

// launchForTurn launches name for turn unless the transcript, read once the slot is held, shows name already answered it.
// The answer can be stored while a boot launch still holds the slot.
func (e *engine) launchForTurn(name string, turn client.Message, prompt string) error {
	_, err := e.launchUnless(name, launchTurn, turn.Seq, prompt, func() (bool, error) {
		return e.answered(name, turn)
	})
	return err
}

// answered reports whether name published an answer to turn after it.
func (e *engine) answered(name string, turn client.Message) (bool, error) {
	msgs, err := e.readFrom(turn.Seq)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		if replies(m, name, turn) {
			return true, nil
		}
	}
	return false, nil
}

// replies reports whether m is name's answer to turn: a say or finding from name whose re names the turn.
// With no re, it answers when it names the turn's sender in to, and all does not count.
func replies(m client.Message, name string, turn client.Message) bool {
	if m.From != name || (m.Kind != kindSay && m.Kind != kindFinding) {
		return false
	}
	if m.Re != nil {
		return *m.Re == turn.Seq
	}
	return slices.Contains(m.To, turn.From)
}

// concerns reports whether m's re is absent or names seq. A missing re costs readability only, and a wrong one states something false.
func concerns(m client.Message, seq int64) bool {
	return m.Re == nil || *m.Re == seq
}

// launchUnless runs one member process under its slot and reports whether it started one. seq is the message the launch
// concerns. skip, when set, is checked inside the slot. A skipped launch writes no member row and no event.
func (e *engine) launchUnless(name string, kind launchKind, seq int64, prompt string, skip func() (bool, error)) (bool, error) {
	if !e.canLaunch() {
		return false, nil
	}
	a, ok := e.agent(name)
	if !ok || a.module == "" || a.model == "" {
		return false, errors.New("roster is required to launch")
	}
	sl := e.slot(name)
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if err := e.stopped(); err != nil {
		return false, err
	}
	if e.shutting() {
		return false, nil
	}
	if kind != launchBoot && e.localStatus(name) == statusTimedOut {
		return false, nil
	}
	if skip != nil {
		skipped, err := skip()
		if err != nil {
			return false, err
		}
		if skipped {
			return false, nil
		}
	}
	pctx, release := e.processCtx(name)
	defer release()
	var stdout bytes.Buffer
	cmd := e.agentCmd(pctx, a, prompt)
	cmd.Stdout = &stdout
	started, err := e.startUnlessStopping(cmd)
	if errors.Is(err, errLaunchCap) {
		return false, e.publishWind(classLaunchCap)
	}
	if err != nil || !started {
		return false, err
	}
	defer e.doneInFlight()
	return true, e.recordLaunch(pctx, name, kind, seq, a, cmd, &stdout)
}

// recordLaunch writes the started process's rows, waits for it, and records how it ended.
func (e *engine) recordLaunch(pctx context.Context, name string, kind launchKind, seq int64, a agent, cmd *exec.Cmd, stdout *bytes.Buffer) error {
	began := time.Now()
	e.events.Info("launch", "member", name, "kind", kind.String(), "seq", seq)
	pid := cmd.Process.Pid
	// Launches have no fence of their own: the fenced row write kills a process a stale run started.
	rowErr := e.writeLaunchRows(name, kind, launchedProc(pid))
	if rowErr != nil {
		if err := killGroup(pid); err != nil {
			_ = e.show(fmt.Errorf("kill %s: %w", name, err))
		}
	}
	waitErr := cmd.Wait()
	e.exitEvent(name, waitErr, time.Since(began))
	if rowErr != nil {
		return rowErr
	}
	// Every write a stale run makes is refused, so the next holder records this member.
	if e.leaseLost() {
		return e.stopped()
	}
	// A zero exit is the member's result, including when the run is already closing.
	if waitErr == nil {
		keys, err := e.locator(a.module)
		if err != nil {
			if werr := e.show(err); werr != nil {
				return werr
			}
			return e.markTimeout(name, classMissingSession)
		}
		id, ok := sessionID(stdout.Bytes(), keys)
		if !ok {
			return e.markTimeout(name, classMissingSession)
		}
		if err := e.storeSession(name, id); err != nil {
			return err
		}
		return e.writeStatus(name, statusExited, procID{})
	}
	if errors.Is(context.Cause(pctx), errLimit) {
		return e.markTimeout(name, classProcessLimit)
	}
	if e.child.Err() != nil && pctx.Err() != nil {
		// The run killed the process. timed-out is not a live pid, and this path does not hand off.
		if err := e.markStopped(name); err != nil {
			return err
		}
		return errStopped
	}
	return e.markTimeout(name, classProcessExit)
}

// writeLaunchRows writes the rows of a process just started: launched for a member with no row, and running for
// every launch that is not a boot.
func (e *engine) writeLaunchRows(name string, kind launchKind, proc procID) error {
	row, err := e.memberRow(name)
	if err != nil {
		return err
	}
	if row == nil {
		if err := e.writeStatus(name, statusLaunched, proc); err != nil {
			return err
		}
	}
	if kind != launchBoot {
		return e.writeStatus(name, statusRunning, proc)
	}
	return nil
}

// exitEvent writes the event of a member process that ended: its exit code when it has one, and how long it ran.
func (e *engine) exitEvent(name string, waitErr error, ran time.Duration) {
	args := []any{"member", name}
	if code, ok := exitCode(waitErr); ok {
		args = append(args, "code", code)
	}
	e.events.Info("exit", append(args, "duration", ran.Round(time.Millisecond))...)
}

func (e *engine) show(err error) error {
	if err == nil || e.stderr == nil {
		return nil
	}
	text := err.Error()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, werr := io.WriteString(e.stderr, text)
	return werr
}

func (e *engine) agentCmd(ctx context.Context, a agent, prompt string) *exec.Cmd {
	args := []string{
		"--quiet",
		"--print",
		"--output", "json",
		"--role", roleMember,
		"--context", contextBus,
		"--permission", e.perm,
		"--agent", a.module,
		"--model", a.model,
	}
	if id := e.sessionOf(a.name); id != "" {
		args = append(args, "--resume="+id)
	}
	cmd := exec.CommandContext(ctx, e.startBin, args...)
	cmd.Dir = e.dir
	cmd.Stdin = strings.NewReader(prompt)
	// Stdout is the session JSON and is parsed apart from this stream.
	cmd.Stderr = e.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killGroup(cmd.Process.Pid)
	}
	return cmd
}

// startUnlessStopping starts cmd unless the run halted, a stopping done was seen, the run is winding down, or the run context ended.
// noteHalt, finishDone, and windDown set those under the same lock, so no process starts after them.
// The launch cap is counted here so parallel launches cannot pass it. The start that reaches it returns errLaunchCap.
// A started process is in flight until the caller calls doneInFlight.
func (e *engine) startUnlessStopping(cmd *exec.Cmd) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.haltErr != nil {
		return false, e.haltErr
	}
	if e.closing {
		return false, nil
	}
	// The process context derives from root so the done sender can outlive the stop, so it cannot refuse this start.
	if e.child.Err() != nil {
		return false, errStopped
	}
	if e.windClass != "" {
		return false, nil
	}
	if e.launches >= e.maxLaunch {
		e.windDownLocked(classLaunchCap)
		return false, errLaunchCap
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	e.launches++
	e.inFlight++
	return true, nil
}

// processCtx returns the context of one member process and its release, which the caller calls after the process is waited.
// The context derives from the caller's, so a signal still kills the process at once. guard decides every other end.
func (e *engine) processCtx(name string) (context.Context, func()) {
	pctx, cancel := context.WithCancelCause(e.root)
	exited := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if cause := e.guard(name, exited); cause != nil {
			cancel(cause)
		}
	}()
	return pctx, func() {
		close(exited)
		<-watched
		cancel(nil)
	}
}

// guard waits for a member process to exit and returns why the run ends it first: errLimit at the process limit,
// or errStopped once the run stops, after graceFor(name) for the sender of the stopping done.
// It returns nil when the process exits, and nil once the lease is lost, because a stale run kills no member.
func (e *engine) guard(name string, exited <-chan struct{}) error {
	var limit, grace <-chan time.Time
	if e.limit > 0 {
		t := time.NewTimer(e.limit)
		defer t.Stop()
		limit = t.C
	}
	stopping := e.child.Done()
	for {
		select {
		case <-exited:
			return nil
		case <-limit:
			if e.leaseLost() {
				return nil
			}
			return errLimit
		case <-grace:
			if e.leaseLost() {
				return nil
			}
			return errStopped
		case <-stopping:
			stopping = nil
			if e.leaseLost() {
				return nil
			}
			d := e.graceFor(name)
			if d <= 0 {
				return errStopped
			}
			t := time.NewTimer(d)
			defer t.Stop()
			grace = t.C
		}
	}
}

// graceFor is how long name may keep running once the run stops. Only the sender of the stopping done gets a period.
func (e *engine) graceFor(name string) time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closing || name != e.doneFrom {
		return 0
	}
	return e.pace.grace
}

// markStopped records a member the run killed while stopping. The stop is not a member failure.
func (e *engine) markStopped(name string) error {
	return e.writeStatus(name, statusTimedOut, procID{})
}

// markTimeout records a member failure and applies the task minimum.
func (e *engine) markTimeout(name, class string) error {
	return e.timeout(name, class, true)
}

// recordTimeout records a member failure without the task minimum, which resume applies once after reapDead.
func (e *engine) recordTimeout(name, class string) error {
	return e.timeout(name, class, false)
}

// timeout writes timed-out, publishes the error, and notes a handoff when name was acting.
// With floor set, the wind-down is decided before the publish and the handoff, so nothing they wake can launch past it.
func (e *engine) timeout(name, class string, floor bool) error {
	// The acting check has to see the old status. A failed read still records the death.
	acting, actErr := e.isActing(name)
	if err := e.writeStatus(name, statusTimedOut, procID{}); err != nil {
		return err
	}
	below := floor && e.windBelowMinimum()
	seq, err := e.publishErr(name, class)
	if err != nil {
		return err
	}
	if below {
		if err := e.publishWind(classBelowMinimum); err != nil {
			return err
		}
	}
	if actErr != nil {
		return actErr
	}
	if acting {
		e.noteHandoff(handoff{from: name, seq: seq})
	}
	return nil
}

// drainHandoff runs outside the failed member's lock. A backup who times out is handed off again.
// A closing or winding-down run starts no backup, so a pending handoff is dropped.
func (e *engine) drainHandoff() error {
	for {
		h, ok := e.takeHandoff()
		if !ok {
			return nil
		}
		if err := e.stopped(); err != nil {
			return err
		}
		if e.shutting() || e.windingDown() {
			return nil
		}
		next, err := e.backup()
		if err != nil {
			return err
		}
		if next == "" {
			return e.haltProtocol("", classNoBackup)
		}
		if err := e.launchMember(next, launchHandoff, h.seq, e.handoffPrompt(h.from, next)); err != nil {
			return err
		}
	}
}

func killGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func exitCode(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}

func mintTx() (string, error) {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// publishErr publishes an error that names no message, under a random transaction id, and returns the stored seq.
func (e *engine) publishErr(member, class string) (int64, error) {
	seq, _, err := e.storeErr(member, class, 0, "")
	return seq, err
}

// haltProtocol publishes the halting protocol error class and stops the run on it.
func (e *engine) haltProtocol(member, class string) error {
	seq, err := e.publishErr(member, class)
	if err != nil {
		return err
	}
	return e.noteHalt(&protocolStop{class: class, seq: seq})
}

// protocolErr publishes a finding-protocol error about the message at re, under a random transaction id.
func (e *engine) protocolErr(member, class string, re int64) error {
	_, _, err := e.storeErr(member, class, re, "")
	return err
}

// factErr publishes a finding-protocol error under tx, a transaction id derived from its fact.
// A continuation that meets the same fact again gets the stored error back.
func (e *engine) factErr(member, class string, re int64, tx string) error {
	_, _, err := e.storeErr(member, class, re, tx)
	return err
}

// storeErr publishes an error from run to owner and returns its seq and whether the bus stored a new message.
// re is the message it concerns, or zero. An empty tx mints a random transaction id.
// Each newly stored error writes one error event, so a fact met again on a continuation writes none.
func (e *engine) storeErr(member, class string, re int64, tx string) (int64, bool, error) {
	seq, fresh, err := e.send(post{kind: kindError, from: fromRun, to: []string{fromOwner}, body: errBody(member, class), re: re, tx: tx})
	if err == nil && fresh {
		e.errEvent(member, class, re)
	}
	return seq, fresh, err
}

// errEvent writes the event of an error run published: its class, the member, and the message it concerns.
func (e *engine) errEvent(member, class string, re int64) {
	args := []any{"class", class}
	if member != "" {
		args = append(args, "member", member)
	}
	if re > 0 {
		args = append(args, "seq", re)
	}
	e.events.Info("error", args...)
}

// errBody is the error layout: the member and the class, or the class alone. The message it concerns is its re.
func errBody(member, class string) string {
	if member == "" {
		return class
	}
	return member + "\n" + class
}

// factTx is the transaction id of an error a continuation can meet again: the class and the parts of its fact, joined by slashes.
func factTx(class string, parts ...string) string {
	return strings.Join(append([]string{class}, parts...), "/")
}

// post is one message run publishes. A zero re publishes none, and an empty tx mints a random transaction id.
type post struct {
	kind string
	from string
	to   []string
	body string
	re   int64
	tx   string
}

// send publishes p and returns the stored seq and whether the bus stored a new message.
// A replay of a stored attempt is success and not new.
func (e *engine) send(p post) (int64, bool, error) {
	tx := p.tx
	if tx == "" {
		var err error
		if tx, err = mintTx(); err != nil {
			return 0, false, err
		}
	}
	req := client.SendRequest{
		Chat:   e.name,
		From:   p.from,
		To:     p.to,
		Body:   p.body,
		TxID:   tx,
		Kind:   p.kind,
		Holder: e.holder,
	}
	if p.re > 0 {
		req.Re = &p.re
	}
	res, err := e.call(e.root, "send", req)
	if err != nil {
		return 0, false, err
	}
	if res.Message == nil {
		return 0, false, fmt.Errorf("publish %s: ok with no message", p.kind)
	}
	return res.Message.Seq, res.AlreadyStored == nil || !*res.AlreadyStored, nil
}
