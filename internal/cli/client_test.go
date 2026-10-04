// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/store"
)

func TestNonCommandDoesNotDialOrCreateStore(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	for _, args := range [][]string{nil, {"nope"}, {"create"}, {"publish"}, {"read"}, {"wait"}, {"close"}} {
		err := execute(context.Background(), args, io.Discard, io.Discard)
		if err == nil {
			t.Fatalf("%v succeeded", args)
		}
	}
	select {
	case <-accepted:
		t.Fatal("accepted a connection")
	case <-time.After(150 * time.Millisecond):
	}
	assertEmptyDir(t, wd)
}

func TestClientCommandsExit(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	dir := t.TempDir()
	home := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"CGO_ENABLED=0",
			"HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
			"XDG_STATE_HOME="+filepath.Join(home, "state"),
			"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr: %s", stderr.String())
		}
		var res client.Result
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatalf("stdout %s: %v", stdout.Bytes(), err)
		}
		if res.Outcome != bus.OutcomeOK {
			t.Fatalf("%v outcome %s %s", args, res.Outcome, stdout.String())
		}
	}
	base := []string{"--address", addr}
	run(append([]string{"create"}, append(base, "--name", "job")...)...)
	run(append([]string{"publish"}, append(base, "--conversation", "job", "--from", "alice", "--to", "bob", "--body", "hello", "--idempotency-key", "1")...)...)
	run(append([]string{"read"}, append(base, "--conversation", "job", "--cursor", "0", "--limit", "10")...)...)
	run(append([]string{"wait"}, append(base, "--conversation", "job", "--cursor", "0")...)...)
	run(append([]string{"list"}, base...)...)
	run(append([]string{"close"}, append(base, "--conversation", "job")...)...)
	assertEmptyDir(t, dir)
	assertEmptyDir(t, home)
}

func TestPublishIdempotencyAndNoKeyFile(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	wd, home := isolateHome(t)
	ctx := context.Background()
	args := []string{
		"--address", addr,
		"--conversation", "dup",
		"--from", "alice",
		"--to", "carol",
		"--to", "bob",
		"--body", "<b>",
		"--idempotency-key", "caller-key-1",
	}
	create(t, ctx, addr, "dup")
	first, raw := runClient(t, ctx, append([]string{"publish"}, args...)...)
	if first.Outcome != bus.OutcomeOK || first.AlreadyStored == nil || *first.AlreadyStored {
		t.Fatalf("first %s", raw)
	}
	if first.Message == nil || first.Message.Key != "caller-key-1" || first.Message.Body != "<b>" {
		t.Fatalf("message %+v", first.Message)
	}
	if len(first.Message.To) != 2 || first.Message.To[0] != "carol" || first.Message.To[1] != "bob" {
		t.Fatalf("to %v", first.Message.To)
	}
	if !bytes.Contains([]byte(raw), []byte(`<b>`)) {
		t.Fatalf("stdout escaped the body: %s", raw)
	}
	second, _ := runClient(t, ctx, append([]string{"publish"}, args...)...)
	if second.Outcome != bus.OutcomeOK || second.AlreadyStored == nil || !*second.AlreadyStored {
		t.Fatalf("second %+v", second)
	}
	if second.Message == nil || second.Message.Seq != first.Message.Seq || second.Message.Key != "caller-key-1" {
		t.Fatalf("second message %+v", second.Message)
	}
	got := readAll(t, ctx, addr, "dup", 0)
	if len(got) != 1 || got[0].Seq != first.Message.Seq {
		t.Fatalf("transcript %+v", got)
	}
	assertEmptyDir(t, wd)
	assertEmptyDir(t, home)
}

func TestWaitCursorIsNotStored(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	wd, home := isolateHome(t)
	ctx := context.Background()
	create(t, ctx, addr, "seen")
	publish(t, ctx, addr, "seen", "alice", []string{"bob"}, "hello", "k")
	args := []string{"wait", "--address", addr, "--conversation", "seen", "--cursor", "0"}
	first, raw1 := runClient(t, ctx, args...)
	_, raw2 := runClient(t, ctx, args...)
	if raw1 != raw2 {
		t.Fatalf("results differ\n%s\n%s", raw1, raw2)
	}
	if first.Outcome != bus.OutcomeOK || first.Messages == nil || len(*first.Messages) != 1 || (*first.Messages)[0].Body != "hello" {
		t.Fatalf("wait %s", raw1)
	}
	assertEmptyDir(t, wd)
	assertEmptyDir(t, home)
}

