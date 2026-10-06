// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

var httpClient = &http.Client{Transport: &http.Transport{Proxy: nil}}

func startServer(t *testing.T, opt Options) (base, dir string, st *store.Store, stop func()) {
	t.Helper()
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	dir = t.TempDir()
	var err error
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, ln, st, opt) }()
	waitReady(t, ln.Addr().String(), errCh)
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Errorf("serve: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("server did not stop")
			}
		})
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	t.Cleanup(stop)
	return "http://" + ln.Addr().String(), dir, st, stop
}

func waitReady(t *testing.T, addr string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-errCh:
			t.Fatalf("serve: %v", err)
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", addr, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func post(ctx context.Context, base, path string, body any) (wire, int, []byte, error) {
	var payload []byte
	switch b := body.(type) {
	case []byte:
		payload = b
	default:
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return wire{}, 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		return wire{}, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return wire{}, 0, nil, err
	}
	raw, err := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return wire{}, resp.StatusCode, raw, err
	}
	var out wire
	if len(raw) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			return wire{}, resp.StatusCode, raw, uerr
		}
	}
	return out, resp.StatusCode, raw, nil
}

func mustPost(t *testing.T, base, path string, body any) wire {
	t.Helper()
	res, _ := mustRaw(t, base, path, body)
	return res
}

func mustRaw(t *testing.T, base, path string, body any) (wire, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, status, raw, err := post(ctx, base, path, body)
	if err != nil {
		t.Fatalf("%s: %v body=%s", path, err, raw)
	}
	if status != http.StatusOK {
		t.Fatalf("%s status %d body %s", path, status, raw)
	}
	return res, raw
}

func create(t *testing.T, base, name string) wire {
	t.Helper()
	res := mustPost(t, base, PathCreate, map[string]any{"name": name})
	if res.Outcome != OutcomeOK {
		t.Fatalf("create %s: %+v", name, res)
	}
	return res
}

func publish(t *testing.T, base, conv, from string, to []string, body, key string) wire {
	t.Helper()
	if to == nil {
		to = []string{}
	}
	res := mustPost(t, base, PathPublish, map[string]any{
		"conversation":    conv,
		"from":            from,
		"to":              to,
		"body":            body,
		"idempotency_key": key,
	})
	return res
}

