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
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func TestAttendPrintsOwnerAskErrorAndDone(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	createRoster(t, ctx, addr, "job", "alpha")
	launched := "launched"
	if res, err := client.Do(ctx, addr, "member", "", client.MemberRequest{
		Conversation: "job", Name: "alpha", Status: &launched,
	}); err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("launch %v %+v", err, res)
	}
	before := memberRows(t, ctx, addr, "job")

	publishAs(t, ctx, addr, "job", "alpha", "say", []string{"owner"}, "side-remark", "say-1")
	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"bob"}, "not-for-owner", "done-bob")
	ask := publishAs(t, ctx, addr, "job", "alpha", "ask", []string{"owner", "bravo"}, "need a decision", "ask-1")

	proc := startAttend(t, "attend", "--address", addr, "--conversation", "job")
	got := proc.waitFor(t, "need a decision")
	if strings.Contains(got, "side-remark") || strings.Contains(got, "not-for-owner") || strings.Contains(got, "kind: say") || strings.Contains(got, "kind: done") {
		t.Fatalf("printed the span\n%s", got)
	}
	if !strings.Contains(got, "kind: ask\n") || !strings.Contains(got, "from: alpha\n") || !strings.Contains(got, "name: job\n") || strings.Contains(got, "status:") {
		t.Fatalf("ask\n%s", got)
	}
	if !strings.Contains(got, fmt.Sprintf("match_seq: %d\n", ask.Seq)) {
		t.Fatalf("match seq\n%s", got)
	}
	ownerAt := strings.Index(got, "to: owner\n")
	bravoAt := strings.Index(got, "to: bravo\n")
	if ownerAt < 0 || bravoAt < 0 || ownerAt > bravoAt {
		t.Fatalf("to order\n%s", got)
	}

	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"bob"}, "still-not-owner", "done-bob-2")
	publishAs(t, ctx, addr, "job", "runner", "error", []string{"owner"}, "member timed out", "err-1")
	got = proc.waitFor(t, "member timed out")
	if strings.Contains(got, "still-not-owner") || strings.Contains(got, "not-for-owner") || strings.Contains(got, "side-remark") {
		t.Fatalf("printed a join\n%s", got)
	}
	if !strings.Contains(got, "kind: error\n") {
		t.Fatalf("error\n%s", got)
	}

	body := "all done\nnow"
	done := publishAs(t, ctx, addr, "job", "carol", "done", []string{"owner"}, body, "done-1")
	out := proc.wait(t)
	if proc.err != nil || proc.stderr.Len() != 0 {
		t.Fatalf("exit %v stderr %s\n%s", proc.err, proc.stderr.String(), out)
	}
	frame := fmt.Sprintf("body <<%d\n%s\n", len(body), body)
	if !strings.Contains(out, frame) || !strings.Contains(out, "from: carol\n") || !strings.Contains(out, fmt.Sprintf("match_seq: %d\n", done.Seq)) {
		t.Fatalf("done\n%s", out)
	}
	if strings.Count(out, "outcome: ok\n") != 3 || strings.Count(out, "kind: done\n") != 1 {
		t.Fatalf("results\n%s", out)
	}
	if strings.Contains(out, "side-remark") || strings.Contains(out, "not-for-owner") || strings.Contains(out, "still-not-owner") || strings.Contains(out, "kind: say") {
		t.Fatalf("printed a skipped message\n%s", out)
	}
	after := memberRows(t, ctx, addr, "job")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("members before %+v after %+v", before, after)
	}
	msgs := transcript(t, ctx, addr, "job")
	if len(msgs) != 6 {
		t.Fatalf("transcript %d", len(msgs))
	}
}

