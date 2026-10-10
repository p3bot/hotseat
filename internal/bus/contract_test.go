// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

func TestPublishWithoutKindIsRefused(t *testing.T) {
	base, _, st, _ := startServer(t, Options{})
	create(t, base, "job")
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing", map[string]any{"conversation": "job", "from": "alice", "to": []string{}, "body": "x", "txid": "a"}},
		{"empty", map[string]any{"conversation": "job", "from": "alice", "to": []string{}, "body": "x", "txid": "b", "kind": ""}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := mustPost(t, base, PathPublish, tt.body)
			if res.Outcome != OutcomeRefused || res.Reason != ReasonKindRequired || res.Message != nil {
				t.Fatalf("%+v", res)
			}
		})
	}
	if n := transcriptLen(t, st, "job"); n != 0 {
		t.Fatalf("stored %d", n)
	}
}

func TestKindOutsideTheNameGrammarIsRefused(t *testing.T) {
	base, _, st, _ := startServer(t, Options{})
	create(t, base, "job")
	res := mustPost(t, base, PathPublish, map[string]any{
		"conversation": "job", "from": "alice", "to": []string{}, "body": "x", "txid": "a", "kind": "bad kind",
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonKindBad {
		t.Fatalf("%+v", res)
	}
	if n := transcriptLen(t, st, "job"); n != 0 {
		t.Fatalf("stored %d", n)
	}
}

func TestKindIsPartOfPublishContent(t *testing.T) {
	at := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	base, _, st, _ := startServer(t, Options{Clock: func() time.Time { return at }})
	create(t, base, "job")
	act := publishKind(t, base, "alice", "act", []string{"bob"}, "do it", "a1")
	if act.Outcome != OutcomeOK || act.Message == nil || act.Message.Kind != "act" {
		t.Fatalf("act %+v", act)
	}
	turn := publishKind(t, base, "alice", "turn", []string{"bob"}, "not a mode line", "t1")
	if turn.Outcome != OutcomeOK || turn.Message == nil || turn.Message.Kind != "turn" || turn.Message.Body != "not a mode line" {
		t.Fatalf("turn %+v", turn)
	}
	if closeRes := mustPost(t, base, PathClose, map[string]any{"conversation": "job"}); closeRes.Outcome != OutcomeOK {
		t.Fatal(closeRes)
	}
	again := publishKind(t, base, "alice", "act", []string{"bob"}, "do it", "a1")
	if again.Outcome != OutcomeOK || again.AlreadyStored == nil || !*again.AlreadyStored || again.Message == nil || again.Message.Kind != "act" || again.Conversation == nil || again.Conversation.Status != "closed" {
		t.Fatalf("retry %+v", again)
	}
	conflict := publishKind(t, base, "alice", "say", []string{"bob"}, "do it", "a1")
	if conflict.Outcome != OutcomeRefused || conflict.Reason != ReasonKeyConflict {
		t.Fatalf("kind conflict %+v", conflict)
	}
	if n := transcriptLen(t, st, "job"); n != 2 {
		t.Fatalf("stored %d", n)
	}
}

func TestEmptyKindsArrayIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	for _, path := range []string{PathRead, PathWait} {
		body := map[string]any{"conversation": "job", "cursor": 0, "kinds": []string{}}
		if path == PathRead {
			body["limit"] = 10
		}
		res := mustPost(t, base, path, body)
		if res.Outcome != OutcomeRefused || res.Reason != ReasonKindsEmpty {
			t.Fatalf("%s %+v", path, res)
		}
	}
}

func TestKindsThatAreNotAnArrayAreRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	for _, kinds := range []any{"say", nil} {
		res := mustPost(t, base, PathRead, map[string]any{
			"conversation": "job", "cursor": 0, "limit": 10, "kinds": kinds,
		})
		if res.Outcome != OutcomeRefused || res.Reason != ReasonKindsShape {
			t.Fatalf("kinds %#v %+v", kinds, res)
		}
	}
}

func TestKindsNameOutsideTheGrammarIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	res := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "kinds": []string{"bad kind"},
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonKindsBadName {
		t.Fatalf("%+v", res)
	}
}

func TestNamedWaitWithKindsReturnsTheSpan(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publishKind(t, base, "alice", "say", []string{"carol"}, "side", "1")
	publishKind(t, base, "alice", "say", []string{"bob"}, "earlier", "2")
	publishKind(t, base, "alice", "turn", []string{"bob"}, "mode: parallel\nphase", "3")
	filtered := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "name": "bob", "kinds": []string{"turn"}, "deadline": "0s",
	})
	if filtered.Outcome != OutcomeOK || filtered.MatchSeq == nil || *filtered.MatchSeq != 3 || messageLen(filtered) != 3 {
		t.Fatalf("span %+v len %d", filtered, messageLen(filtered))
	}
	if (*filtered.Messages)[1].Kind != "say" || (*filtered.Messages)[2].Kind != "turn" {
		t.Fatalf("messages %+v", filtered.Messages)
	}
	open := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "name": "bob", "deadline": "0s",
	})
	if open.Outcome != OutcomeOK || open.MatchSeq == nil || *open.MatchSeq != 2 || messageLen(open) != 2 {
		t.Fatalf("address match %+v len %d", open, messageLen(open))
	}
}

func TestUnnamedWaitWithKindsReturnsOnlyTheMatch(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publishKind(t, base, "alice", "say", []string{"bob"}, "earlier", "1")
	publishKind(t, base, "alice", "turn", []string{"bob"}, "mode: parallel\nphase", "2")
	res := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "kinds": []string{"turn"}, "deadline": "0s",
	})
	if res.Outcome != OutcomeOK || res.MatchSeq == nil || *res.MatchSeq != 2 || messageLen(res) != 1 || (*res.Messages)[0].Kind != "turn" {
		t.Fatalf("unnamed %+v", res.Messages)
	}
}

func TestReadKindsFiltersAndLimitCountsReturned(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publishKind(t, base, "alice", "say", []string{"carol"}, "for-carol", "1")
	publishKind(t, base, "alice", "say", []string{"bob"}, "one", "2")
	publishKind(t, base, "alice", "turn", []string{"bob"}, "phase", "3")
	publishKind(t, base, "alice", "say", []string{"bob"}, "two", "4")
	named := mustPost(t, base, PathRead, map[string]any{
		"conversation": "job", "cursor": 0, "limit": 1, "name": "bob", "kinds": []string{"say"},
	})
	if named.Outcome != OutcomeOK || messageLen(named) != 1 || (*named.Messages)[0].Body != "one" {
		t.Fatalf("named %+v", named.Messages)
	}
	skipped := mustPost(t, base, PathRead, map[string]any{
		"conversation": "job", "cursor": 0, "limit": 10, "name": "bob", "kinds": []string{"ask"},
	})
	if skipped.Outcome != OutcomeOK || messageLen(skipped) != 0 {
		t.Fatalf("skipped %+v", skipped.Messages)
	}
	unnamed := mustPost(t, base, PathRead, map[string]any{
		"conversation": "job", "cursor": 0, "limit": 10, "kinds": []string{"turn"},
	})
	if unnamed.Outcome != OutcomeOK || messageLen(unnamed) != 1 || (*unnamed.Messages)[0].Kind != "turn" {
		t.Fatalf("unnamed %+v", unnamed.Messages)
	}
}