func TestCreateFreshAndTaken(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := create(t, base, "job")
	if res.Outcome != OutcomeOK || res.AlreadyExisted == nil || *res.AlreadyExisted || res.Conversation == nil || res.Conversation.Status != "open" || res.Conversation.Name != "job" {
		t.Fatalf("conversation = %+v existed %v", res.Conversation, res.AlreadyExisted)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if read.Outcome != OutcomeOK || read.Messages == nil || len(*read.Messages) != 0 {
		t.Fatalf("empty transcript = %+v", read)
	}
	taken := mustPost(t, base, PathCreate, map[string]any{"name": "job"})
	if taken.Outcome != OutcomeOK || taken.AlreadyExisted == nil || !*taken.AlreadyExisted || taken.Conversation == nil || taken.Conversation.Status != "open" {
		t.Fatalf("duplicate = %+v", taken)
	}
	bad := mustPost(t, base, PathCreate, map[string]any{"name": "has space"})
	if bad.Outcome != OutcomeRefused || bad.Reason != ReasonBadName {
		t.Fatalf("bad name = %+v", bad)
	}
	longName := strings.Repeat("a", MaxNameLen)
	if res := create(t, base, longName); res.Outcome != OutcomeOK {
		t.Fatalf("64-char name = %+v", res)
	}
	tooLong := mustPost(t, base, PathCreate, map[string]any{"name": longName + "b"})
	if tooLong.Outcome != OutcomeRefused {
		t.Fatalf("65-char name = %+v", tooLong)
	}
	list := mustPost(t, base, PathList, map[string]any{})
	if list.Conversations == nil || len(*list.Conversations) != 2 {
		t.Fatalf("list = %+v", list.Conversations)
	}
	if (*list.Conversations)[0].Name != longName || (*list.Conversations)[1].Name != "job" {
		t.Fatalf("list order = %+v", *list.Conversations)
	}
}

func TestPublishDurableIdempotentAndConflict(t *testing.T) {
	fixed := time.Date(2026, 10, 3, 7, 0, 0, 123, time.UTC)
	base, _, _, _ := startServer(t, Options{Clock: func() time.Time { return fixed }})
	create(t, base, "job")
	create(t, base, "other")
	first := publish(t, base, "job", "alice", []string{"bob", "carol"}, "hello", "k")
	if first.Outcome != OutcomeOK || first.Message == nil || first.Message.Seq != 1 || first.Conversation == nil || first.Conversation.Status != "open" {
		t.Fatalf("first = %+v", first)
	}
	if first.AlreadyStored == nil || *first.AlreadyStored {
		t.Fatalf("already_stored = %v", first.AlreadyStored)
	}
	if first.Message.Time != fixed.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("time = %s", first.Message.Time)
	}
	if first.Message.Body != "hello" || len(first.Message.To) != 2 || first.Message.To[0] != "bob" {
		t.Fatalf("message = %+v", first.Message)
	}
	other := publish(t, base, "other", "alice", []string{"bob"}, "x", "k")
	if other.Message == nil || other.Message.Seq != 1 {
		t.Fatalf("independent seq = %+v", other.Message)
	}
	retry := publish(t, base, "job", "alice", []string{"bob", "carol"}, "hello", "k")
	if retry.Outcome != OutcomeOK || retry.AlreadyStored == nil || !*retry.AlreadyStored || retry.Message.Seq != 1 {
		t.Fatalf("retry = %+v", retry)
	}
	if retry.Message.Time != first.Message.Time {
		t.Fatalf("retry time changed to %s", retry.Message.Time)
	}
	reordered := publish(t, base, "job", "alice", []string{"carol", "bob"}, "hello", "k")
	if reordered.Outcome != OutcomeRefused || reordered.Reason != ReasonKeyConflict {
		t.Fatalf("reordered to = %+v", reordered)
	}
	changed := publish(t, base, "job", "alice", []string{"bob", "carol"}, "different", "k")
	if changed.Outcome != OutcomeRefused || changed.Reason != ReasonKeyConflict {
		t.Fatalf("different body = %+v", changed)
	}
	sender := publish(t, base, "job", "erin", []string{"bob", "carol"}, "hello", "k")
	if sender.Outcome != OutcomeRefused || sender.Reason != ReasonKeyConflict {
		t.Fatalf("different from = %+v", sender)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if read.Messages == nil || len(*read.Messages) != 1 || (*read.Messages)[0].Body != "hello" {
		t.Fatalf("transcript = %+v", read.Messages)
	}
	empty := publish(t, base, "job", "alice", []string{}, "", "empty")
	if empty.Outcome != OutcomeOK || empty.Message == nil || empty.Message.Body != "" || empty.Message.Seq != 2 {
		t.Fatalf("empty body = %+v", empty)
	}
	tag := publish(t, base, "job", "alice", []string{"bob"}, "<tag>&", "tag")
	if tag.Message == nil || tag.Message.Body != "<tag>&" {
		t.Fatalf("body round trip = %+v", tag.Message)
	}
}

func TestPublishRejectsBrokenRules(t *testing.T) {
	base, _, _, _ := startServer(t, Options{MaxBody: 4})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{"bob"}, "ok", "seed")

	badUTF := append([]byte(`{"conversation":"job","from":"alice","to":[],"body":"`), 0xff)
	badUTF = append(badUTF, []byte(`","idempotency_key":"bad"}`)...)
	badFrom := append([]byte(`{"conversation":"job","from":"al`), 0xff)
	badFrom = append(badFrom, []byte(`ice","to":[],"body":"ok","idempotency_key":"k"}`)...)
	cases := []struct {
		name   string
		body   any
		reason string
	}{
		{"non utf8", badUTF, ReasonRequestUTF8},
		{"non utf8 outside body", badFrom, ReasonRequestUTF8},
		{"oversize", map[string]any{"conversation": "job", "from": "alice", "to": []string{}, "body": "hello", "idempotency_key": "big"}, ReasonBodySize},
		{"missing key", map[string]any{"conversation": "job", "from": "alice", "to": []string{}, "body": "x"}, ReasonKeyRequired},
		{"from all", map[string]any{"conversation": "job", "from": "all", "to": []string{"bob"}, "body": "x", "idempotency_key": "a"}, ReasonFromAll},
		{"all mixed", map[string]any{"conversation": "job", "from": "alice", "to": []string{"all", "bob"}, "body": "x", "idempotency_key": "b"}, ReasonToAllMixed},
		{"missing to", map[string]any{"conversation": "job", "from": "alice", "body": "x", "idempotency_key": "c"}, ReasonToRequired},
		{"string all", map[string]any{"conversation": "job", "from": "alice", "to": "all", "body": "x", "idempotency_key": "d"}, ReasonToShape},
		{"missing conversation", map[string]any{"from": "alice", "to": []string{}, "body": "x", "idempotency_key": "e"}, ReasonConversationRequired},
		{"bad conversation", map[string]any{"conversation": "bad name", "from": "alice", "to": []string{}, "body": "x", "idempotency_key": "f"}, ReasonBadConversation},
		{"bad from", map[string]any{"conversation": "job", "from": "bad from", "to": []string{}, "body": "x", "idempotency_key": "g"}, ReasonFromBad},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := mustPost(t, base, PathPublish, tt.body)
			if res.Outcome != OutcomeRefused || res.Reason != tt.reason {
				t.Fatalf("got outcome=%s reason=%s", res.Outcome, res.Reason)
			}
		})
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if read.Messages == nil || len(*read.Messages) != 1 {
		t.Fatalf("transcript changed: %+v", read.Messages)
	}
	if closeRes := mustPost(t, base, PathClose, map[string]any{"conversation": "job"}); closeRes.Outcome != OutcomeOK {
		t.Fatal(closeRes)
	}
	again := mustPost(t, base, PathCreate, map[string]any{"name": "job"})
	if again.Outcome != OutcomeOK || again.AlreadyExisted == nil || !*again.AlreadyExisted || again.Conversation == nil || again.Conversation.Status != "closed" {
		t.Fatalf("closed name reused: %+v", again)
	}
	fresh := publish(t, base, "job", "alice", []string{"bob"}, "nope", "after")
	if fresh.Outcome != OutcomeOK || fresh.Conversation == nil || fresh.Conversation.Status != "closed" || fresh.Message == nil || fresh.Message.Body != "nope" {
		t.Fatalf("publish after close = %+v", fresh)
	}
}

