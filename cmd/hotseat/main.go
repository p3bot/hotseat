// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command hotseat is the conversation bus.
// It runs the command tree, maps a signal to its exit code, and exits.
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
	if cli.StatusFailure(err) {
		os.Exit(cli.ExitFailure)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(cli.ExitFailure)
	}
}