func TestCreateBindingRoundTrip(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res, raw := mustRaw(t, base, PathCreate, map[string]any{
		"name":      "job",
		"task":      "tasks:hotseat/review/ticket",
		"ticket":    "foo-x234",
		"seat":      "bravo",
		"directory": "/tmp/work",
		"prompts":   []string{"tasks:hotseat/review/extra", "tasks:hotseat/review/other"},
		"custom":    "line\nname: hidden",
		"roster":    []string{"bravo", "alpha"},
	})
	if res.Outcome != OutcomeOK || res.Conversation == nil {
		t.Fatalf("%+v", res)
	}
	c := res.Conversation
	if c.Seat != "bravo" || c.Task != "tasks:hotseat/review/ticket" || len(c.Prompts) != 2 || c.Prompts[0] != "tasks:hotseat/review/extra" || len(c.Roster) != 2 || c.Roster[0] != "bravo" || c.Roster[1] != "alpha" {
		t.Fatalf("binding %+v", c)
	}
	if !bytes.Contains(raw, []byte(`"prompts":[`)) || !bytes.Contains(raw, []byte(`"roster":[`)) {
		t.Fatalf("json %s", raw)
	}
	plain, _ := mustRaw(t, base, PathCreate, map[string]any{"name": "plain"})
	if plain.Conversation == nil || plain.Conversation.Task != "" || plain.Conversation.Prompts == nil || len(plain.Conversation.Prompts) != 0 || plain.Conversation.Roster == nil || len(plain.Conversation.Roster) != 0 {
		t.Fatalf("name-only %+v", plain.Conversation)
	}
	list := mustPost(t, base, PathList, map[string]any{})
	if list.Conversations == nil || len(*list.Conversations) != 2 || (*list.Conversations)[0].Name != "job" || (*list.Conversations)[0].Roster[1] != "alpha" {
		t.Fatalf("list %+v", list.Conversations)
	}
	members := mustPost(t, base, PathMembers, map[string]any{"conversation": "job"})
	if members.Outcome != OutcomeOK || members.Members == nil || len(*members.Members) != 0 {
		t.Fatalf("create inserted members %+v", members.Members)
	}
}

func TestDuplicateRosterNameIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha", "alpha"},
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonRosterDuplicate {
		t.Fatalf("%+v", res)
	}
	if list := mustPost(t, base, PathList, map[string]any{}); messageLenConv(list) != 0 {
		t.Fatalf("wrote %+v", list.Conversations)
	}
}

func TestSeatOutsideTheRosterIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "carol", "roster": []string{"alpha", "bravo"},
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonSeatNotInRoster {
		t.Fatalf("%+v", res)
	}
	missing := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "roster": []string{"alpha"},
	})
	if missing.Outcome != OutcomeRefused || missing.Reason != ReasonSeatNotInRoster {
		t.Fatalf("empty seat %+v", missing)
	}
	if list := mustPost(t, base, PathList, map[string]any{}); messageLenConv(list) != 0 {
		t.Fatal("wrote a conversation")
	}
}

func TestSeatWithAnEmptyRosterIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{"name": "job", "seat": "alpha"})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonSeatNeedsRoster {
		t.Fatalf("%+v", res)
	}
	if list := mustPost(t, base, PathList, map[string]any{}); messageLenConv(list) != 0 {
		t.Fatal("wrote a conversation")
	}
}

func TestEmptyPromptIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "prompts": []string{"ok", ""},
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonPromptEmpty {
		t.Fatalf("%+v", res)
	}
}

func TestSecondCreateWritesNothing(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	first := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"}, "custom": "keep",
	})
	if first.Outcome != OutcomeOK || first.AlreadyExisted == nil || *first.AlreadyExisted {
		t.Fatalf("first %+v", first)
	}
	second := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "bravo", "roster": []string{"bravo"}, "custom": "other",
	})
	if second.Outcome != OutcomeOK || second.AlreadyExisted == nil || !*second.AlreadyExisted || second.Conversation == nil || second.Conversation.Custom != "keep" || len(second.Conversation.Roster) != 1 || second.Conversation.Roster[0] != "alpha" {
		t.Fatalf("second %+v", second.Conversation)
	}
}

