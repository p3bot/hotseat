// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func TestWriteResultPrintsReAfterKind(t *testing.T) {
	re := int64(4)
	res := client.Result{
		Outcome: "ok",
		Messages: &[]client.Message{
			{Seq: 5, Time: "t", From: "bob", Kind: "say", Re: &re, To: []string{"alice"}, Body: "yes", TxID: "2"},
			{Seq: 6, Time: "t", From: "bob", Kind: "say", To: []string{"alice"}, Body: "no", TxID: "3"},
		},
	}
	var buf bytes.Buffer
	if err := writeResult(&buf, res); err != nil {
		t.Fatal(err)
	}
	const want = `outcome: ok
messages:
message:
seq: 5
time: t
from: bob
kind: say
re: 4
to: alice
txid: 2
body <<3
yes
message:
seq: 6
time: t
from: bob
kind: say
to: alice
txid: 3
body <<2
no
`
	if buf.String() != want {
		t.Fatalf("layout:\n%s", buf.String())
	}
	got := parseStdout(t, buf.Bytes())
	msgs := *got.Messages
	if msgs[0].Re == nil || *msgs[0].Re != 4 || msgs[1].Re != nil {
		t.Fatalf("parsed %+v", msgs)
	}
}

func TestSendReIsStoredAndReadPrintsIt(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	runBinOK(t, bin, nil, "chat", "create", "job", "--address", addr)
	first := runBinOK(t, bin, nil, "chat", "send", "job", "--address", addr,
		"--from", "alice", "--kind", "say", "--to", "bob", "--body", "question", "--txid", "q1")
	if first.Message == nil || first.Message.Re != nil {
		t.Fatalf("first %+v", first.Message)
	}
	seq := fmt.Sprint(first.Message.Seq)
	reply := runBinOK(t, bin, nil, "chat", "send", "job", "--address", addr,
		"--from", "bob", "--kind", "say", "--to", "alice", "--body", "answer", "--txid", "a1", "--re", seq)
	if reply.Message == nil || reply.Message.Re == nil || *reply.Message.Re != first.Message.Seq {
		t.Fatalf("reply %+v", reply.Message)
	}

	read := runBinOK(t, bin, nil, "chat", "read", "job", "--address", addr, "--cursor", "0", "--limit", "10")
	msgs := *read.Messages
	if len(msgs) != 2 || msgs[0].Re != nil || msgs[1].Re == nil || *msgs[1].Re != first.Message.Seq {
		t.Fatalf("read %+v", msgs)
	}
	_, raw := runClient(t, context.Background(), "chat", "read", "job", "--address", addr, "--cursor", "0", "--limit", "10")
	if strings.Count(raw, "\nre: ") != 1 || !strings.Contains(raw, "kind: say\nre: "+seq+"\nto: alice\n") {
		t.Fatalf("read text\n%s", raw)
	}
}

func TestSendReRefusalsNameTheRule(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "job")
	create(t, ctx, addr, "other")
	sendChat(t, ctx, addr, "job", "alice", []string{"bob"}, "one", "k1")
	sendChat(t, ctx, addr, "other", "alice", []string{"bob"}, "two", "k2")
	sendChat(t, ctx, addr, "job", "alice", []string{"bob"}, "three", "k3")
	cases := []struct {
		re     string
		reason string
	}{
		{"0", bus.ReasonReRange},
		{"-2", bus.ReasonReRange},
		{"2", bus.ReasonReTarget},
		{"99", bus.ReasonReTarget},
		{"4", bus.ReasonReTarget},
	}
	for _, tc := range cases {
		res, raw := runClient(t, ctx, "chat", "send", "job", "--address", addr,
			"--from", "alice", "--kind", "say", "--to", "bob", "--body", "reply", "--txid", "r"+tc.re, "--re", tc.re)
		if res.Outcome != bus.OutcomeRefused || res.Reason != tc.reason {
			t.Fatalf("re %s:\n%s", tc.re, raw)
		}
	}
	if n := len(readAll(t, ctx, addr, "job", 0)); n != 2 {
		t.Fatalf("stored %d", n)
	}

	replay := func(re ...string) client.Result {
		args := []string{"chat", "send", "job", "--address", addr,
			"--from", "alice", "--kind", "say", "--to", "bob", "--body", "three", "--txid", "k3"}
		if len(re) == 1 {
			args = append(args, "--re", re[0])
		}
		res, _ := runClient(t, ctx, args...)
		return res
	}
	if res := replay(); res.Outcome != bus.OutcomeOK || res.AlreadyStored == nil || !*res.AlreadyStored {
		t.Fatalf("same absent re %+v", res)
	}
	if res := replay("1"); res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonKeyConflict {
		t.Fatalf("present against absent %+v", res)
	}
}

func TestSendWithoutReSendsNoRe(t *testing.T) {
	var mu sync.Mutex
	var got [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"outcome":"ok","already_stored":false,"message":{"seq":1,"time":"t","from":"alice","to":[],"body":"x","txid":"1","kind":"say","re":null}}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	ctx := context.Background()
	base := []string{"chat", "send", "job", "--address", addr, "--from", "alice", "--kind", "say", "--body", "x", "--txid", "1"}
	runClient(t, ctx, base...)
	runClient(t, ctx, append(base, "--re", "0")...)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("calls %d", len(got))
	}
	assertAbsent(t, got[0], "re")
	if !bytes.Contains(got[1], []byte(`"re":0`)) {
		t.Fatalf("re 0 not sent: %s", got[1])
	}
}

func TestAttendPrintsTheReLine(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	createRoster(t, ctx, addr, "job", "alpha")
	turn := sendAs(t, ctx, addr, "job", "owner", "turn", []string{"alpha"}, "go", "turn-1")
	proc := startAttend(t, "chat", "attend", "job", "--address", addr)
	re := turn.Seq
	res, err := client.Do(ctx, addr, "send", "", client.SendRequest{
		Chat: "job", From: "alpha", To: []string{"owner"},
		Body: "finished", TxID: "done-1", Kind: "done", Re: &re,
	})
	if err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("send %v %+v", err, res)
	}
	out := proc.wait(t)
	if proc.err != nil {
		t.Fatalf("exit %v\n%s", proc.err, out)
	}
	if !strings.Contains(out, fmt.Sprintf("kind: done\nre: %d\nto: owner\n", turn.Seq)) {
		t.Fatalf("attend\n%s", out)
	}
}