func TestNamedUnnamedWaitAndTimeout(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "wake")
	publish(t, ctx, addr, "wake", "alice", []string{"carol"}, "skip", "k1")
	publish(t, ctx, addr, "wake", "alice", []string{"bob"}, "hit", "k2")
	publish(t, ctx, addr, "wake", "alice", []string{"bob"}, "later", "k3")

	named, _ := runClient(t, ctx, "wait", "--address", addr, "--conversation", "wake", "--cursor", "0", "--name", "bob")
	if named.Outcome != bus.OutcomeOK || named.MatchSeq == nil || *named.MatchSeq != 2 {
		t.Fatalf("named %+v", named)
	}
	bodies := messageBodies(t, named)
	if len(bodies) != 2 || bodies[0] != "skip" || bodies[1] != "hit" {
		t.Fatalf("named messages %v", bodies)
	}
	again, _ := runClient(t, ctx, "wait", "--address", addr, "--conversation", "wake", "--cursor", "0", "--name", "bob")
	if again.MatchSeq == nil || *again.MatchSeq != 2 || len(messageBodies(t, again)) != 2 {
		t.Fatalf("repeat %+v", again)
	}

	unnamed, _ := runClient(t, ctx, "wait", "--address", addr, "--conversation", "wake", "--cursor", "0")
	one := messageBodies(t, unnamed)
	if unnamed.Outcome != bus.OutcomeOK || len(one) != 1 || one[0] != "skip" {
		t.Fatalf("unnamed %+v", unnamed)
	}

	create(t, ctx, addr, "quiet")
	timed, raw := runClient(t, ctx, "wait", "--address", addr, "--conversation", "quiet", "--cursor", "0", "--deadline", "0s")
	if timed.Outcome != bus.OutcomeTimeout || timed.Messages != nil {
		t.Fatalf("timeout %+v raw %s", timed, raw)
	}
	if bytes.Contains([]byte(raw), []byte(`"messages"`)) {
		t.Fatalf("timeout included messages: %s", raw)
	}
}

func TestReadPageAndName(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "page")
	publish(t, ctx, addr, "page", "alice", []string{"carol"}, "one", "k1")
	publish(t, ctx, addr, "page", "alice", []string{"bob"}, "two", "k2")
	publish(t, ctx, addr, "page", "bob", nil, "three", "k3")
	publish(t, ctx, addr, "page", "alice", []string{"all"}, "four", "k4")
	publish(t, ctx, addr, "page", "alice", []string{"bob", "carol"}, "five", "k5")

	page, _ := runClient(t, ctx, "read", "--address", addr, "--conversation", "page", "--cursor", "0", "--limit", "2")
	if page.Outcome != bus.OutcomeOK {
		t.Fatalf("page %+v", page)
	}
	got := messageBodies(t, page)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("page %v", got)
	}
	last := (*page.Messages)[len(*page.Messages)-1].Seq
	if last != 2 {
		t.Fatalf("last seq %d", last)
	}
	rest := readAll(t, ctx, addr, "page", last)
	restBodies := make([]string, len(rest))
	for i, m := range rest {
		restBodies[i] = m.Body
	}
	if len(restBodies) != 3 || restBodies[0] != "three" || restBodies[1] != "four" || restBodies[2] != "five" {
		t.Fatalf("rest %v", restBodies)
	}
	if len(rest[0].To) != 0 {
		t.Fatalf("empty to = %v", rest[0].To)
	}

	named, _ := runClient(t, ctx, "read", "--address", addr, "--conversation", "page", "--cursor", "0", "--limit", "10", "--name", "bob")
	nb := messageBodies(t, named)
	if len(nb) != 3 || nb[0] != "two" || nb[1] != "four" || nb[2] != "five" {
		t.Fatalf("named %v", nb)
	}
}