func TestRegisterKeepsStatusExceptWhenLaunched(t *testing.T) {
	at := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	base, _, _, _ := startServer(t, Options{Clock: func() time.Time { return at }})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha", "bravo"},
	})
	pid := 424242
	launched := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched", "pid": pid,
	})
	if launched.Outcome != OutcomeOK || launched.Member == nil || launched.Member.Status != "launched" || launched.Member.Registered != "" || launched.Member.PID == nil || *launched.Member.PID != int64(pid) {
		t.Fatalf("launch %+v", launched.Member)
	}
	second := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	if second.Outcome != OutcomeRefused || second.Reason != ReasonAlreadyLaunched {
		t.Fatalf("second launch %+v", second)
	}
	at = at.Add(time.Minute)
	registered := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
	if registered.Outcome != OutcomeOK || registered.Member == nil || registered.Member.Status != "registered" || registered.Member.Registered == "" || registered.Member.Launched == registered.Member.Registered {
		t.Fatalf("register %+v", registered.Member)
	}
	at = at.Add(time.Minute)
	keep := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
	if keep.Outcome != OutcomeOK || keep.Member == nil || keep.Member.Status != "registered" || keep.Member.Registered == registered.Member.Registered {
		t.Fatalf("second register %+v", keep.Member)
	}
	for _, status := range []string{"running", "exited", "timed-out"} {
		set := mustPost(t, base, PathMember, map[string]any{
			"conversation": "job", "name": "alpha", "status": status,
		})
		if set.Outcome != OutcomeOK || set.Member == nil || set.Member.Status != status || set.Member.PID == nil || *set.Member.PID != int64(pid) {
			t.Fatalf("set %s %+v", status, set.Member)
		}
		before := set.Member.Registered
		at = at.Add(time.Minute)
		again := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
		if again.Outcome != OutcomeOK || again.Member == nil || again.Member.Status != status || again.Member.Registered == before {
			t.Fatalf("register while %s %+v", status, again.Member)
		}
	}
	// A running row can still have an empty registered time.
	mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "bravo", "status": "launched",
	})
	running := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "bravo", "status": "running",
	})
	if running.Outcome != OutcomeOK || running.Member == nil || running.Member.Status != "running" || running.Member.Registered != "" {
		t.Fatalf("running before register %+v", running.Member)
	}
	at = at.Add(time.Minute)
	late := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "bravo"})
	if late.Outcome != OutcomeOK || late.Member == nil || late.Member.Status != "running" || late.Member.Registered == "" {
		t.Fatalf("register while running and unregistered %+v", late.Member)
	}
}

func TestRegisterWithoutARowIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	missing := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
	if missing.Outcome != OutcomeRefused || missing.Reason != ReasonNotFound {
		t.Fatalf("missing conversation %+v", missing)
	}
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	outside := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "bravo"})
	if outside.Outcome != OutcomeRefused || outside.Reason != ReasonNotInRoster {
		t.Fatalf("outside %+v", outside)
	}
	none := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
	if none.Outcome != OutcomeRefused || none.Reason != ReasonMemberMissing {
		t.Fatalf("no row %+v", none)
	}
	if rows := mustPost(t, base, PathMembers, map[string]any{"conversation": "job"}); rows.Members == nil || len(*rows.Members) != 0 {
		t.Fatalf("rows %+v", rows.Members)
	}
}

func TestRosterThatIsNotAnArrayIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{"name": "job", "roster": "alpha"})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonRosterShape {
		t.Fatalf("%+v", res)
	}
}

func TestRosterNameOutsideTheGrammarIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "bad name", "roster": []string{"bad name"},
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonRosterBadName {
		t.Fatalf("%+v", res)
	}
}

func TestPromptsThatAreNotAnArrayAreRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, PathCreate, map[string]any{"name": "job", "prompts": "tasks:hotseat/review/extra"})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonPromptsShape {
		t.Fatalf("%+v", res)
	}
}

func TestNameOutsideTheRosterIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	res := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "bravo", "status": "launched",
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonNotInRoster {
		t.Fatalf("%+v", res)
	}
}

func TestMemberStatusOutsideTheSetIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	res := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "registered",
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonStatusBad {
		t.Fatalf("%+v", res)
	}
}

func TestSecondLaunchedIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	first := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	if first.Outcome != OutcomeOK {
		t.Fatalf("first %+v", first)
	}
	second := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	if second.Outcome != OutcomeRefused || second.Reason != ReasonAlreadyLaunched {
		t.Fatalf("second %+v", second)
	}
	rows := mustPost(t, base, PathMembers, map[string]any{"conversation": "job"})
	if rows.Members == nil || len(*rows.Members) != 1 {
		t.Fatalf("rows %+v", rows.Members)
	}
}

func TestPidWithoutAStatusIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	launched := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	if launched.Outcome != OutcomeOK || launched.Member == nil || launched.Member.PID != nil {
		t.Fatalf("launch %+v", launched.Member)
	}
	res := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "pid": 4242,
	})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonPIDNeedsStatus || res.Member != nil {
		t.Fatalf("pid without status %+v", res)
	}
	got := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha",
	})
	if got.Outcome != OutcomeOK || got.Member == nil || got.Member.Status != "launched" || got.Member.PID != nil {
		t.Fatalf("row %+v", got.Member)
	}
}

func TestPidMustBeAPositiveInteger(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	for _, pid := range []any{0, -1, 1.5, "2"} {
		res := mustPost(t, base, PathMember, map[string]any{
			"conversation": "job", "name": "alpha", "status": "launched", "pid": pid,
		})
		if res.Outcome != OutcomeRefused || res.Reason != ReasonPID {
			t.Fatalf("pid %#v %+v", pid, res)
		}
	}
}

func TestSessionStaysOffTheTranscriptListAndLog(t *testing.T) {
	var logs bytes.Buffer
	base, _, _, _ := startServer(t, Options{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "bravo", "roster": []string{"bravo", "alpha"},
	})
	mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "bravo", "status": "launched", "pid": 424242,
	})
	const secret = "sess-secret-9f3a"
	set := mustPost(t, base, PathSession, map[string]any{
		"conversation": "job", "name": "bravo", "session": secret,
	})
	if set.Outcome != OutcomeOK || set.Session == nil || set.Session.Session != secret || set.Session.Name != "bravo" {
		t.Fatalf("set %+v", set.Session)
	}
	empty := mustPost(t, base, PathSession, map[string]any{
		"conversation": "job", "name": "bravo", "session": "",
	})
	if empty.Outcome != OutcomeRefused || empty.Reason != ReasonSessionEmpty {
		t.Fatalf("empty %+v", empty)
	}
	got := mustPost(t, base, PathSession, map[string]any{"conversation": "job", "name": "bravo"})
	if got.Outcome != OutcomeOK || got.Session == nil || got.Session.Session != secret {
		t.Fatalf("get %+v", got.Session)
	}
	all, raw := mustRaw(t, base, PathSession, map[string]any{"conversation": "job"})
	if all.Sessions == nil || len(*all.Sessions) != 2 || (*all.Sessions)[0].Name != "bravo" || (*all.Sessions)[0].Session != secret || (*all.Sessions)[1].Name != "alpha" || (*all.Sessions)[1].Session != "" {
		t.Fatalf("sessions %+v", all.Sessions)
	}
	if !bytes.Contains(raw, []byte(secret)) {
		t.Fatal("session route hid the id from its caller")
	}
	_, memberBody := mustRaw(t, base, PathMember, map[string]any{"conversation": "job", "name": "bravo"})
	_, listBody := mustRaw(t, base, PathMembers, map[string]any{"conversation": "job"})
	publishKind(t, base, "alice", "say", []string{"bravo"}, "hello", "m1")
	_, readBody := mustRaw(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	_, listConv := mustRaw(t, base, PathList, map[string]any{})
	memberRaw := string(memberBody)
	listRaw := string(listBody)
	readRaw := string(readBody)
	for _, part := range []struct{ name, body string }{
		{"member", memberRaw},
		{"members", listRaw},
		{"read", readRaw},
		{"list", string(listConv)},
		{"log", logs.String()},
	} {
		if strings.Contains(part.body, secret) {
			t.Fatalf("%s leaked the session id\n%s", part.name, part.body)
		}
	}
	for _, part := range []struct{ name, body string }{
		{"members", listRaw},
		{"read", readRaw},
		{"list", string(listConv)},
		{"log", logs.String()},
	} {
		if strings.Contains(part.body, "424242") {
			t.Fatalf("%s leaked the pid\n%s", part.name, part.body)
		}
	}
	if strings.Contains(listRaw, `"pid"`) || strings.Contains(listRaw, `"session"`) {
		t.Fatalf("members %s", listRaw)
	}
	if strings.Contains(memberRaw, `"session"`) || !strings.Contains(memberRaw, `"pid":424242`) {
		t.Fatalf("member %s", memberRaw)
	}
}

