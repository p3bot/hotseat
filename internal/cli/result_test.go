// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/p3bot/hotseat/internal/client"
)

func TestWriteResultLayout(t *testing.T) {
	stored := false
	res := client.Result{
		Outcome:       "ok",
		AlreadyStored: &stored,
		Conversation:  &client.Conversation{Name: "job", Status: "open"},
		Message: &client.Message{
			Seq:  1,
			Time: "2026-10-08T07:00:00Z",
			From: "alice",
			To:   []string{"bob", "carol"},
			Body: "hello",
			TxID: "1",
		},
	}
	var buf bytes.Buffer
	if err := writeResult(&buf, res); err != nil {
		t.Fatal(err)
	}
	const want = `outcome: ok
already_stored: false
conversation:
name: job
status: open
message:
seq: 1
time: 2026-10-08T07:00:00Z
from: alice
to: bob
to: carol
txid: 1
body <<5
hello
`
	if buf.String() != want {
		t.Fatalf("layout:\n%s", buf.String())
	}
	if json.Valid(buf.Bytes()) {
		t.Fatal("layout is JSON")
	}
	got := parseStdout(t, buf.Bytes())
	if got.Outcome != res.Outcome || got.AlreadyStored == nil || *got.AlreadyStored || got.Conversation == nil || got.Message == nil {
		t.Fatalf("parsed %+v", got)
	}
	if got.Conversation.Name != "job" || got.Conversation.Status != "open" {
		t.Fatalf("conversation %+v", got.Conversation)
	}
	if got.Message.Seq != 1 || got.Message.From != "alice" || got.Message.Body != "hello" || got.Message.TxID != "1" {
		t.Fatalf("message %+v", got.Message)
	}
	if len(got.Message.To) != 2 || got.Message.To[0] != "bob" || got.Message.To[1] != "carol" {
		t.Fatalf("to %v", got.Message.To)
	}
}

func TestWriteResultFramesAwkwardText(t *testing.T) {
	existed := true
	seq := int64(4)
	msgs := []client.Message{
		{Seq: 1, Time: "t", From: "alice", To: []string{}, Body: "", TxID: "empty"},
		{Seq: 2, Time: "t", From: "alice", To: []string{}, Body: "<b>", TxID: "html"},
		{Seq: 3, Time: "t", From: "alice", To: []string{}, Body: "outcome: ok", TxID: "field"},
		{Seq: 4, Time: "t\n2", From: "alice", To: []string{"bob"}, Body: "one\noutcome: ok\n", TxID: "a\nb"},
	}
	convs := []client.Conversation{}
	res := client.Result{
		Outcome:        "ok",
		Reason:         "line\nkey: value",
		AlreadyExisted: &existed,
		MatchSeq:       &seq,
		Messages:       &msgs,
		Conversations:  &convs,
	}
	var buf bytes.Buffer
	if err := writeResult(&buf, res); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if json.Valid(raw) || bytes.Contains(raw, []byte(`\u003c`)) || !bytes.Contains(raw, []byte("<b>")) {
		t.Fatalf("raw %s", raw)
	}
	got := parseStdout(t, raw)
	if got.Reason != res.Reason || got.AlreadyExisted == nil || !*got.AlreadyExisted || got.MatchSeq == nil || *got.MatchSeq != 4 {
		t.Fatalf("scalars %+v", got)
	}
	if got.Conversations == nil || len(*got.Conversations) != 0 || got.Messages == nil || len(*got.Messages) != len(msgs) {
		t.Fatalf("lists %+v", got)
	}
	for i, msg := range msgs {
		out := (*got.Messages)[i]
		if out.Seq != msg.Seq || out.Time != msg.Time || out.From != msg.From || out.Body != msg.Body || out.TxID != msg.TxID || len(out.To) != len(msg.To) {
			t.Fatalf("message %d %+v", i, out)
		}
	}
	if (*got.Messages)[3].To[0] != "bob" {
		t.Fatalf("to %v", (*got.Messages)[3].To)
	}
}