func TestClosedNamedWaitReturnsTail(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "tail")
	publish(t, ctx, addr, "tail", "alice", []string{"bob"}, "hit", "k1")
	publish(t, ctx, addr, "tail", "alice", []string{"carol"}, "rest", "k2")
	before := readAll(t, ctx, addr, "tail", 0)
	closed, _ := runClient(t, ctx, "close", "--address", addr, "--conversation", "tail")
	if closed.Outcome != bus.OutcomeOK || closed.Conversation == nil || closed.Conversation.Status != "closed" {
		t.Fatalf("close %+v", closed)
	}
	if closed.Message != nil {
		t.Fatal("close appended a message field")
	}
	after := readAll(t, ctx, addr, "tail", 0)
	if len(after) != len(before) {
		t.Fatalf("close changed the transcript %d -> %d", len(before), len(after))
	}
	res, raw := runClient(t, ctx, "wait", "--address", addr, "--conversation", "tail", "--cursor", "1", "--name", "bob")
	if res.Outcome != bus.OutcomeClosed || res.MatchSeq != nil {
		t.Fatalf("wait %s", raw)
	}
	bodies := messageBodies(t, res)
	if len(bodies) != 1 || bodies[0] != "rest" {
		t.Fatalf("tail %v", bodies)
	}
}

func TestRefusedNamesTheRule(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "once")
	again, _ := runClient(t, ctx, "create", "--address", addr, "--name", "once")
	if again.Outcome != bus.OutcomeRefused || again.Reason != bus.ReasonNameInUse {
		t.Fatalf("create %+v", again)
	}
	create(t, ctx, addr, "shut")
	runClient(t, ctx, "close", "--address", addr, "--conversation", "shut")
	pub, _ := runClient(t, ctx, "publish", "--address", addr, "--conversation", "shut", "--from", "alice", "--body", "nope", "--idempotency-key", "k")
	if pub.Outcome != bus.OutcomeRefused || pub.Reason != bus.ReasonClosed {
		t.Fatalf("publish %+v", pub)
	}
	empty, _ := runClient(t, ctx, "read", "--address", addr, "--conversation", "shut", "--cursor", "0", "--limit", "5", "--name", "")
	if empty.Outcome != bus.OutcomeRefused || empty.Reason != bus.ReasonBadName {
		t.Fatalf("empty name %+v", empty)
	}
}

func TestNoListenerIsConnectionFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	res, raw := runClient(t, context.Background(), "list", "--address", addr)
	if res.Outcome != client.OutcomeConnectionFailure || !strings.Contains(res.Reason, "connection failed: "+addr+":") || !strings.Contains(res.Reason, "refused") {
		t.Fatalf("result %s", raw)
	}
	if res.Outcome == bus.OutcomeTimeout || res.Outcome == bus.OutcomeRefused {
		t.Fatalf("collapsed outcome %s", res.Outcome)
	}
}

func TestStoppingBusDuringWaitIsNotTimeout(t *testing.T) {
	addr, parked, stop := startServing(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "block")

	errCh := make(chan error, 1)
	outCh := make(chan string, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		err := execute(ctx, []string{
			"wait", "--address", addr, "--conversation", "block", "--cursor", "0", "--name", "bob", "--deadline", "30s",
		}, &stdout, &stderr)
		if err == nil && stderr.Len() != 0 {
			err = errString(stderr.String())
		}
		outCh <- stdout.String()
		errCh <- err
	}()
	waitParked(t, parked)
	stop()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
		raw := <-outCh
		var res client.Result
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("stdout %s: %v", raw, err)
		}
		if res.Outcome != client.OutcomeConnectionFailure || !strings.HasPrefix(res.Reason, "connection dropped: "+addr+":") {
			t.Fatalf("outcome %s reason %s", res.Outcome, res.Reason)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("wait did not return after the bus stopped")
	}
}