func TestMemberWriteAfterCloseIsStored(t *testing.T) {
	base, _, st, _ := startServer(t, Options{})
	mustPost(t, base, PathCreate, map[string]any{
		"name": "job", "seat": "alpha", "roster": []string{"alpha"},
	})
	mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "launched",
	})
	if res := mustPost(t, base, PathClose, map[string]any{"conversation": "job"}); res.Outcome != OutcomeOK || res.Conversation == nil || res.Conversation.Status != "closed" {
		t.Fatalf("close %+v", res)
	}
	pub := publishKind(t, base, "alpha", "say", []string{}, "later report", "late")
	if pub.Outcome != OutcomeOK || pub.Conversation == nil || pub.Conversation.Status != "closed" || pub.Message == nil || pub.Message.Body != "later report" {
		t.Fatalf("publish %+v", pub)
	}
	updated := mustPost(t, base, PathMember, map[string]any{
		"conversation": "job", "name": "alpha", "status": "exited",
	})
	if updated.Outcome != OutcomeOK || updated.Member == nil || updated.Member.Status != "exited" {
		t.Fatalf("status %+v", updated)
	}
	reg := mustPost(t, base, PathRegister, map[string]any{"conversation": "job", "name": "alpha"})
	if reg.Outcome != OutcomeOK || reg.Member == nil || reg.Member.Status != "exited" || reg.Member.Registered == "" {
		t.Fatalf("register after close %+v", reg.Member)
	}
	rows := mustPost(t, base, PathMembers, map[string]any{"conversation": "job"})
	if rows.Outcome != OutcomeOK || rows.Members == nil || len(*rows.Members) != 1 || (*rows.Members)[0].Status != "exited" {
		t.Fatalf("list %+v", rows.Members)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if read.Outcome != OutcomeOK || messageLen(read) != 1 || (*read.Messages)[0].Body != "later report" {
		t.Fatalf("read %+v", read.Messages)
	}
	if n := transcriptLen(t, st, "job"); n != 1 {
		t.Fatalf("stored %d", n)
	}
}

func publishKind(t *testing.T, base, from, kind string, to []string, body, key string) wire {
	t.Helper()
	return mustPost(t, base, PathPublish, map[string]any{
		"conversation": "job",
		"from":         from,
		"to":           to,
		"body":         body,
		"txid":         key,
		"kind":         kind,
	})
}

func transcriptLen(t *testing.T, st *store.Store, conv string) int {
	t.Helper()
	_, msgs, err := st.Transcript(context.Background(), conv, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func messageLenConv(res wire) int {
	if res.Conversations == nil {
		return 0
	}
	return len(*res.Conversations)
}