func TestAttendRestartsFromZeroUnlessCursorIsSet(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	create(t, ctx, addr, "job")
	publishAs(t, ctx, addr, "job", "alpha", "say", []string{"owner"}, "say-body", "say-1")
	ask := publishAs(t, ctx, addr, "job", "alpha", "ask", []string{"owner"}, "ask-body", "ask-1")
	publishAs(t, ctx, addr, "job", "runner", "error", []string{"owner"}, "error-body", "err-1")
	done := publishAs(t, ctx, addr, "job", "alpha", "done", []string{"owner"}, "done-body", "done-1")

	args := []string{"attend", "--address", addr, "--conversation", "job"}
	first, stderr, err := runAttend(t, args...)
	if err != nil || stderr != "" {
		t.Fatalf("first %v %s\n%s", err, stderr, first)
	}
	if !strings.Contains(first, "ask-body") || !strings.Contains(first, "error-body") || !strings.Contains(first, "done-body") || strings.Contains(first, "say-body") {
		t.Fatalf("first\n%s", first)
	}
	second, stderr, err := runAttend(t, args...)
	if err != nil || stderr != "" || second != first {
		t.Fatalf("second %v %s\n%s\n%s", err, stderr, second, first)
	}
	third, stderr, err := runAttend(t, "attend", "--address", addr, "--conversation", "job", "--cursor", strconv.FormatInt(ask.Seq, 10))
	if err != nil || stderr != "" || strings.Contains(third, "ask-body") || strings.Contains(third, "say-body") || !strings.Contains(third, "error-body") || !strings.Contains(third, "done-body") {
		t.Fatalf("cursor %v %s\n%s", err, stderr, third)
	}
	proc := startAttend(t, "attend", "--address", addr, "--conversation", "job", "--cursor", strconv.FormatInt(done.Seq, 10))
	publishAs(t, ctx, addr, "job", "alpha", "ask", []string{"owner"}, "later-ask", "ask-2")
	later := proc.waitFor(t, "later-ask")
	if strings.Contains(later, "done-body") || strings.Contains(later, "ask-body") || strings.Contains(later, "error-body") || strings.Contains(later, "say-body") {
		t.Fatalf("cursor reprinted\n%s", later)
	}
	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"owner"}, "later-done", "done-2")
	out := proc.wait(t)
	if proc.err != nil || strings.Contains(out, "done-body") || !strings.Contains(out, "later-done") {
		t.Fatalf("later %v\n%s", proc.err, out)
	}
}

func TestAttendDoneToAllExitsZero(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	create(t, ctx, addr, "job")
	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"all"}, "broadcast-done", "d1")
	out, stderr, err := runAttend(t, "attend", "--address", addr, "--conversation", "job")
	if err != nil || stderr != "" || !strings.Contains(out, "kind: done\n") || !strings.Contains(out, "to: all\n") || !strings.Contains(out, "broadcast-done") {
		t.Fatalf("err %v stderr %s\n%s", err, stderr, out)
	}
}

