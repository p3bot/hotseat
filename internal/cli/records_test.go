// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestRecordSessionAndLeaseRoutesHaveNoCommand(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, name := range append([]string{cmd.Name()}, cmd.Aliases...) {
			if name == "record" || name == "records" || name == "session" || name == "lease" {
				t.Fatalf("command %q serves a conductor route", cmd.CommandPath())
			}
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())
}