func TestLongIdempotencyKeyAccepted(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	key := strings.Repeat("k", 1025)
	res := publish(t, base, "job", "alice", []string{"bob"}, "hi", key)
	if res.Outcome != OutcomeOK || res.Message == nil || res.Message.Key != key || res.Message.Seq != 1 {
		t.Fatalf("long key = outcome %s", res.Outcome)
	}
}

func TestDefaultMaxBody(t *testing.T) {
	if DefaultMaxBody != 512*1024 {
		t.Fatalf("default = %d", DefaultMaxBody)
	}
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	exact := publish(t, base, "job", "alice", []string{}, strings.Repeat("a", DefaultMaxBody), "full")
	if exact.Outcome != OutcomeOK || exact.Message == nil || len(exact.Message.Body) != DefaultMaxBody {
		t.Fatalf("exact max = outcome %s", exact.Outcome)
	}
	over := publish(t, base, "job", "alice", []string{}, strings.Repeat("b", DefaultMaxBody+1), "over")
	if over.Outcome != OutcomeRefused || over.Reason != ReasonBodySize {
		t.Fatalf("over max = %+v", over)
	}
}

func TestEscapedBodyWithinMax(t *testing.T) {
	const maxBody = 80_000
	base, _, _, _ := startServer(t, Options{MaxBody: maxBody})
	create(t, base, "job")

	quotes := strings.Repeat("\"", 75_000)
	quoted := publish(t, base, "job", "alice", []string{}, quotes, "quotes")
	if quoted.Outcome != OutcomeOK || quoted.Message == nil || quoted.Message.Body != quotes {
		t.Fatalf("quotes = outcome %s reason %s", quoted.Outcome, quoted.Reason)
	}

	// \x01 is encoded as \u0001, six bytes per body byte.
	controls := strings.Repeat("\x01", 40_000)
	controlled := publish(t, base, "job", "alice", []string{}, controls, "controls")
	if controlled.Outcome != OutcomeOK || controlled.Message == nil || controlled.Message.Body != controls {
		t.Fatalf("controls = outcome %s reason %s", controlled.Outcome, controlled.Reason)
	}

	over := publish(t, base, "job", "alice", []string{}, strings.Repeat("a", maxBody+1), "over")
	if over.Outcome != OutcomeRefused || over.Reason != ReasonBodySize {
		t.Fatalf("over max = outcome %s reason %s", over.Outcome, over.Reason)
	}
}

