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
)

var errStopped = errors.New("stopped")

func (e *engine) bootstrap(a agent) error {
	err := e.launchMember(a.name, launchBoot, e.bootPrompt(a))
	if errors.Is(err, errStopped) {
		return nil
	}
	return err
}

func (e *engine) launchMember(name string, kind launchKind, prompt string) error {
	if !e.canLaunch() {
		return nil
	}
	a, ok := e.agent(name)
	if !ok || a.module == "" || a.model == "" {
		return errors.New("roster is required to launch")
	}
	sl := e.slot(name)
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if err := e.stopped(); err != nil {
		return err
	}
	if kind != launchBoot && e.localStatus(name) == statusTimedOut {
		return nil
	}
	pctx, cancel := e.processCtx()
	defer cancel()
	var stdout bytes.Buffer
	cmd := e.agentCmd(pctx, a, prompt)
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	row, err := e.memberRow(name)
	if err != nil {
		_ = killGroup(pid)
		_ = cmd.Wait()
		return err
	}
	if row == nil {
		if err := e.writeStatus(name, statusLaunched, int64(pid), true); err != nil {
			_ = killGroup(pid)
			_ = cmd.Wait()
			return err
		}
	}
	if kind != launchBoot {
		if err := e.writeStatus(name, statusRunning, int64(pid), true); err != nil {
			_ = killGroup(pid)
			_ = cmd.Wait()
			return err
		}
	}
	waitErr := cmd.Wait()
	exitLine := [][2]string{{"exit", name}}
	if code, ok := exitCode(waitErr); ok {
		exitLine = append(exitLine, [2]string{"code", fmt.Sprintf("%d", code)})
	}
	e.out.record(exitLine...)
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
		return e.writeStatus(name, statusExited, 0, false)
	}
	if e.child.Err() != nil && errors.Is(pctx.Err(), context.Canceled) {
		// The run killed the process. timed-out is not a live pid, and this path does not hand off.
		if err := e.markStopped(name); err != nil {
			return err
		}
		return errStopped
	}
	if errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return e.markTimeout(name, classProcessLimit)
	}
	return e.markTimeout(name, classProcessExit)
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

func (e *engine) processCtx() (context.Context, context.CancelFunc) {
	if e.limit <= 0 {
		return e.child, func() {}
	}
	return context.WithTimeout(e.child, e.limit)
}

// markStopped records a member the run killed while stopping. The stop is not a member failure.
func (e *engine) markStopped(name string) error {
	return e.writeStatus(name, statusTimedOut, 0, false)
}

func (e *engine) markTimeout(name, class string) error {
	// The acting check has to see the old status. A failed read still records the death.
	acting, actErr := e.isActing(name)
	if err := e.writeStatus(name, statusTimedOut, 0, false); err != nil {
		return err
	}
	if err := e.publishErr(name, class); err != nil {
		return err
	}
	if actErr != nil {
		return actErr
	}
	if acting {
		e.noteHandoff(name)
	}
	return nil
}

// drainHandoff runs outside the failed member's lock. A backup who times out is handed off again.
func (e *engine) drainHandoff() error {
	for {
		from, ok := e.takeHandoff()
		if !ok {
			return nil
		}
		if err := e.stopped(); err != nil {
			return err
		}
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
		if err := e.launchMember(next, launchFollow, e.handoffPrompt(from, next)); err != nil {
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

func (e *engine) publishErr(member, class string) error {
	body := class
	if member != "" {
		body = member + "\n" + class
	}
	return e.publish(kindError, fromRun, []string{fromOwner}, body)
}

func (e *engine) publish(kind, from string, to []string, body string) error {
	tx, err := mintTx()
	if err != nil {
		return err
	}
	res, err := e.call(e.root, "publish", client.PublishRequest{
		Conversation: e.name,
		From:         from,
		To:           to,
		Body:         body,
		TxID:         tx,
		Kind:         kind,
	})
	if err != nil {
		return err
	}
	if res.Outcome != "ok" {
		return fmt.Errorf("publish %s: %s", kind, res.Reason)
	}
	return nil
}
