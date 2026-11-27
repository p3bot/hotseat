// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

// asConductor holds chat's lease while writes run, as hotseat run would, and releases it after.
func asConductor(t *testing.T, ctx context.Context, addr, chat string, writes func(holder string)) {
	t.Helper()
	const holder = "test-conductor"
	lease := func(op string) {
		t.Helper()
		res, err := client.Do(ctx, addr, "lease", "", client.LeaseRequest{Chat: chat, Lease: op, Holder: holder})
		if err != nil || res.Outcome != bus.OutcomeOK {
			t.Fatalf("lease %s %v %+v", op, err, res)
		}
	}
	lease(bus.LeaseAcquire)
	writes(holder)
	lease(bus.LeaseRelease)
}

func TestSendRequiresKind(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := execute(context.Background(), []string{
		"chat", "send", "job", "--address", "127.0.0.1:1", "--from", "alice", "--body", "hi",
	}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "kind") || strings.Contains(stderr.String(), "txid:") {
		t.Fatalf("err %v\nstdout %s\nstderr %s", err, stdout.String(), stderr.String())
	}
}

func TestJoinAndMembersOmitPidStartAndSession(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	created, err := client.Do(ctx, addr, "create", "", client.CreateRequest{
		Name:      "job",
		Task:      "tasks:hotseat/review/ticket",
		Ticket:    "foo-x234",
		Seat:      "bravo",
		Directory: "/tmp/work",
		Prompts:   []string{"tasks:hotseat/review/extra", "tasks:hotseat/review/other"},
		Custom:    "line\nname: hidden",
		Roster:    []client.RosterEntry{{Name: "bravo"}, {Name: "alpha"}},
	})
	if err != nil || created.Outcome != bus.OutcomeOK {
		t.Fatalf("create %v %+v", err, created)
	}
	listed, raw := runClient(t, ctx, "chat", "list", "--address", addr)
	if listed.Chats == nil || len(*listed.Chats) != 1 {
		t.Fatalf("list %s", raw)
	}
	chat := (*listed.Chats)[0]
	if chat.Seat != "bravo" || len(chat.Roster) != 2 || chat.Roster[0].Name != "bravo" || chat.Roster[1].Name != "alpha" || len(chat.Prompts) != 2 || chat.Prompts[0] != "tasks:hotseat/review/extra" || chat.Custom != "line\nname: hidden" {
		t.Fatalf("binding %+v\n%s", chat, raw)
	}
	if !strings.Contains(raw, "custom <<") || !strings.Contains(raw, "name: hidden") {
		t.Fatalf("custom frame\n%s", raw)
	}
	plain, plainRaw := runClient(t, ctx, "chat", "create", "plain", "--address", addr)
	if plain.Chat == nil || plain.Chat.Name != "plain" || strings.Contains(plainRaw, "task:") || strings.Contains(plainRaw, "roster:") || strings.Contains(plainRaw, "prompts:") {
		t.Fatalf("name-only\n%s", plainRaw)
	}
	pid := int64(424242)
	const token = "boot-7c1e/313131"
	status := "launched"
	const secret = "sess-secret-9f3a"
	asConductor(t, ctx, addr, "job", func(holder string) {
		if res, err := client.Do(ctx, addr, "member", "", client.MemberRequest{
			Chat: "job", Name: "alpha", Status: &status, Holder: holder,
		}); err != nil || res.Outcome != bus.OutcomeOK {
			t.Fatalf("launch alpha %v %+v", err, res)
		}
		if res, err := client.Do(ctx, addr, "member", "", client.MemberRequest{
			Chat: "job", Name: "bravo", Status: &status, PID: &pid, Start: token, Holder: holder,
		}); err != nil || res.Outcome != bus.OutcomeOK {
			t.Fatalf("launch bravo %v %+v", err, res)
		}
		session := secret
		name := "bravo"
		if res, err := client.Do(ctx, addr, "session", "", client.SessionRequest{
			Chat: "job", Name: &name, Session: &session, Holder: holder,
		}); err != nil || res.Outcome != bus.OutcomeOK || res.Session == nil || res.Session.Session != secret {
			t.Fatalf("session %v %+v", err, res)
		}
	})
	joined, joinedRaw := runClient(t, ctx, "chat", "join", "job", "bravo", "--address", addr)
	if joined.Outcome != bus.OutcomeOK || joined.Member == nil || joined.Member.Status != "joined" || joined.Member.Joined == "" {
		t.Fatalf("join %s", joinedRaw)
	}
	if strings.Contains(joinedRaw, "424242") || strings.Contains(joinedRaw, token) || strings.Contains(joinedRaw, "pid:") || strings.Contains(joinedRaw, "start:") {
		t.Fatalf("join leaked\n%s", joinedRaw)
	}
	rows, listRaw := runClient(t, ctx, "chat", "members", "job", "--address", addr)
	if rows.Members == nil || len(*rows.Members) != 2 || (*rows.Members)[0].Name != "bravo" || (*rows.Members)[0].Status != "joined" || (*rows.Members)[1].Name != "alpha" || (*rows.Members)[1].Status != "launched" {
		t.Fatalf("members %+v\n%s", rows.Members, listRaw)
	}
	if strings.Contains(listRaw, secret) || strings.Contains(listRaw, "424242") || strings.Contains(listRaw, token) || strings.Contains(listRaw, "pid:") || strings.Contains(listRaw, "start:") || strings.Contains(listRaw, "session:") {
		t.Fatalf("list leaked\n%s", listRaw)
	}
	if !strings.Contains(listRaw, "launched:") || !strings.Contains(listRaw, "joined:") || !strings.Contains(listRaw, "status: joined") {
		t.Fatalf("list fields\n%s", listRaw)
	}
	missing, missingRaw := runClient(t, ctx, "chat", "join", "job", "carol", "--address", addr)
	if missing.Outcome != bus.OutcomeRefused || missing.Reason != bus.ReasonNotInRoster {
		t.Fatalf("missing row %s", missingRaw)
	}
}

func TestReadOmitsKindsUntilTheFlagIsSet(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "job")
	sendChat(t, ctx, addr, "job", "alice", []string{"bob"}, "hello", "1")
	if res, err := client.Do(ctx, addr, "send", "", client.SendRequest{
		Chat: "job", From: "alice", To: []string{"bob"}, Body: "phase", TxID: "2", Kind: "turn",
	}); err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("turn %v %+v", err, res)
	}
	all, _ := runClient(t, ctx, "chat", "read", "job", "--address", addr, "--cursor", "0", "--limit", "10")
	if all.Messages == nil || len(*all.Messages) != 2 {
		t.Fatalf("all %+v", all.Messages)
	}
	only, raw := runClient(t, ctx, "chat", "read", "job", "--address", addr, "--cursor", "0", "--limit", "10", "--kind", "turn")
	if only.Messages == nil || len(*only.Messages) != 1 || (*only.Messages)[0].Kind != "turn" || !strings.Contains(raw, "kind: turn") {
		t.Fatalf("filtered %s", raw)
	}
}