func TestReadFilterLimitAndNoMutation(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{}, "note", "1")
	publish(t, base, "job", "alice", []string{"carol"}, "side", "2")
	publish(t, base, "job", "alice", []string{"bob"}, "ping", "3")
	publish(t, base, "job", "bob", []string{"bob"}, "self", "4")
	publish(t, base, "job", "alice", []string{"all"}, "broadcast", "5")

	all := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 2})
	if all.Messages == nil || len(*all.Messages) != 2 || (*all.Messages)[0].Seq != 1 || (*all.Messages)[1].Seq != 2 {
		t.Fatalf("limited = %+v", all.Messages)
	}
	rest := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 2, "limit": 10})
	if rest.Messages == nil || len(*rest.Messages) != 3 || (*rest.Messages)[0].Seq != 3 {
		t.Fatalf("rest = %+v", rest.Messages)
	}
	named := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10, "name": "bob"})
	if named.Messages == nil || len(*named.Messages) != 2 || (*named.Messages)[0].Seq != 3 || (*named.Messages)[1].Seq != 5 {
		t.Fatalf("named read = %+v", named.Messages)
	}
	nullName := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10, "name": nil})
	if nullName.Messages == nil || len(*nullName.Messages) != 5 || (*nullName.Messages)[0].Seq != 1 {
		t.Fatalf("null name = %+v", nullName.Messages)
	}
	again := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10, "name": "bob"})
	if len(*again.Messages) != 2 {
		t.Fatalf("second read changed: %+v", again.Messages)
	}
	next := publish(t, base, "job", "alice", []string{"bob"}, "next", "6")
	if next.Message == nil || next.Message.Seq != 6 {
		t.Fatalf("seq after read = %+v", next.Message)
	}
	missing := mustPost(t, base, PathRead, map[string]any{"conversation": "nope", "cursor": 0, "limit": 1})
	if missing.Outcome != OutcomeRefused || missing.Reason != ReasonNotFound {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestWaitStoredSpan(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{}, "note", "1")
	publish(t, base, "job", "alice", []string{"carol"}, "side", "2")
	publish(t, base, "job", "alice", []string{"bob"}, "ping", "3")

	named := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob", "deadline": "1s"})
	if named.Outcome != OutcomeOK || named.MatchSeq == nil || *named.MatchSeq != 3 || named.Messages == nil || len(*named.Messages) != 3 {
		t.Fatalf("named = outcome %s match %v len %d", named.Outcome, named.MatchSeq, messageLen(named))
	}
	unnamed := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "deadline": "0s"})
	if unnamed.Outcome != OutcomeOK || unnamed.MatchSeq == nil || *unnamed.MatchSeq != 1 || len(*unnamed.Messages) != 1 {
		t.Fatalf("unnamed = %+v", unnamed.Messages)
	}
	// A zero deadline does not hide a stored match.
	instant := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 2, "name": "bob", "deadline": "0s"})
	if instant.Outcome != OutcomeOK || instant.MatchSeq == nil || *instant.MatchSeq != 3 {
		t.Fatalf("zero deadline stored match = %s", instant.Outcome)
	}
}

