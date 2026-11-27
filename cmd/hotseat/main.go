// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command hotseat is the chat bus.
// It runs the command tree, maps a signal to its exit code, then a reported
// exit to its status, and exits.
package main

import (
	"fmt"
	"os"

	"github.com/p3bot/hotseat/internal/cli"
)

func main() {
	err := cli.Execute()
	if code := cli.SignalExitCode(); code != 0 {
		os.Exit(code)
	}
	if code, ok := cli.ReportedExit(err); ok {
		os.Exit(code)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(cli.ExitFailure)
	}
}
