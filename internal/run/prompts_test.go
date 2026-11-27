// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"slices"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/client"
)

// Members execute these lines as written and the fake agent never runs them, so each whole line is pinned here.
func TestMemberPromptsGiveTheExactChatCommandLines(t *testing.T) {
	e := &engine{name: "job", seat: "alpha", roster: []agent{{name: "alpha"}, {name: "bravo"}}}
	d := newDiscussion([]string{"alpha", "bravo"}, 4, 7, "alpha")
	d.duplicates = []int64{6}

	reads := func(cursor, name string) []string {
		return []string{
			"Read findings and stances with: hotseat chat read job --cursor " + cursor + " --limit 200 --kind finding --kind propose --kind dismiss --kind agree --kind object --kind stand",
			"Read everything else with: hotseat chat read job --cursor " + cursor + " --limit 200 --to " + name,
		}
	}
	send := func(what, from, kind, re string) string {
		return "Send " + what + " with: hotseat chat send job --from " + from + " --kind " + kind + " --re " + re + ", then --to and the body."
	}
	turn := func(from string) string {
		return "Send the turn with: hotseat chat send job --from " + from + " --kind turn, then --to and the body."
	}
	done := func(from string) string {
		return "Send the done with: hotseat chat send job --from " + from + " --kind done --to owner, then the body."
	}

	// The seat has work after joining, so its only exit instruction comes after the turn.
	ordered := "Join with hotseat chat join job alpha.\nSend the first turn without waiting for anyone else to join.\n" + turn("alpha") + "\nThen exit.\n"
	if got := e.bootPrompt(agent{name: "alpha"}); !strings.HasSuffix(got, ordered) {
		t.Errorf("boot seat order\ngot:\n%s\nwant suffix:\n%s", got, ordered)
	}

	for _, tc := range []struct {
		launch string
		prompt string
		want   []string
	}{
		{"boot", e.bootPrompt(agent{name: "bravo"}), []string{
			"Join with hotseat chat join job bravo and exit.",
		}},
		{"boot seat", e.bootPrompt(agent{name: "alpha"}), []string{
			"Join with hotseat chat join job alpha.",
			turn("alpha"),
		}},
		{"turn", e.turnPrompt("bravo", client.Message{Seq: 3, From: "alpha"}, "parallel", "review"), []string{
			send("the say", "bravo", "say", "3"),
			send("each finding", "bravo", "finding", "3"),
		}},
		// The read starts before the earliest duplicate, so the speaker sees it.
		{"discussion", e.discussionPrompt("bravo", d, false), append(reads("5", "bravo"),
			send("the stance", "bravo", "<stance>", "7"),
			send("a pass", "bravo", "pass", "7"),
		)},
		{"aggregate", e.aggregatePrompt("alpha", client.Message{Seq: 3}, []int64{6, 7}), append(reads("3", "alpha"),
			send("the aggregate", "alpha", "aggregate", "3"),
		)},
		{"stall", e.stallPrompt("alpha", d, nil), append(reads("6", "alpha"),
			send("the extend or deadlock", "alpha", "<extend or deadlock>", "7"),
		)},
		{"fix", e.fixPrompt("bravo", 7, 9), append(reads("6", "bravo"),
			send("the fixed", "bravo", "fixed", "7"),
		)},
		{"follow", e.followPrompt("alpha", 0, nil, nil), []string{
			turn("alpha"),
			done("alpha"),
		}},
		{"handoff", e.handoffPrompt("alpha", "bravo"), []string{
			turn("bravo"),
			done("bravo"),
		}},
		{"report", e.reportPrompt("alpha", nil, ledger{halt: &client.Halt{Finding: 4}}), append(reads("3", "alpha"),
			done("alpha"),
		)},
	} {
		var got []string
		for line := range strings.SplitSeq(tc.prompt, "\n") {
			if strings.Contains(line, "hotseat ") {
				got = append(got, line)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s command lines\ngot:\n%s\nwant:\n%s", tc.launch, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}
}