func TestAbsoluteDeadlineKeepsTimeAlreadySpent(t *testing.T) {
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	base, _, _, _ := startServer(t, Options{Clock: func() time.Time { return now }})
	create(t, base, "job")
	past := "2026-10-06T17:59:00+10:00"
	missed := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "name": "bob", "deadline": past,
	})
	if missed.Outcome != OutcomeTimeout || missed.Messages != nil {
		t.Fatalf("past deadline = %s", missed.Outcome)
	}
	publish(t, base, "job", "alice", []string{"bob"}, "ping", "1")
	stored := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "name": "bob", "deadline": past,
	})
	if stored.Outcome != OutcomeOK || stored.MatchSeq == nil || *stored.MatchSeq != 1 {
		t.Fatalf("stored match under a past deadline = %s", stored.Outcome)
	}
	end := now.Add(80 * time.Millisecond).Format(time.RFC3339Nano)
	started := time.Now()
	retry := mustPost(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 1, "name": "bob", "deadline": end,
	})
	elapsed := time.Since(started)
	if retry.Outcome != OutcomeTimeout {
		t.Fatalf("same end time = %s", retry.Outcome)
	}
	if elapsed < 40*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("same end time waited %s", elapsed)
	}
}

func TestWaitSpanContents(t *testing.T) {
	blocked := make(chan struct{}, 2)
	base, _, _, _ := startServer(t, Options{OnBlock: func(string, string) { blocked <- struct{}{} }})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{"carol"}, "side", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	zeroCh := make(chan wire, 1)
	oneCh := make(chan wire, 1)
	go func() {
		res, _, _, err := post(ctx, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob"})
		if err != nil {
			res.Outcome = err.Error()
		}
		zeroCh <- res
	}()
	go func() {
		res, _, _, err := post(ctx, base, PathWait, map[string]any{"conversation": "job", "cursor": 1, "name": "bob"})
		if err != nil {
			res.Outcome = err.Error()
		}
		oneCh <- res
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-blocked:
		case <-time.After(2 * time.Second):
			t.Fatal("did not block")
		}
	}
	publish(t, base, "job", "alice", []string{"bob"}, "ping", "2")
	zero := <-zeroCh
	one := <-oneCh
	if zero.Outcome != OutcomeOK || messageLen(zero) != 2 || zero.MatchSeq == nil || *zero.MatchSeq != 2 {
		t.Fatalf("cursor 0 span = outcome %s len %d", zero.Outcome, messageLen(zero))
	}
	if (*zero.Messages)[0].Body != "side" || (*zero.Messages)[1].Body != "ping" {
		t.Fatalf("span bodies = %+v", *zero.Messages)
	}
	if one.Outcome != OutcomeOK || messageLen(one) != 1 || (*one.Messages)[0].Seq != 2 {
		t.Fatalf("cursor 1 = %+v", one.Messages)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 2 {
		t.Fatalf("waits consumed messages: %d", messageLen(read))
	}
}

func TestWaitTimeoutAndNoDeadline(t *testing.T) {
	blocked := make(chan struct{}, 1)
	base, _, _, _ := startServer(t, Options{OnBlock: func(string, string) { blocked <- struct{}{} }})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{}, "note", "1")

	timed, raw := mustRaw(t, base, PathWait, map[string]any{
		"conversation": "job", "cursor": 0, "name": "bob", "deadline": "150ms",
	})
	if timed.Outcome != OutcomeTimeout || bytes.Contains(raw, []byte(`"messages"`)) {
		t.Fatalf("timeout body = %s", raw)
	}
	still := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(still) != 1 {
		t.Fatalf("timeout wrote or hid messages: %d", messageLen(still))
	}

	select {
	case <-blocked:
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan wire, 1)
	go func() {
		res, _, _, err := post(ctx, base, PathWait, map[string]any{"conversation": "job", "cursor": 1, "name": "bob"})
		if err != nil {
			res.Outcome = err.Error()
		}
		got <- res
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("no-deadline wait did not block")
	}
	select {
	case res := <-got:
		t.Fatalf("returned before publish: %s", res.Outcome)
	case <-time.After(150 * time.Millisecond):
	}
	publish(t, base, "job", "alice", []string{"bob"}, "go", "2")
	select {
	case res := <-got:
		if res.Outcome != OutcomeOK || res.MatchSeq == nil || *res.MatchSeq != 2 {
			t.Fatalf("woke with %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stayed blocked after publish")
	}
}

func TestCloseRetryAndTails(t *testing.T) {
	blocked := make(chan struct{}, 1)
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	first := publish(t, base, "job", "alice", []string{"bob"}, "ping", "k")
	side := publish(t, base, "job", "alice", []string{"carol"}, "side", "side")
	if first.Message == nil || side.Message == nil {
		t.Fatal("publish failed")
	}

	// Match accepted before close, observed after close.
	closed := mustPost(t, base, PathClose, map[string]any{"conversation": "job"})
	if closed.Outcome != OutcomeOK || closed.Conversation == nil || closed.Conversation.Status != "closed" {
		t.Fatalf("close = %+v", closed)
	}
	again := mustPost(t, base, PathClose, map[string]any{"conversation": "job"})
	if again.Outcome != OutcomeOK {
		t.Fatalf("second close = %+v", again)
	}
	matched := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob"})
	if matched.Outcome != OutcomeOK || matched.MatchSeq == nil || *matched.MatchSeq != 1 || messageLen(matched) != 1 {
		t.Fatalf("match before close = %s len %d", matched.Outcome, messageLen(matched))
	}
	tail := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 1, "name": "bob", "deadline": "0s"})
	if tail.Outcome != OutcomeTimeout || tail.Messages != nil {
		t.Fatalf("named wait with no match = outcome %s messages nil=%v", tail.Outcome, tail.Messages == nil)
	}
	unnamed := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 2, "deadline": "0s"})
	if unnamed.Outcome != OutcomeTimeout || unnamed.Messages != nil {
		t.Fatalf("unnamed caught up = %s messages nil=%v", unnamed.Outcome, unnamed.Messages == nil)
	}
	next := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 1})
	if next.Outcome != OutcomeOK || messageLen(next) != 1 {
		t.Fatalf("unnamed with a remaining message = %s", next.Outcome)
	}
	fresh := publish(t, base, "job", "alice", []string{"bob"}, "more", "new")
	if fresh.Outcome != OutcomeOK || fresh.Conversation == nil || fresh.Conversation.Status != "closed" || fresh.Message == nil || fresh.Message.Seq != 3 {
		t.Fatalf("new key = %+v", fresh)
	}
	retry := publish(t, base, "job", "alice", []string{"bob"}, "ping", "k")
	if retry.Outcome != OutcomeOK || retry.AlreadyStored == nil || !*retry.AlreadyStored || retry.Message.Seq != 1 || retry.Conversation == nil || retry.Conversation.Status != "closed" {
		t.Fatalf("retry after close = %+v", retry)
	}
	different := publish(t, base, "job", "alice", []string{"bob"}, "other", "k")
	if different.Outcome != OutcomeRefused || different.Reason != ReasonKeyConflict {
		t.Fatalf("conflict after close = %+v", different)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 3 || (*read.Messages)[2].Body != "more" {
		t.Fatalf("transcript len = %d", messageLen(read))
	}
	list := mustPost(t, base, PathList, map[string]any{})
	if list.Conversations == nil || (*list.Conversations)[0].Status != "closed" {
		t.Fatalf("list = %+v", list.Conversations)
	}

	// Close does not finish a blocked wait. A later matching publish does.
	base2, _, _, _ := startServer(t, Options{OnBlock: func(string, string) { blocked <- struct{}{} }})
	create(t, base2, "job")
	publish(t, base2, "job", "alice", []string{"carol"}, "only", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan wire, 1)
	go func() {
		res, _, _, err := post(ctx, base2, PathWait, map[string]any{
			"conversation": "job", "cursor": 0, "name": "bob", "deadline": "5s",
		})
		if err != nil {
			res.Outcome = err.Error()
		}
		got <- res
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("did not block")
	}
	if res := mustPost(t, base2, PathClose, map[string]any{"conversation": "job"}); res.Outcome != OutcomeOK {
		t.Fatal(res)
	}
	select {
	case res := <-got:
		t.Fatalf("close woke the waiter: %s", res.Outcome)
	case <-time.After(150 * time.Millisecond):
	}
	publish(t, base2, "job", "alice", []string{"bob"}, "go", "2")
	select {
	case res := <-got:
		if res.Outcome != OutcomeOK || res.MatchSeq == nil || *res.MatchSeq != 2 || messageLen(res) != 2 {
			t.Fatalf("woke with %s match %v len %d", res.Outcome, res.MatchSeq, messageLen(res))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stayed blocked after publish")
	}
}

func TestRestartPreservesStore(t *testing.T) {
	base, dir, st, stop := startServer(t, Options{})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{"bob"}, "hello", "k")
	if res := mustPost(t, base, PathClose, map[string]any{"conversation": "job"}); res.Outcome != OutcomeOK {
		t.Fatal(res)
	}
	stop()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st2.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, ln, st2, Options{Logger: slog.New(slog.DiscardHandler)})
	}()
	waitReady(t, ln.Addr().String(), errCh)
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("restarted server did not stop")
		}
	})
	base = "http://" + ln.Addr().String()
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 1 || (*read.Messages)[0].Body != "hello" {
		t.Fatalf("restart read = %+v", read.Messages)
	}
	list := mustPost(t, base, PathList, map[string]any{})
	if list.Conversations == nil || (*list.Conversations)[0].Status != "closed" {
		t.Fatalf("restart status = %+v", list.Conversations)
	}
	// Nothing is still blocked in the new process. The cursor is caught up, so the deadline ends the wait.
	timed := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 1, "name": "bob", "deadline": "100ms"})
	if timed.Outcome != OutcomeTimeout {
		t.Fatalf("caught-up wait = %s", timed.Outcome)
	}
}

