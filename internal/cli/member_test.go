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

func TestPublishRequiresKind(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := execute(context.Background(), []string{
		"publish", "--address", "127.0.0.1:1", "--conversation", "job", "--from", "alice", "--body", "hi",
	}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "kind") || strings.Contains(stderr.String(), "txid:") {
		t.Fatalf("err %v\nstdout %s\nstderr %s", err, stdout.String(), stderr.String())
	}
}

func TestMemberListOmitsPidAndSession(t *testing.T) {
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
		Roster:    []string{"bravo", "alpha"},
	})
	if err != nil || created.Outcome != bus.OutcomeOK {
		t.Fatalf("create %v %+v", err, created)
	}
	listed, raw := runClient(t, ctx, "list", "--address", addr)
	if listed.Conversations == nil || len(*listed.Conversations) != 1 {
		t.Fatalf("list %s", raw)
	}
	conv := (*listed.Conversations)[0]
	if conv.Seat != "bravo" || len(conv.Roster) != 2 || conv.Roster[0] != "bravo" || conv.Roster[1] != "alpha" || len(conv.Prompts) != 2 || conv.Prompts[0] != "tasks:hotseat/review/extra" || conv.Custom != "line\nname: hidden" {
		t.Fatalf("binding %+v\n%s", conv, raw)
	}
	if !strings.Contains(raw, "custom <<") || !strings.Contains(raw, "name: hidden") {
		t.Fatalf("custom frame\n%s", raw)
	}
	plain, plainRaw := runClient(t, ctx, "create", "--address", addr, "--name", "plain")
	if plain.Conversation == nil || plain.Conversation.Name != "plain" || strings.Contains(plainRaw, "task:") || strings.Contains(plainRaw, "roster:") || strings.Contains(plainRaw, "prompts:") {
		t.Fatalf("name-only\n%s", plainRaw)
	}
	pid := int64(424242)
	status := "launched"
	if res, err := client.Do(ctx, addr, "member", "", client.MemberRequest{
		Conversation: "job", Name: "alpha", Status: &status,
	}); err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("launch alpha %v %+v", err, res)
	}
	if res, err := client.Do(ctx, addr, "member", "", client.MemberRequest{
		Conversation: "job", Name: "bravo", Status: &status, PID: &pid,
	}); err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("launch bravo %v %+v", err, res)
	}
	const secret = "sess-secret-9f3a"
	session := secret
	name := "bravo"
	if res, err := client.Do(ctx, addr, "session", "", client.SessionRequest{
		Conversation: "job", Name: &name, Session: &session,
	}); err != nil || res.Outcome != bus.OutcomeOK || res.Session == nil || res.Session.Session != secret {
		t.Fatalf("session %v %+v", err, res)
	}
	reg, regRaw := runClient(t, ctx, "member", "register", "--address", addr, "--conversation", "job", "--name", "bravo")
	if reg.Outcome != bus.OutcomeOK || reg.Member == nil || reg.Member.Status != "registered" || reg.Member.Registered == "" {
		t.Fatalf("register %s", regRaw)
	}
	rows, listRaw := runClient(t, ctx, "member", "list", "--address", addr, "--conversation", "job")
	if rows.Members == nil || len(*rows.Members) != 2 || (*rows.Members)[0].Name != "bravo" || (*rows.Members)[0].Status != "registered" || (*rows.Members)[1].Name != "alpha" || (*rows.Members)[1].Status != "launched" {
		t.Fatalf("members %+v\n%s", rows.Members, listRaw)
	}
	if strings.Contains(listRaw, secret) || strings.Contains(listRaw, "424242") || strings.Contains(listRaw, "pid:") || strings.Contains(listRaw, "session:") {
		t.Fatalf("list leaked\n%s", listRaw)
	}
	if !strings.Contains(listRaw, "launched:") || !strings.Contains(listRaw, "registered:") || !strings.Contains(listRaw, "status: registered") {
		t.Fatalf("list fields\n%s", listRaw)
	}
	missing, missingRaw := runClient(t, ctx, "member", "register", "--address", addr, "--conversation", "job", "--name", "carol")
	if missing.Outcome != bus.OutcomeRefused || missing.Reason != bus.ReasonNotInRoster {
		t.Fatalf("missing row %s", missingRaw)
	}
}

func TestReadOmitsKindsUntilTheFlagIsSet(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "job")
	publish(t, ctx, addr, "job", "alice", []string{"bob"}, "hello", "1")
	if res, err := client.Do(ctx, addr, "publish", "", client.PublishRequest{
		Conversation: "job", From: "alice", To: []string{"bob"}, Body: "phase", TxID: "2", Kind: "turn",
	}); err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("turn %v %+v", err, res)
	}
	all, _ := runClient(t, ctx, "read", "--address", addr, "--conversation", "job", "--cursor", "0", "--limit", "10")
	if all.Messages == nil || len(*all.Messages) != 2 {
		t.Fatalf("all %+v", all.Messages)
	}
	only, raw := runClient(t, ctx, "read", "--address", addr, "--conversation", "job", "--cursor", "0", "--limit", "10", "--kind", "turn")
	if only.Messages == nil || len(*only.Messages) != 1 || (*only.Messages)[0].Kind != "turn" || !strings.Contains(raw, "kind: turn") {
		t.Fatalf("filtered %s", raw)
	}
}
