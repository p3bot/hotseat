// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/store"
)

// liveBus serves a fresh store on loopback until the end of the test and returns its address.
func liveBus(t *testing.T) string {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(errors.Join(err, st.Close()))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bus.Serve(ctx, ln, st, bus.Options{}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("bus did not stop")
		}
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return ln.Addr().String()
}

func mustDo(t *testing.T, addr, op string, body any) Result {
	t.Helper()
	res, err := Do(context.Background(), addr, op, "", body)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("%s: %+v", op, res)
	}
	return res
}

func TestRecordResultsDecodeToWhatRecordsReturns(t *testing.T) {
	addr := liveBus(t)
	mustDo(t, addr, "create", CreateRequest{Name: "job"})
	seq := func(kind, txid string) int64 {
		t.Helper()
		res := mustDo(t, addr, "publish", PublishRequest{Conversation: "job", From: "alice", Body: kind, TxID: txid, Kind: kind})
		return res.Message.Seq
	}
	turn, finding := seq("turn", "1"), seq("finding", "2")
	empty := seq("turn", "3")
	set := mustDo(t, addr, "record", SetRecord{Conversation: "job", Turn: turn, Findings: []int64{finding}})
	if set.Set == nil || set.Grouping == nil || set.AlreadyStored == nil || *set.AlreadyStored {
		t.Fatalf("set = %+v", set)
	}
	none := mustDo(t, addr, "record", SetRecord{Conversation: "job", Turn: empty})
	settle := mustDo(t, addr, "record", SettleRecord{Conversation: "job", Finding: finding, Proposal: finding, ProposalKind: "finding"})
	fix := mustDo(t, addr, "record", FixRecord{Conversation: "job", Finding: finding, Fixed: seq("fixed", "4")})
	halt := mustDo(t, addr, "record", HaltRecord{Conversation: "job", Finding: finding})

	got := mustDo(t, addr, "records", RecordsRequest{Conversation: "job"})
	want := Result{
		Outcome:   bus.OutcomeOK,
		Sets:      &[]Set{*set.Set, *none.Set},
		Groupings: &[]Grouping{*set.Grouping},
		Settles:   &[]Settle{*settle.Settle},
		Fixes:     &[]Fix{*fix.Fix},
		Halts:     &[]Halt{*halt.Halt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %+v\nwant %+v", got, want)
	}
}

func TestEachRecordSendsItsSelectorAndOnlyItsFields(t *testing.T) {
	var mu sync.Mutex
	var path, got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		path, got = r.URL.Path, string(body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"outcome":"ok","already_stored":false}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	for _, tc := range []struct {
		req  RecordWrite
		want string
	}{
		{SetRecord{Conversation: "job", Turn: 4}, `{"conversation":"job","record":"set","turn":4,"findings":[]}`},
		{GroupingRecord{Conversation: "job", Turn: 4}, `{"conversation":"job","record":"grouping","turn":4,"drops":[]}`},
		{SettleRecord{Conversation: "job", Finding: 5, Proposal: 6, ProposalKind: "propose"}, `{"conversation":"job","record":"settle","finding":5,"proposal":6,"proposal_kind":"propose"}`},
		{FixRecord{Conversation: "job", Finding: 5, Fixed: 7}, `{"conversation":"job","record":"fix","finding":5,"fixed":7}`},
		{HaltRecord{Conversation: "job", Finding: 5}, `{"conversation":"job","record":"halt","finding":5}`},
	} {
		if _, err := Do(context.Background(), addr, "record", "", tc.req); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		p, body := path, got
		mu.Unlock()
		if p != bus.PathRecord || body != tc.want {
			t.Fatalf("posted %s %s, want %s", p, body, tc.want)
		}
	}
	if _, err := Do(context.Background(), addr, "record", "", RecordsRequest{Conversation: "job"}); err == nil {
		t.Fatal("record posted a body that is not a RecordWrite")
	}
	if _, err := Do(context.Background(), addr, "records", "", RecordsRequest{Conversation: "job"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != bus.PathRecords || got != `{"conversation":"job"}` {
		t.Fatalf("records posted %s %s", path, got)
	}
}