func TestDropWaitLeavesStore(t *testing.T) {
	blocked := make(chan struct{}, 1)
	base, _, _, _ := startServer(t, Options{OnBlock: func(string, string) { blocked <- struct{}{} }})
	create(t, base, "job")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, _, err := post(ctx, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob"})
		errCh <- err
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("did not block")
	}
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("dropped wait returned a response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dropped wait did not return")
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 0 {
		t.Fatalf("drop changed the store: %d", messageLen(read))
	}
	publish(t, base, "job", "alice", []string{"bob"}, "later", "k")
	got := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob", "deadline": "1s"})
	if got.Outcome != OutcomeOK || messageLen(got) != 1 {
		t.Fatalf("later wait = %s len %d", got.Outcome, messageLen(got))
	}
}

func TestUnavailableWritesNothing(t *testing.T) {
	base, dir, st, _ := startServer(t, Options{})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{"bob"}, "hello", "k")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	res := mustPost(t, base, PathPublish, map[string]any{
		"conversation": "job", "from": "alice", "to": []string{"bob"}, "body": "next", "idempotency_key": "n",
	})
	if res.Outcome != OutcomeUnavailable || res.Reason != ReasonUnavailable || res.Message != nil {
		t.Fatalf("unavailable = %+v", res)
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st2.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	_, msgs, err := st2.Transcript(context.Background(), "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "hello" {
		t.Fatalf("transcript after unavailable = %+v", msgs)
	}
}