func TestInterruptParkedWaitExits130(t *testing.T) {
	bin := buildBinary(t)
	addr, parked, stop := startServing(t)
	defer stop()
	create(t, context.Background(), addr, "block")

	cmd := exec.Command(bin, "wait", "--address", addr, "--conversation", "block", "--cursor", "0", "--name", "bob", "--deadline", "30s")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitParked(t, parked)
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("parked wait did not exit on interrupt")
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 130 {
		t.Fatalf("exit %v stdout %s stderr %s", waitErr, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "timeout") {
		t.Fatalf("stdout %s stderr %s", stdout.String(), stderr.String())
	}
}

func TestDefaultAddressIsTheLocalBus(t *testing.T) {
	probe, err := net.Listen("tcp", bus.DefaultListen)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	stop := startBusOn(t, bus.DefaultListen)
	defer stop()
	res, raw := runClient(t, context.Background(), "create", "--name", "job")
	if res.Outcome != bus.OutcomeOK || res.Conversation == nil || res.Conversation.Name != "job" || res.Conversation.Status != "open" {
		t.Fatalf("create %s", raw)
	}
	cmd := newCreateCmd()
	addr, err := cmd.Flags().GetString("address")
	if err != nil {
		t.Fatal(err)
	}
	if addr != bus.DefaultListen {
		t.Fatalf("address default %s", addr)
	}
}

func TestUnsetNameAndDeadlineAreOmitted(t *testing.T) {
	var mu sync.Mutex
	var got [][]byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, append([]byte(nil), body...))
		if r.Header.Get("Authorization") != "" {
			auth = r.Header.Get("Authorization")
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"outcome":"ok","messages":[]}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	ctx := context.Background()
	runClient(t, ctx, "read", "--address", addr, "--conversation", "job", "--cursor", "0", "--limit", "5")
	runClient(t, ctx, "read", "--address", addr, "--conversation", "job", "--cursor", "0", "--limit", "5", "--name", "")
	runClient(t, ctx, "wait", "--address", addr, "--conversation", "job", "--cursor", "4")
	runClient(t, ctx, "wait", "--address", addr, "--conversation", "job", "--cursor", "4", "--name", "bob", "--deadline", "30s")

	mu.Lock()
	defer mu.Unlock()
	if auth != "" {
		t.Fatalf("sent a token %s", auth)
	}
	if len(got) != 4 {
		t.Fatalf("calls %d", len(got))
	}
	assertAbsent(t, got[0], "name")
	assertString(t, got[1], "name", "")
	assertAbsent(t, got[2], "name")
	assertAbsent(t, got[2], "deadline")
	assertString(t, got[3], "name", "bob")
	assertString(t, got[3], "deadline", "30s")
}

func TestPublishBodyFileAndStdinCarryTheBusMaximum(t *testing.T) {
	bin := buildBinary(t)
	addr, stop := startBus(t)
	defer stop()
	runBinOK(t, bin, nil, "create", "--address", addr, "--name", "big")

	fileBody := strings.Repeat("a", bus.DefaultMaxBody)
	file := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(file, []byte(fileBody), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile := runBinOK(t, bin, nil, "publish", "--address", addr,
		"--conversation", "big", "--from", "alice", "--body-file", file, "--idempotency-key", "file")
	if fromFile.Message == nil || fromFile.Message.Body != fileBody {
		n := 0
		if fromFile.Message != nil {
			n = len(fromFile.Message.Body)
		}
		t.Fatalf("file body length %d", n)
	}

	piped := strings.Repeat("b", bus.DefaultMaxBody)
	fromStdin := runBinOK(t, bin, strings.NewReader(piped), "publish", "--address", addr,
		"--conversation", "big", "--from", "alice", "--body-file", "-", "--idempotency-key", "pipe")
	if fromStdin.Message == nil || fromStdin.Message.Body != piped {
		n := 0
		if fromStdin.Message != nil {
			n = len(fromStdin.Message.Body)
		}
		t.Fatalf("stdin body length %d", n)
	}
}

func TestOversizeBodyFileIsRefused(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "big")
	file := filepath.Join(t.TempDir(), "over")
	if err := os.WriteFile(file, bytes.Repeat([]byte("c"), bus.DefaultMaxBody+1), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "big", "--from", "alice", "--body-file", file, "--idempotency-key", "over")
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonBodySize {
		t.Fatalf("outcome %s reason %s", res.Outcome, res.Reason)
	}
}

func TestLiteralHyphenBodyIsNotStdin(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "hyphen")
	res, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "hyphen", "--from", "alice", "--body=-", "--idempotency-key", "dash")
	if res.Outcome != bus.OutcomeOK || res.Message == nil || res.Message.Body != "-" {
		got := ""
		if res.Message != nil {
			got = res.Message.Body
		}
		t.Fatalf("outcome %s body %q", res.Outcome, got)
	}
}

func TestPublishBodyChoiceDoesNotDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()
	addr := ln.Addr().String()
	base := []string{"publish", "--address", addr, "--conversation", "job", "--from", "alice", "--idempotency-key", "k"}
	if err := execute(context.Background(), base, io.Discard, io.Discard); err == nil {
		t.Fatal("missing body succeeded")
	}
	both := append(append([]string{}, base...), "--body", "hi", "--body-file", "note.txt")
	if err := execute(context.Background(), both, io.Discard, io.Discard); err == nil {
		t.Fatal("both body sources succeeded")
	}
	missingFile := append(append([]string{}, base...), "--body-file", filepath.Join(t.TempDir(), "absent"))
	if err := execute(context.Background(), missingFile, io.Discard, io.Discard); err == nil {
		t.Fatal("absent file succeeded")
	}
	select {
	case <-accepted:
		t.Fatal("dialled while choosing a body")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestInvalidUTF8BodyIsRefusedWithoutDialling(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"outcome":"ok"}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(file, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"publish", "--address", addr, "--conversation", "job", "--from", "alice", "--idempotency-key", "k"}
	fromFile, _ := runClient(t, ctx, append(base, "--body-file", file)...)
	if fromFile.Outcome != bus.OutcomeRefused || fromFile.Reason != bus.ReasonBodyUTF8 {
		t.Fatalf("file outcome %s reason %s", fromFile.Outcome, fromFile.Reason)
	}
	fromInline, _ := runClient(t, ctx, append(base, "--body", string([]byte{0xff}))...)
	if fromInline.Outcome != bus.OutcomeRefused || fromInline.Reason != bus.ReasonBodyUTF8 {
		t.Fatalf("inline outcome %s reason %s", fromInline.Outcome, fromInline.Reason)
	}
	var stdout, stderr bytes.Buffer
	err := executeIO(ctx, append(base, "--body-file", "-"), bytes.NewReader([]byte{0xff}), &stdout, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("stdin exit %v stderr %q stdout %s", err, stderr.String(), stdout.String())
	}
	var fromStdin client.Result
	if err := json.Unmarshal(stdout.Bytes(), &fromStdin); err != nil {
		t.Fatal(err)
	}
	if fromStdin.Outcome != bus.OutcomeRefused || fromStdin.Reason != bus.ReasonBodyUTF8 {
		t.Fatalf("stdin outcome %s reason %s", fromStdin.Outcome, fromStdin.Reason)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestInvalidUTF8KeyIsRefusedWithoutDialling(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"outcome":"ok"}`)
	}))
	defer srv.Close()
	res, _ := runClient(t, context.Background(), "publish", "--address", srv.Listener.Addr().String(),
		"--conversation", "job", "--from", "alice", "--body", "hi",
		"--idempotency-key", string([]byte{0xff}))
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonKeyUTF8 {
		t.Fatalf("outcome %s reason %s", res.Outcome, res.Reason)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestValidUTF8BodiesStillPublish(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	create(t, ctx, addr, "text")
	empty, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "text", "--from", "alice", "--body", "", "--idempotency-key", "empty")
	if empty.Outcome != bus.OutcomeOK || empty.Message == nil || empty.Message.Body != "" {
		t.Fatalf("empty %+v", empty.Message)
	}
	const cafe = "café"
	inline, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "text", "--from", "alice", "--body", cafe, "--idempotency-key", "cafe")
	if inline.Outcome != bus.OutcomeOK || inline.Message == nil || inline.Message.Body != cafe {
		got := ""
		if inline.Message != nil {
			got = inline.Message.Body
		}
		t.Fatalf("inline outcome %s body %q", inline.Outcome, got)
	}
	file := filepath.Join(t.TempDir(), "cafe")
	if err := os.WriteFile(file, []byte(cafe), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "text", "--from", "alice", "--body-file", file, "--idempotency-key", "file")
	if fromFile.Outcome != bus.OutcomeOK || fromFile.Message == nil || fromFile.Message.Body != cafe {
		got := ""
		if fromFile.Message != nil {
			got = fromFile.Message.Body
		}
		t.Fatalf("file outcome %s body %q", fromFile.Outcome, got)
	}
	blank := filepath.Join(t.TempDir(), "blank")
	if err := os.WriteFile(blank, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fromBlank, _ := runClient(t, ctx, "publish", "--address", addr,
		"--conversation", "text", "--from", "alice", "--body-file", blank, "--idempotency-key", "blank")
	if fromBlank.Outcome != bus.OutcomeOK || fromBlank.Message == nil || fromBlank.Message.Body != "" {
		t.Fatalf("blank file %+v", fromBlank.Message)
	}
}

func TestListShowsStatus(t *testing.T) {
	addr, stop := startBus(t)
	defer stop()
	ctx := context.Background()
	empty, _ := runClient(t, ctx, "list", "--address", addr)
	if empty.Outcome != bus.OutcomeOK || empty.Conversations == nil || len(*empty.Conversations) != 0 {
		t.Fatalf("empty %+v", empty)
	}
	create(t, ctx, addr, "b")
	create(t, ctx, addr, "a")
	runClient(t, ctx, "close", "--address", addr, "--conversation", "a")
	listed, _ := runClient(t, ctx, "list", "--address", addr)
	if listed.Conversations == nil || len(*listed.Conversations) != 2 {
		t.Fatalf("list %+v", listed)
	}
	cs := *listed.Conversations
	if cs[0].Name != "a" || cs[0].Status != "closed" || cs[1].Name != "b" || cs[1].Status != "open" {
		t.Fatalf("list %+v", cs)
	}
}

func runBinOK(t *testing.T, bin string, stdin io.Reader, args ...string) client.Result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var res client.Result
	unmarshalErr := json.Unmarshal(stdout.Bytes(), &res)
	if err != nil || stderr.Len() != 0 || unmarshalErr != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("%v: exit %v stderr %q outcome %s reason %s decode %v", args, err, stderr.String(), res.Outcome, res.Reason, unmarshalErr)
	}
	return res
}

// startServing runs a bus and closes parked when a wait is blocked.
func startServing(t *testing.T) (addr string, parked <-chan struct{}, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	var once sync.Once
	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.Serve(ctx, ln, st, bus.Options{
			Logger: slog.New(slog.DiscardHandler),
			OnBlock: func(string, string) {
				once.Do(func() { close(ready) })
			},
		})
	}()
	addr = ln.Addr().String()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case serveErr := <-errCh:
			t.Fatalf("bus stopped: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("bus did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Errorf("bus: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("bus did not stop")
			}
			if err := st.Close(); err != nil {
				t.Errorf("close store: %v", err)
			}
		})
	}
	return addr, ready, stop
}

func waitParked(t *testing.T, parked <-chan struct{}) {
	t.Helper()
	select {
	case <-parked:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not park")
	}
}

func startBus(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr, startBusOn(t, addr)
}

func startBusOn(t *testing.T, addr string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runBus(ctx, t.TempDir(), addr, bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("bus did not listen on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Errorf("bus: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("bus did not stop")
			}
		})
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	bin := filepath.Join(t.TempDir(), "hotseat")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/hotseat")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func isolateHome(t *testing.T) (string, string) {
	t.Helper()
	wd := t.TempDir()
	home := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return wd, home
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("%s is not empty: %v", dir, names)
	}
}

func runClient(t *testing.T, ctx context.Context, args ...string) (client.Result, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := execute(ctx, args, &stdout, &stderr)
	if err != nil {
		t.Fatalf("%v: %v\nstderr: %s\nstdout: %s", args, err, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: %s", stderr.String())
	}
	var res client.Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout %s: %v", stdout.Bytes(), err)
	}
	return res, stdout.String()
}

func create(t *testing.T, ctx context.Context, addr, name string) {
	t.Helper()
	res, raw := runClient(t, ctx, "create", "--address", addr, "--name", name)
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("create %s: %s", name, raw)
	}
}

func publish(t *testing.T, ctx context.Context, addr, conv, from string, to []string, body, key string) {
	t.Helper()
	args := []string{
		"publish", "--address", addr, "--conversation", conv, "--from", from,
		"--body", body, "--idempotency-key", key,
	}
	for _, name := range to {
		args = append(args, "--to", name)
	}
	res, raw := runClient(t, ctx, args...)
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("publish %s: %s", key, raw)
	}
}

func readAll(t *testing.T, ctx context.Context, addr, conv string, cursor int64) []client.Message {
	t.Helper()
	res, raw := runClient(t, ctx, "read", "--address", addr, "--conversation", conv, "--cursor", itoa(cursor), "--limit", "50")
	if res.Outcome != bus.OutcomeOK || res.Messages == nil {
		t.Fatalf("read %s", raw)
	}
	return *res.Messages
}

func messageBodies(t *testing.T, res client.Result) []string {
	t.Helper()
	if res.Messages == nil {
		t.Fatal("messages missing")
	}
	out := make([]string, len(*res.Messages))
	for i, m := range *res.Messages {
		out[i] = m.Body
	}
	return out
}

func assertAbsent(t *testing.T, raw []byte, key string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m[key]; ok {
		t.Fatalf("%s was present in %s", key, raw)
	}
}

func assertString(t *testing.T, raw []byte, key, want string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	got, ok := m[key]
	if !ok {
		t.Fatalf("%s missing from %s", key, raw)
	}
	var s string
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatal(err)
	}
	if s != want {
		t.Fatalf("%s = %q", key, s)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

type errString string

func (e errString) Error() string { return string(e) }
