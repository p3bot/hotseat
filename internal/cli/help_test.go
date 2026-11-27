// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxHelpLines caps a page's Long text, so the reference in docs/ stays the one place a rule is stated in full.
const maxHelpLines = 12

func visibleCommands(cmd *cobra.Command) []*cobra.Command {
	out := []*cobra.Command{cmd}
	for _, sub := range cmd.Commands() {
		if !sub.Hidden {
			out = append(out, visibleCommands(sub)...)
		}
	}
	return out
}

func TestEveryHelpPageIsShortAndEndsWithAReferenceThatExists(t *testing.T) {
	for _, cmd := range visibleCommands(newRoot()) {
		name := cmd.CommandPath()
		if cmd.Short == "" {
			t.Errorf("%s has no Short", name)
		}
		lines := strings.Split(cmd.Long, "\n")
		if len(lines) > maxHelpLines {
			t.Errorf("%s Long is %d lines, over %d", name, len(lines), maxHelpLines)
		}
		last := lines[len(lines)-1]
		path, ok := strings.CutPrefix(last, referenceLine)
		if !ok {
			t.Errorf("%s Long does not end with a reference line: %q", name, last)
			continue
		}
		if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(path))); err != nil {
			t.Errorf("%s reference %s: %v", name, path, err)
		}
	}
}

func localFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" {
			names = append(names, f.Name)
		}
	})
	slices.Sort(names)
	return names
}

func TestBusVerbsTakeOnlyTheirOwnFlags(t *testing.T) {
	root := newRoot()
	for path, want := range map[string][]string{
		"bus start":  {"address", "debug", "max-body", "store", "token-file"},
		"bus stop":   {"store"},
		"bus status": {"address"},
	} {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := localFlagNames(cmd); !slices.Equal(got, want) {
			t.Errorf("%s flags %v, want %v", path, got, want)
		}
	}
}

func TestBusServeIsHidden(t *testing.T) {
	cmd, _, err := newRoot().Find([]string{"bus", "serve"})
	if err != nil || cmd.Name() != "serve" || !cmd.Hidden {
		t.Fatalf("bus serve found %v hidden %v err %v", cmd.Name(), cmd.Hidden, err)
	}
}
