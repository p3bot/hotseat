// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"encoding/json"
	"testing"
)

func sendBody(chat, from, txid string, re any) map[string]any {
	body := map[string]any{
		"chat": chat, "from": from, "to": []string{"bob"},
		"body": "b " + txid, "txid": txid, "kind": "say",
	}
	if re != nil {
		body["re"] = re
	}
	return body
}

func TestReplyThatIsNotAPositiveIntegerIsRefused(t *testing.T) {
	base, _, st, _ := startServer(t, Options{})
	create(t, base, "job")
	for _, re := range []string{"0", "-3", "1.5", `"1"`, "1e3", "true", "[1]"} {
		body := []byte(`{"chat":"job","from":"alice","to":[],"body":"x","txid":"a","kind":"say","re":` + re + `}`)
		res := mustPost(t, base, PathSend, body)
		if res.Outcome != OutcomeRefused || res.Reason != ReasonReRange || res.Message != nil {
			t.Fatalf("re %s: %+v", re, res)
		}
	}
	if n := transcriptLen(t, st, "job"); n != 0 {
		t.Fatalf("stored %d", n)
	}
}

func TestReplyThatIsNotAnEarlierMessageOfTheChatIsRefused(t *testing.T) {
	base, _, st, _ := startServer(t, Options{})
	create(t, base, "job")
	create(t, base, "other")
	first := mustPost(t, base, PathSend, sendBody("job", "alice", "1", nil))
	other := mustPost(t, base, PathSend, sendBody("other", "alice", "2", nil))
	if first.Message == nil || other.Message == nil {
		t.Fatalf("seed %+v %+v", first, other)
	}
	cases := []struct {
		name string
		re   int64
	}{
		{"message of another chat", other.Message.Seq},
		{"seq that does not exist", 99},
		{"own seq", other.Message.Seq + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustPost(t, base, PathSend, sendBody("job", "alice", "3", tc.re))
			if res.Outcome != OutcomeRefused || res.Reason != ReasonReTarget || res.Message != nil {
				t.Fatalf("re %d: %+v", tc.re, res)
			}
		})
	}
	if n := transcriptLen(t, st, "job"); n != 1 {
		t.Fatalf("stored %d", n)
	}
}

func TestReplyIsPartOfSendContent(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	first := mustPost(t, base, PathSend, sendBody("job", "alice", "1", nil))
	second := mustPost(t, base, PathSend, sendBody("job", "alice", "2", nil))
	reply := mustPost(t, base, PathSend, sendBody("job", "alice", "3", first.Message.Seq))
	if reply.Outcome != OutcomeOK || reply.Message.Re == nil || *reply.Message.Re != first.Message.Seq {
		t.Fatalf("reply %+v", reply)
	}
	again := mustPost(t, base, PathSend, sendBody("job", "alice", "3", first.Message.Seq))
	if again.Outcome != OutcomeOK || again.AlreadyStored == nil || !*again.AlreadyStored || again.Message.Seq != reply.Message.Seq {
		t.Fatalf("same re %+v", again)
	}
	for _, tc := range []struct {
		name string
		txid string
		re   any
	}{
		{"different re", "3", second.Message.Seq},
		{"absent against present", "3", nil},
		{"present against absent", "2", first.Message.Seq},
	} {
		res := mustPost(t, base, PathSend, sendBody("job", "alice", tc.txid, tc.re))
		if res.Outcome != OutcomeRefused || res.Reason != ReasonKeyConflict {
			t.Fatalf("%s: %+v", tc.name, res)
		}
	}
}

func TestNullReplyRespondsToNothing(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	body := sendBody("job", "alice", "1", nil)
	body["re"] = nil
	res := mustPost(t, base, PathSend, body)
	if res.Outcome != OutcomeOK || res.Message == nil || res.Message.Re != nil {
		t.Fatalf("%+v", res)
	}
	again := mustPost(t, base, PathSend, sendBody("job", "alice", "1", nil))
	if again.Outcome != OutcomeOK || again.AlreadyStored == nil || !*again.AlreadyStored {
		t.Fatalf("absent after null %+v", again)
	}
}

func TestEveryMessageObjectHasRe(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	create(t, base, "job")
	_, raw := mustRaw(t, base, PathSend, sendBody("job", "alice", "1", nil))
	assertRe(t, "send", raw, "message", []any{nil})
	_, raw = mustRaw(t, base, PathSend, sendBody("job", "alice", "2", 1))
	assertRe(t, "send reply", raw, "message", []any{float64(1)})

	both := []any{nil, float64(1)}
	_, raw = mustRaw(t, base, PathRead, map[string]any{"chat": "job", "cursor": 0, "limit": 10})
	assertRe(t, "read", raw, "messages", both)
	_, raw = mustRaw(t, base, PathWait, map[string]any{"chat": "job", "cursor": 1})
	assertRe(t, "wait", raw, "messages", []any{float64(1)})
	_, raw = mustRaw(t, base, PathWatch, map[string]any{"cursor": 0, "limit": 10})
	assertRe(t, "watch", raw, "messages", both)
	_, raw = mustRaw(t, base, PathMessages, map[string]any{"limit": 10})
	assertRe(t, "messages", raw, "messages", []any{float64(1), nil})
}

// assertRe decodes raw JSON so a missing re key is told apart from null.
func assertRe(t *testing.T, op string, raw []byte, field string, want []any) {
	t.Helper()
	var res map[string]json.RawMessage
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	var msgs []map[string]any
	if field == "message" {
		var one map[string]any
		if err := json.Unmarshal(res[field], &one); err != nil {
			t.Fatalf("%s: %v body=%s", op, err, raw)
		}
		msgs = append(msgs, one)
	} else if err := json.Unmarshal(res[field], &msgs); err != nil {
		t.Fatalf("%s: %v body=%s", op, err, raw)
	}
	if len(msgs) != len(want) {
		t.Fatalf("%s: %d messages, want %d: %s", op, len(msgs), len(want), raw)
	}
	for i, m := range msgs {
		got, ok := m["re"]
		if !ok {
			t.Fatalf("%s: message %d has no re key: %s", op, i, raw)
		}
		if got != want[i] {
			t.Fatalf("%s: message %d re = %v, want %v", op, i, got, want[i])
		}
	}
}