func TestWriteResultOmitsAbsentFields(t *testing.T) {
	var buf bytes.Buffer
	if err := writeResult(&buf, client.Result{Outcome: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "outcome: timeout\n" {
		t.Fatalf("timeout %q", buf.String())
	}
	got := parseStdout(t, buf.Bytes())
	if got.Outcome != "timeout" || got.Reason != "" || got.Messages != nil || got.Message != nil || got.MatchSeq != nil {
		t.Fatalf("parsed %+v", got)
	}

	buf.Reset()
	empty := []client.Message{}
	if err := writeResult(&buf, client.Result{Outcome: "ok", Messages: &empty}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "outcome: ok\nmessages:\n" {
		t.Fatalf("read %q", buf.String())
	}
	got = parseStdout(t, buf.Bytes())
	if got.Messages == nil || len(*got.Messages) != 0 || got.Message != nil {
		t.Fatalf("empty read %+v", got)
	}
}

func TestWriteResultOneLineFrameMarkerStaysValue(t *testing.T) {
	res := client.Result{
		Outcome: "refused",
		Reason:  "note: see <<2",
		Message: &client.Message{
			Seq:  1,
			Time: "t",
			From: "alice",
			To:   []string{"bob"},
			Body: "hello <<5",
			TxID: "<<5",
		},
	}
	var buf bytes.Buffer
	if err := writeResult(&buf, res); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "reason: note: see <<2\n") || !strings.Contains(raw, "txid: <<5\n") || !strings.Contains(raw, "body <<9\nhello <<5\n") {
		t.Fatalf("layout:\n%s", raw)
	}
	got := parseStdout(t, buf.Bytes())
	if got.Reason != res.Reason || got.Message == nil || got.Message.TxID != "<<5" || got.Message.Body != "hello <<5" || len(got.Message.To) != 1 || got.Message.To[0] != "bob" {
		t.Fatalf("parsed reason %q msg %+v", got.Reason, got.Message)
	}
}

func parseStdout(t *testing.T, raw []byte) client.Result {
	t.Helper()
	if json.Valid(raw) {
		t.Fatalf("stdout is JSON: %s", raw)
	}
	res, err := parseResult(raw)
	if err != nil {
		t.Fatalf("stdout %s: %v", raw, err)
	}
	return res
}

func parseResult(raw []byte) (client.Result, error) {
	c := textCursor{raw: raw}
	var res client.Result
	outcome, err := c.word("outcome")
	if err != nil {
		return res, err
	}
	if outcome == "" {
		return res, errors.New("result text missing outcome")
	}
	res.Outcome = outcome
	if f, ok, err := c.ifField("reason"); err != nil {
		return res, err
	} else if ok {
		res.Reason = f.value
	}
	res.AlreadyStored, err = c.optionalBool("already_stored")
	if err != nil {
		return res, err
	}
	res.AlreadyExisted, err = c.optionalBool("already_existed")
	if err != nil {
		return res, err
	}
	res.MatchSeq, err = c.optionalInt("match_seq")
	if err != nil {
		return res, err
	}
	if ok, err := c.ifHeader("conversation"); err != nil {
		return res, err
	} else if ok {
		conv, err := c.conversation()
		if err != nil {
			return res, err
		}
		res.Conversation = &conv
	}
	if ok, err := c.ifHeader("conversations"); err != nil {
		return res, err
	} else if ok {
		list := []client.Conversation{}
		for {
			if c.i >= len(c.raw) {
				break
			}
			f, err := c.peek()
			if err != nil {
				return res, err
			}
			if f.key != "name" {
				break
			}
			conv, err := c.conversation()
			if err != nil {
				return res, err
			}
			list = append(list, conv)
		}
		res.Conversations = &list
	}
	if ok, err := c.ifHeader("message"); err != nil {
		return res, err
	} else if ok {
		msg, err := c.messageFields()
		if err != nil {
			return res, err
		}
		res.Message = &msg
	}
	if ok, err := c.ifHeader("messages"); err != nil {
		return res, err
	} else if ok {
		list := []client.Message{}
		for {
			if c.i >= len(c.raw) {
				break
			}
			f, err := c.peek()
			if err != nil {
				return res, err
			}
			if f.key != "message" {
				break
			}
			if _, err := c.next(); err != nil {
				return res, err
			}
			msg, err := c.messageFields()
			if err != nil {
				return res, err
			}
			list = append(list, msg)
		}
		res.Messages = &list
	}
	if c.i != len(c.raw) {
		return res, errors.New("result text has trailing bytes")
	}
	return res, nil
}

type textCursor struct {
	raw []byte
	i   int
}

type textField struct {
	key    string
	value  string
	framed bool
}

func (c *textCursor) conversation() (client.Conversation, error) {
	name, err := c.value("name")
	if err != nil {
		return client.Conversation{}, err
	}
	status, err := c.value("status")
	if err != nil {
		return client.Conversation{}, err
	}
	return client.Conversation{Name: name, Status: status}, nil
}

func (c *textCursor) messageFields() (client.Message, error) {
	var msg client.Message
	seq, err := c.integer("seq")
	if err != nil {
		return msg, err
	}
	msg.Seq = seq
	msg.Time, err = c.value("time")
	if err != nil {
		return msg, err
	}
	msg.From, err = c.value("from")
	if err != nil {
		return msg, err
	}
	msg.To = []string{}
	for {
		if c.i >= len(c.raw) {
			return msg, errors.New("result text message ended before the body")
		}
		f, err := c.peek()
		if err != nil {
			return msg, err
		}
		if f.key != "to" {
			break
		}
		if _, err := c.next(); err != nil {
			return msg, err
		}
		msg.To = append(msg.To, f.value)
	}
	msg.TxID, err = c.value("txid")
	if err != nil {
		return msg, err
	}
	msg.Body, err = c.framed("body")
	return msg, err
}

func (c *textCursor) optionalBool(key string) (*bool, error) {
	f, ok, err := c.ifField(key)
	if err != nil || !ok {
		return nil, err
	}
	if f.framed {
		return nil, fmt.Errorf("result text field %s is framed", key)
	}
	switch f.value {
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	default:
		return nil, fmt.Errorf("result text bool %s", key)
	}
}

func (c *textCursor) optionalInt(key string) (*int64, error) {
	f, ok, err := c.ifField(key)
	if err != nil || !ok {
		return nil, err
	}
	if f.framed {
		return nil, fmt.Errorf("result text field %s is framed", key)
	}
	n, err := strconv.ParseInt(f.value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("result text int %s", key)
	}
	return &n, nil
}

func (c *textCursor) word(key string) (string, error) {
	f, err := c.next()
	if err != nil {
		return "", fmt.Errorf("result text want %s: %w", key, err)
	}
	if f.key != key || f.framed {
		return "", fmt.Errorf("result text want %s", key)
	}
	return f.value, nil
}

func (c *textCursor) value(key string) (string, error) {
	f, err := c.next()
	if err != nil {
		return "", fmt.Errorf("result text want %s: %w", key, err)
	}
	if f.key != key {
		return "", fmt.Errorf("result text want %s", key)
	}
	return f.value, nil
}

func (c *textCursor) integer(key string) (int64, error) {
	f, err := c.next()
	if err != nil {
		return 0, fmt.Errorf("result text want %s: %w", key, err)
	}
	if f.key != key || f.framed {
		return 0, fmt.Errorf("result text want %s", key)
	}
	n, err := strconv.ParseInt(f.value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("result text int %s", key)
	}
	return n, nil
}

func (c *textCursor) framed(key string) (string, error) {
	f, err := c.next()
	if err != nil {
		return "", fmt.Errorf("result text want %s: %w", key, err)
	}
	if f.key != key || !f.framed {
		return "", fmt.Errorf("result text want framed %s", key)
	}
	return f.value, nil
}

func (c *textCursor) ifHeader(key string) (bool, error) {
	f, ok, err := c.ifField(key)
	if err != nil || !ok {
		return false, err
	}
	if f.framed || f.value != "" {
		return false, fmt.Errorf("result text want %s header", key)
	}
	return true, nil
}

func (c *textCursor) ifField(key string) (textField, bool, error) {
	if c.i >= len(c.raw) {
		return textField{}, false, nil
	}
	f, err := c.peek()
	if err == io.EOF {
		return textField{}, false, nil
	}
	if err != nil {
		return textField{}, false, err
	}
	if f.key != key {
		return textField{}, false, nil
	}
	got, err := c.next()
	return got, true, err
}

func (c *textCursor) peek() (textField, error) {
	save := *c
	f, err := c.next()
	*c = save
	return f, err
}

func (c *textCursor) next() (textField, error) {
	line, err := c.line()
	if err != nil {
		return textField{}, err
	}
	if key, val, ok := strings.Cut(line, ": "); ok {
		if key == "" || strings.ContainsAny(key, " \t") {
			return textField{}, fmt.Errorf("result text has a bad line %q", line)
		}
		return textField{key: key, value: val}, nil
	}
	if key, n, ok := cutFrame(line); ok {
		val, err := c.take(n)
		if err != nil {
			return textField{}, err
		}
		return textField{key: key, value: val, framed: true}, nil
	}
	if strings.HasSuffix(line, ":") {
		key := strings.TrimSuffix(line, ":")
		if key != "" && !strings.Contains(key, ":") {
			return textField{key: key}, nil
		}
	}
	return textField{}, fmt.Errorf("result text has a bad line %q", line)
}

func cutFrame(line string) (string, int, bool) {
	key, rest, ok := strings.Cut(line, " <<")
	if !ok || key == "" || strings.ContainsAny(key, " \t") {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return "", 0, false
	}
	return key, n, true
}

func (c *textCursor) line() (string, error) {
	if c.i >= len(c.raw) {
		return "", io.EOF
	}
	n := bytes.IndexByte(c.raw[c.i:], '\n')
	if n < 0 {
		return "", errors.New("result text ended mid-line")
	}
	line := string(c.raw[c.i : c.i+n])
	c.i += n + 1
	return line, nil
}

func (c *textCursor) take(n int) (string, error) {
	if n < 0 || c.i > len(c.raw) || n > len(c.raw)-c.i {
		return "", errors.New("result text ended inside a field")
	}
	s := string(c.raw[c.i : c.i+n])
	c.i += n
	if c.i >= len(c.raw) || c.raw[c.i] != '\n' {
		return "", errors.New("result text field missing separator")
	}
	c.i++
	return s, nil
}