func TestAttendUsagePrintsNoStdout(t *testing.T) {
	isolateAttend(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !strings.Contains(err.Error(), "use of closed") {
			t.Errorf("close listen: %v", err)
		}
	})
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close conn: %v", err)
		}
		accepted <- struct{}{}
	}()
	addr := ln.Addr().String()
	var stdout, stderr bytes.Buffer
	err = execute(context.Background(), []string{"attend", "--address", addr}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "conversation") {
		t.Fatalf("err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
	t.Setenv("HOTSEAT_CONVERSATION", "job")
	stdout.Reset()
	stderr.Reset()
	err = execute(context.Background(), []string{"attend", "--address", addr, "--conversation", ""}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "conversation") {
		t.Fatalf("empty flag err %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
	select {
	case <-accepted:
		t.Fatal("dialled")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestAttendRefusedWaitExitsNonZero(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	stdout, stderr, err := runAttend(t, "attend", "--address", addr, "--conversation", "missing")
	if !ReportedFailure(err) || stderr != "" || !strings.Contains(stdout, "outcome: refused\n") || !strings.Contains(stdout, "reason: "+bus.ReasonNotFound+"\n") {
		t.Fatalf("err %v stderr %q\n%s", err, stderr, stdout)
	}
}

func TestAttendEnvSelectsConversationAndAddress(t *testing.T) {
	isolateAttend(t)
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	create(t, ctx, addr, "job")
	create(t, ctx, addr, "other")
	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"owner"}, "from-job", "j1")
	publishAs(t, ctx, addr, "other", "alpha", "done", []string{"owner"}, "from-other", "o1")

	t.Setenv("HOTSEAT_CONVERSATION", "other")
	t.Setenv("HOTSEAT_ADDRESS", addr)
	out, stderr, err := runAttend(t, "attend")
	if err != nil || stderr != "" || !strings.Contains(out, "from-other") || strings.Contains(out, "from-job") {
		t.Fatalf("env %v %s\n%s", err, stderr, out)
	}
	out, stderr, err = runAttend(t, "attend", "--conversation", "job")
	if err != nil || stderr != "" || !strings.Contains(out, "from-job") || strings.Contains(out, "from-other") {
		t.Fatalf("flag %v %s\n%s", err, stderr, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOTSEAT_ADDRESS", closed)
	out, stderr, err = runAttend(t, "attend", "--address", addr, "--conversation", "job")
	if err != nil || stderr != "" || !strings.Contains(out, "from-job") || strings.Contains(out, "connection_failure") {
		t.Fatalf("address flag %v %s\n%s", err, stderr, out)
	}
}

func TestAttendTokenIsNotPrinted(t *testing.T) {
	isolateAttend(t)
	const token = "attend-token-7f3c"
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, stop := startBus(t)
	t.Cleanup(stop)
	ctx := context.Background()
	create(t, ctx, addr, "job")
	publishAs(t, ctx, addr, "job", "alpha", "ask", []string{"owner"}, "ask-body", "a1")
	publishAs(t, ctx, addr, "job", "alpha", "done", []string{"owner"}, "done-body", "d1")
	t.Setenv("HOTSEAT_TOKEN_FILE", path)
	t.Setenv("HOTSEAT_CONVERSATION", "job")
	t.Setenv("HOTSEAT_ADDRESS", addr)
	out, stderr, err := runAttend(t, "attend")
	if err != nil || stderr != "" || strings.Contains(out+stderr, token) || !strings.Contains(out, "ask-body") {
		t.Fatalf("env token %v %s\n%s", err, stderr, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	out, stderr, err = runAttend(t, "attend", "--address", closed, "--conversation", "job", "--token-file", path)
	if !ReportedFailure(err) || stderr != "" || !strings.Contains(out, "outcome: connection_failure\n") || strings.Contains(out+stderr, token) {
		t.Fatalf("failed call %v %s\n%s", err, stderr, out)
	}
}

func isolateAttend(t *testing.T) {
	t.Helper()
	t.Setenv("HOTSEAT_CONVERSATION", "")
	t.Setenv("HOTSEAT_ADDRESS", "")
	t.Setenv("HOTSEAT_TOKEN_FILE", "")
}

func runAttend(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := execute(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func publishAs(t *testing.T, ctx context.Context, addr, conv, from, kind string, to []string, body, key string) client.Message {
	t.Helper()
	res, err := client.Do(ctx, addr, "publish", "", client.PublishRequest{
		Conversation: conv,
		From:         from,
		To:           to,
		Body:         body,
		TxID:         key,
		Kind:         kind,
	})
	if err != nil || res.Outcome != bus.OutcomeOK || res.Message == nil {
		t.Fatalf("publish %s: %v %+v", key, err, res)
	}
	return *res.Message
}

func createRoster(t *testing.T, ctx context.Context, addr, name, seat string) {
	t.Helper()
	res, err := client.Do(ctx, addr, "create", "", client.CreateRequest{
		Name: name, Seat: seat, Roster: []string{seat},
	})
	if err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("create %s: %v %+v", name, err, res)
	}
}

func memberRows(t *testing.T, ctx context.Context, addr, conv string) []client.Member {
	t.Helper()
	res, err := client.Do(ctx, addr, "members", "", client.MembersRequest{Conversation: conv})
	if err != nil || res.Outcome != bus.OutcomeOK || res.Members == nil {
		t.Fatalf("members %v %+v", err, res)
	}
	return *res.Members
}

func transcript(t *testing.T, ctx context.Context, addr, conv string) []client.Message {
	t.Helper()
	res, err := client.Do(ctx, addr, "read", "", client.ReadRequest{
		Conversation: conv, Cursor: 0, Limit: 50,
	})
	if err != nil || res.Outcome != bus.OutcomeOK || res.Messages == nil {
		t.Fatalf("read %v %+v", err, res)
	}
	return *res.Messages
}

type attendProc struct {
	cancel   context.CancelFunc
	done     chan struct{}
	copyDone chan struct{}
	err      error
	stderr   *bytes.Buffer
	out      *lockedBuf
}

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startAttend(t *testing.T, args ...string) *attendProc {
	t.Helper()
	pr, pw := io.Pipe()
	proc := &attendProc{
		done:     make(chan struct{}),
		copyDone: make(chan struct{}),
		stderr:   &bytes.Buffer{},
		out:      &lockedBuf{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	proc.cancel = cancel
	go func() {
		if _, err := io.Copy(proc.out, pr); err != nil {
			t.Errorf("copy attend output: %v", err)
		}
		close(proc.copyDone)
	}()
	go func() {
		err := execute(ctx, args, pw, proc.stderr)
		if cerr := pw.Close(); err == nil {
			err = cerr
		}
		proc.err = err
		close(proc.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-proc.copyDone:
		case <-time.After(5 * time.Second):
			t.Errorf("attend output did not close")
		}
	})
	return proc
}

func (p *attendProc) waitFor(t *testing.T, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := p.out.String(); strings.Contains(got, needle) {
			return got
		}
		select {
		case <-p.done:
			select {
			case <-p.copyDone:
			case <-time.After(time.Second):
			}
			t.Fatalf("attend ended before %q: %v\nstderr %s\n%s", needle, p.err, p.stderr.String(), p.out.String())
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("output missing %q:\n%s", needle, p.out.String())
		}
	}
}

func (p *attendProc) wait(t *testing.T) string {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.cancel()
		t.Fatal("attend did not return")
	}
	select {
	case <-p.copyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("attend output did not close")
	}
	return p.out.String()
}
