// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// Exit statuses. Agents branch on them, so they are a contract.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitRefused     = 2
	ExitTimeout     = 3
	ExitUnavailable = 4
)

const (
	healthPass = "pass"
	healthFail = "fail"
)

// exitCodes is the one table from a result word to an exit status: every bus and client outcome, and the bus status health words.
// A word missing from it exits ExitFailure, never ExitOK.
var exitCodes = map[string]int{
	bus.OutcomeOK:                   ExitOK,
	bus.OutcomeRefused:              ExitRefused,
	bus.OutcomeTimeout:              ExitTimeout,
	bus.OutcomeUnavailable:          ExitUnavailable,
	client.OutcomeConnectionFailure: ExitUnavailable,
	healthPass:                      ExitOK,
	healthFail:                      ExitUnavailable,
}

// exitCode is the status for a result word.
func exitCode(word string) int {
	if code, ok := exitCodes[word]; ok {
		return code
	}
	return ExitFailure
}

// outcomeExit is nil for a word that exits 0, and otherwise the reported exit for it. Its result is already on stdout.
func outcomeExit(word string) error {
	if code := exitCode(word); code != ExitOK {
		return reportedExit{code: code}
	}
	return nil
}

// reportedExit is a finished report already written to stdout, and the status to exit with.
// Error is empty so main writes no second line.
type reportedExit struct {
	code int
}

func (reportedExit) Error() string { return "" }

// ReportedExit returns the exit status of an error whose report is already on stdout.
// ok is false for any other error, which main prints on stderr and exits ExitFailure.
func ReportedExit(err error) (code int, ok bool) {
	var rep reportedExit
	if !errors.As(err, &rep) {
		return 0, false
	}
	return rep.code, true
}