func TestAllWakesOthersNotSender(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	publish(t, base, "job", "alice", []string{"all"}, "hey", "k")
	bob := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bob", "deadline": "1s"})
	if bob.Outcome != OutcomeOK || messageLen(bob) != 1 {
		t.Fatalf("bob = %s", bob.Outcome)
	}
	alice := mustPost(t, base, PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "alice", "deadline": "100ms"})
	if alice.Outcome != OutcomeTimeout {
		t.Fatalf("sender woken: %s", alice.Outcome)
	}
}

func TestUnknownOperation(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res := mustPost(t, base, "/v1/nope", map[string]any{})
	if res.Outcome != OutcomeRefused || res.Reason != ReasonUnknownOp {
		t.Fatalf("unknown = %+v", res)
	}
}

func TestRefusedEdges(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	cases := []struct {
		name   string
		path   string
		body   any
		reason string
	}{
		{"negative read cursor", PathRead, map[string]any{"conversation": "job", "cursor": -1, "limit": 1}, ReasonCursorRange},
		{"negative wait cursor", PathWait, map[string]any{"conversation": "job", "cursor": -1}, ReasonCursorRange},
		{"negative deadline", PathWait, map[string]any{"conversation": "job", "cursor": 0, "deadline": "-1s"}, ReasonDeadline},
		{"bad deadline", PathWait, map[string]any{"conversation": "job", "cursor": 0, "deadline": "tomorrow"}, ReasonDeadline},
		{"bad wait name", PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": "bad name"}, ReasonBadName},
		{"bad read name", PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 1, "name": "bad name"}, ReasonBadName},
		{"empty wait name", PathWait, map[string]any{"conversation": "job", "cursor": 0, "name": ""}, ReasonBadName},
		{"empty read name", PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 1, "name": ""}, ReasonBadName},
		{"bad wait conversation", PathWait, map[string]any{"conversation": "bad name", "cursor": 0}, ReasonBadConversation},
		{"bad read conversation", PathRead, map[string]any{"conversation": "bad name", "cursor": 0, "limit": 1}, ReasonBadConversation},
		{"bad close conversation", PathClose, map[string]any{"conversation": "bad name"}, ReasonBadConversation},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := mustPost(t, base, tt.path, tt.body)
			if res.Outcome != OutcomeRefused || res.Reason != tt.reason {
				t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
			}
		})
	}
}

func TestOversizedRequestIsRefused(t *testing.T) {
	base, _, _, _ := startServer(t, Options{MaxBody: 32})
	create(t, base, "job")
	raw := append([]byte(`{"conversation":"job","from":"alice","to":[],"body":"ok","idempotency_key":"k","pad":"`), bytes.Repeat([]byte("x"), 80_000)...)
	raw = append(raw, '"', '}')
	res := mustPost(t, base, PathPublish, raw)
	if res.Outcome != OutcomeRefused || res.Reason != ReasonRequestSize {
		t.Fatalf("outcome=%s reason=%s", res.Outcome, res.Reason)
	}
	read := mustPost(t, base, PathRead, map[string]any{"conversation": "job", "cursor": 0, "limit": 10})
	if messageLen(read) != 0 {
		t.Fatalf("transcript len = %d", messageLen(read))
	}
}

func messageLen(res wire) int {
	if res.Messages == nil {
		return 0
	}
	return len(*res.Messages)
}
