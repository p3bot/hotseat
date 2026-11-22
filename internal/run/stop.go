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
	"slices"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// Stop classes are the body of the one error a run that held its lease publishes to owner when it stops on an error.
const (
	stopProtocol = "stopped by protocol"
	stopRefusal  = "stopped by refusal"
	stopCommand  = "stopped by command"
	stopBus      = "stopped by bus"
	stopLease    = "stopped by lease"
	stopSignal   = "stopped by signal"
	stopRun      = "stopped by run"
)

var stopClasses = []string{stopProtocol, stopRefusal, stopCommand, stopBus, stopLease, stopSignal, stopRun}

// isStopError reports whether m is laid out as the stop error a run publishes: an error from run to owner alone whose
// body is a stop class. The sender is caller-supplied, so this is a reading of the transcript, not proof of a run.
func isStopError(m client.Message) bool {
	return m.Kind == kindError && m.From == fromRun && slices.Equal(m.To, []string{fromOwner}) && slices.Contains(stopClasses, m.Body)
}

// busError is a bus call that did not answer ok. Its text is the operation and the reason.
type busError struct {
	op      string
	outcome string
	reason  string
}

func (e *busError) Error() string {
	return e.op + ": " + e.reason
}

// refusedFor reports whether err is a bus refusal with reason.
func refusedFor(err error, reason string) bool {
	var be *busError
	return errors.As(err, &be) && be.outcome == bus.OutcomeRefused && be.reason == reason
}

// commandError is a start or tk call that failed or printed output run cannot use.
type commandError struct {
	text string
	err  error
}

func (e *commandError) Error() string {
	return e.text
}

func (e *commandError) Unwrap() error {
	return e.err
}

// unusable marks err, a parse of start or tk output, as a command failure.
func unusable(err error) error {
	return &commandError{text: err.Error(), err: err}
}

// rosterRefusal refuses a roster smaller than the task's min_members. It is the same check on create and on a
// continuation, and it changes nothing in the conversation, so it is a refusal of the request and not an error stop.
type rosterRefusal struct {
	have int
	task string
	want int
}

func (e *rosterRefusal) Error() string {
	return fmt.Sprintf("roster has %d members and task %s requires %d", e.have, e.task, e.want)
}

// protocolStop is a halting protocol error run published. seq is that error message.
type protocolStop struct {
	class string
	seq   int64
}

func (e *protocolStop) Error() string {
	return e.class
}

// stopCause is the stop class for err and the message its stop error names, or zero.
// Anything the other classes do not recognise is run's own failure.
func stopCause(err error) (string, int64) {
	var ps *protocolStop
	var be *busError
	var ce *commandError
	switch {
	case errors.As(err, &ps):
		return stopProtocol, ps.seq
	case errors.Is(err, errLeaseLost):
		return stopLease, 0
	case errors.As(err, &be):
		switch be.outcome {
		case bus.OutcomeRefused:
			return stopRefusal, 0
		case bus.OutcomeUnavailable, client.OutcomeConnectionFailure:
			return stopBus, 0
		}
	case errors.As(err, &ce):
		return stopCommand, 0
	}
	return stopRun, 0
}

// isErrorStop reports whether err stops the run on an error. A stopping done, a killed member, and a roster refusal are not,
// and neither is a call the run's own stop cut off, since it is fallout of that stop. A signal ends root, so it stays a cause.
func (e *engine) isErrorStop(err error) bool {
	var refusal *rosterRefusal
	if err == nil || errors.Is(err, errDone) || errors.Is(err, errStopped) || errors.As(err, &refusal) {
		return false
	}
	return !errors.Is(err, context.Canceled) || e.root.Err() != nil || e.child.Err() == nil
}

// halt notes err as the run's stop when it is an error stop, and returns err.
func (e *engine) halt(err error) error {
	if !e.isErrorStop(err) {
		return err
	}
	return e.noteHalt(err)
}

// sayStop publishes the stop error for err once the lease was acquired. Once the run's context has ended the cause is the signal,
// since every error after it is its fallout. The publish carries no holder and is bounded on its own clock,
// so a signal or a lost lease does not prevent it. A failure is reported on stderr, because the run already stops on err.
func (e *engine) sayStop(err error) {
	if !e.leased {
		return
	}
	class, re := stopCause(err)
	if e.root.Err() != nil {
		class, re = stopSignal, 0
	}
	tx, err := mintTx()
	if err != nil {
		_ = e.show(fmt.Errorf("publish %s: %w", class, err))
		return
	}
	req := client.PublishRequest{
		Conversation: e.name,
		From:         fromRun,
		To:           []string{fromOwner},
		Body:         class,
		TxID:         tx,
		Kind:         kindError,
	}
	if re > 0 {
		req.Re = &re
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.root), releaseLimit)
	defer cancel()
	res, err := client.Do(ctx, e.addr, "publish", e.token, req)
	if err == nil && res.Outcome != bus.OutcomeOK {
		err = &busError{op: "publish " + class, outcome: res.Outcome, reason: res.Reason}
	}
	if err != nil {
		_ = e.show(err)
	}
}
