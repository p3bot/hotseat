// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/store"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hotseat-run-fake")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin = filepath.Join(dir, "fake")
	cmd := exec.Command("go", "build", "-o", fakeBin, "github.com/p3bot/hotseat/internal/run/fake")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build fake: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type call struct {
	Time  string   `json:"time"`
	Kind  string   `json:"kind"`
	Argv  []string `json:"argv"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
}

type harness struct {
	t    *testing.T
	addr string
	dir  string
	plan planFile
}

type planFile struct {
	ID       string              `json:"id"`
	IDExit   int                 `json:"idExit"`
	IDStderr string              `json:"idStderr"`
	Body     string              `json:"body"`
	BodyExit int                 `json:"bodyExit"`
	DenyPerm map[string]bool     `json:"denyPerm,omitempty"`
	Locator  map[string]textFile `json:"locator,omitempty"`
	Launch   map[string]runFile  `json:"launch,omitempty"`
	Scopes   string              `json:"scopes"`
	TkExit   int                 `json:"tkExit"`
	TkStderr string              `json:"tkStderr"`
}

type textFile struct {
	Out    string `json:"out"`
	Stderr string `json:"stderr,omitempty"`
	Exit   int    `json:"exit"`
}

type runFile struct {
	Exit     int    `json:"exit"`
	ExitOn   string `json:"exitOn,omitempty"`
	Sleep    string `json:"sleep"`
	JSON     string `json:"json"`
	Stderr   string `json:"stderr,omitempty"`
	Register bool   `json:"register"`
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessClock(t, nil)
}

func newHarnessClock(t *testing.T, clock func() time.Time) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.Serve(ctx, ln, st, bus.Options{
			Logger: slog.New(slog.DiscardHandler),
			Clock:  clock,
		})
	}()
	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		<-errCh
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	h := &harness{
		t:    t,
		addr: ln.Addr().String(),
		dir:  dir,
		plan: planFile{
			ID:     "tasks:hotseat/review/ticket",
			Body:   "Review the ticket.\n",
			Scopes: "foo\t/tmp/tickets\t" + filepath.Join(dir, "code") + "\ttk-driven\n",
		},
	}
	if err := os.Mkdir(filepath.Join(dir, "code"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOTSEAT_FAKE_DIR", dir)
	t.Setenv("HOTSEAT_FAKE_BUS", h.addr)
	return h
}

func (h *harness) save() {
	h.t.Helper()
	raw, err := json.Marshal(h.plan)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "plan.json"), raw, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) base() Config {
	return Config{
		Task:          "review/ticket",
		TaskSet:       true,
		Agents:        "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1",
		AgentsSet:     true,
		Seat:          "alpha",
		SeatSet:       true,
		Ticket:        "foo-x234",
		TicketSet:     true,
		Address:       h.addr,
		Dir:           h.dir,
		StartBin:      fakeBin,
		TkBin:         fakeBin,
		Timeout:       5 * time.Second,
		TimeoutSet:    true,
		Permission:    DefaultPermission,
		PermissionSet: true,
	}
}

func (h *harness) run(ctx context.Context, cfg Config) (string, string, error) {
	h.t.Helper()
	h.save()
	var stdout, stderr bytes.Buffer
	err := Execute(ctx, cfg, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func (h *harness) calls() []call {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.dir, "calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		h.t.Fatal(err)
	}
	var out []call
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var c call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (h *harness) do(op string, body any) client.Result {
	h.t.Helper()
	res, err := client.Do(context.Background(), h.addr, op, "", body)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

var txSeq atomic.Int64

func (h *harness) publish(conv, from, kind, body string, to []string) client.Message {
	h.t.Helper()
	res := h.do("publish", client.PublishRequest{
		Conversation: conv,
		From:         from,
		To:           to,
		Body:         body,
		TxID:         fmt.Sprintf("t%d", txSeq.Add(1)),
		Kind:         kind,
	})
	if res.Outcome != bus.OutcomeOK || res.Message == nil {
		h.t.Fatalf("publish %s: %s", kind, res.Reason)
	}
	return *res.Message
}

func (h *harness) messages(conv string) []client.Message {
	h.t.Helper()
	res := h.do("read", client.ReadRequest{Conversation: conv, Cursor: 0, Limit: 2000})
	if res.Outcome != bus.OutcomeOK || res.Messages == nil {
		h.t.Fatalf("read %s", res.Reason)
	}
	return *res.Messages
}

func (h *harness) conv(name string) (client.Conversation, bool) {
	h.t.Helper()
	res := h.do("list", struct{}{})
	if res.Outcome != bus.OutcomeOK || res.Conversations == nil {
		h.t.Fatalf("list %s", res.Reason)
	}
	for _, c := range *res.Conversations {
		if c.Name == name {
			return c, true
		}
	}
	return client.Conversation{}, false
}

func (h *harness) only() client.Conversation {
	h.t.Helper()
	res := h.do("list", struct{}{})
	if res.Outcome != bus.OutcomeOK || res.Conversations == nil || len(*res.Conversations) != 1 {
		h.t.Fatalf("conversations %+v %s", res.Conversations, res.Reason)
	}
	return (*res.Conversations)[0]
}

func (h *harness) member(conv, name string) client.Member {
	h.t.Helper()
	res := h.do("member", client.MemberRequest{Conversation: conv, Name: name})
	if res.Outcome != bus.OutcomeOK || res.Member == nil {
		h.t.Fatalf("member %s %s", name, res.Reason)
	}
	return *res.Member
}

func (h *harness) hold(name string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Join(h.dir, "hold"), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "hold", name), nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) release(name string) {
	h.t.Helper()
	if err := os.Remove(filepath.Join(h.dir, "hold", name)); err != nil {
		h.t.Fatal(err)
	}
}

// lockedBuffer is safe for a test to read while Execute writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func launches(calls []call, name string) []call {
	var out []call
	for _, c := range calls {
		if c.Kind == "launch" && strings.Contains(c.Stdin, "You are "+name+" ") {
			out = append(out, c)
		}
	}
	return out
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 20*time.Second)
}

func TestRunCreatesNamedConversationAndPublishesInstructions(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{"alpha": {Register: true}, "bravo": {Register: true}}
	cfg := h.base()
	cfg.Prompts = "tasks:hotseat/review/extra"
	cfg.PromptsSet = true
	cfg.Instructions = "focus on the migration"
	cfg.InstructionsSet = true
	cfg.SeatSet = false
	cfg.Agents = "bravo=hotseat/claude:c1 alpha=hotseat/grok:g1"
	cctx, cancel := ctx()
	defer cancel()
	done := make(chan error, 1)
	var stdout string
	go func() {
		var err error
		stdout, _, err = h.run(cctx, cfg)
		done <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	var conv client.Conversation
	for time.Now().Before(deadline) {
		if c, ok := h.conv("foo-x234-review-ticket"); ok {
			conv = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conv.Name == "" {
		t.Fatal(stdout, h.calls())
	}
	h.publish(conv.Name, "alpha", "done", "finished", nil)
	if err := <-done; err != nil {
		t.Fatal(err, stdout)
	}
	if conv.Task != "tasks:hotseat/review/ticket" || conv.Ticket != "foo-x234" || conv.Seat != "alpha" {
		t.Fatalf("binding %+v", conv)
	}
	if conv.Directory != filepath.Join(h.dir, "code") {
		t.Fatalf("directory %s", conv.Directory)
	}
	msgs := h.messages(conv.Name)
	var instructions []client.Message
	for _, m := range msgs {
		if m.Kind == kindInstructions {
			instructions = append(instructions, m)
		}
	}
	if len(instructions) != 1 || instructions[0].From != "owner" || instructions[0].TxID != "instructions" {
		t.Fatalf("instructions %+v", instructions)
	}
	if !sameStrings(instructions[0].To, []string{"bravo", "alpha"}) {
		t.Fatalf("to %v", instructions[0].To)
	}
	if !strings.Contains(instructions[0].Body, "Review the ticket.\n\nseat: alpha\nticket: foo-x234\nprompts:\n- tasks:hotseat/review/extra\ncustom:\nfocus on the migration\n") {
		t.Fatalf("body %q", instructions[0].Body)
	}
	for _, c := range launches(h.calls(), "alpha") {
		if c.Dir != conv.Directory {
			t.Fatalf("cwd %s", c.Dir)
		}
	}
	if !strings.Contains(stdout, "conversation: foo-x234-review-ticket") || !strings.Contains(stdout, "outcome: done") {
		t.Fatalf("stdout %s", stdout)
	}
}

func TestSecondRunMintsOneCodeAndDoesNotPublishIntoTheFirst(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.conv("foo-x234-review-ticket"); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish("foo-x234-review-ticket", "alpha", "done", "finished", nil)
	cancel()
	time.Sleep(50 * time.Millisecond)
	h2 := newHarness(t)
	h2.plan = h.plan
	cfg2 := h2.base()
	cfg2.Address = h.addr
	c2, cancel2 := ctx()
	defer cancel2()
	go func() {
		_, _, _ = h2.run(c2, cfg2)
	}()
	var second client.Conversation
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res := h.do("list", struct{}{})
		if res.Conversations != nil {
			for _, c := range *res.Conversations {
				if strings.HasPrefix(c.Name, "foo-x234-review-ticket-") && c.Name != "foo-x234-review-ticket" {
					second = c
				}
			}
		}
		if second.Name != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second.Name == "" {
		t.Fatal("no coded conversation")
	}
	code := strings.TrimPrefix(second.Name, "foo-x234-review-ticket-")
	if len(code) != 4 || strings.Contains(code, "-") || !strings.ContainsRune(LetterAlphabet, rune(code[0])) {
		t.Fatalf("code %q", code)
	}
	h.publish(second.Name, "alpha", "done", "finished", nil)
	cancel2()
	n := 0
	for _, m := range h.messages("foo-x234-review-ticket") {
		if m.Kind == kindInstructions {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("first instructions %d", n)
	}
}

func TestOversizeBodyUppercaseAndMissingModuleCreateNothing(t *testing.T) {
	h := newHarness(t)
	h.plan.Body = strings.Repeat("a", bus.DefaultMaxBody)
	cfg := h.base()
	_, stderr, err := h.run(context.Background(), cfg)
	if err == nil {
		t.Fatal("oversize created")
	}
	if _, ok := h.conv("foo-x234-review-ticket"); ok {
		t.Fatal("conversation exists")
	}
	_ = stderr

	h.plan.Body = "Review\n"
	cfg.Agents = "Alpha=hotseat/grok:g1"
	_, _, err = h.run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatal(err)
	}
	if h.onlyExists() {
		t.Fatal("uppercase created a conversation")
	}

	h.plan.IDExit = 1
	h.plan.IDStderr = "matches:\n  tasks:review/ticket\n  tasks:review/other\n"
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1"
	_, stderr, err = h.run(context.Background(), cfg)
	if err == nil || strings.Count(err.Error(), "tasks:review/other") != 1 || strings.Contains(stderr, "tasks:review/other") {
		t.Fatalf("err %v stderr %s", err, stderr)
	}
	if h.onlyExists() {
		t.Fatal("missing module created a conversation")
	}

	h.plan.IDExit = 0
	h.plan.ID = "tasks:" + strings.Repeat("b", 65)
	_, _, err = h.run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Fatal(err)
	}
	if h.onlyExists() {
		t.Fatal("long name created a conversation")
	}
}

func (h *harness) onlyExists() bool {
	res := h.do("list", struct{}{})
	return res.Conversations != nil && len(*res.Conversations) > 0
}

func TestContinueRebuildsMissingInstructionsAndRejectsMismatch(t *testing.T) {
	h := newHarness(t)
	work := t.TempDir()
	created := h.do("create", client.CreateRequest{
		Name: "review-ticket-foo-x234", Task: "tasks:hotseat/review/ticket", Ticket: "foo-x234",
		Seat: "alpha", Directory: filepath.Join(h.dir, "code"), Roster: []string{"alpha", "bravo"},
	})
	if created.Outcome != bus.OutcomeOK {
		t.Fatal(created.Reason)
	}
	cfg := h.base()
	cfg.TaskSet = false
	cfg.Conversation = "review-ticket-foo-x234"
	cfg.ConversationSet = true
	cfg.Dir = work
	cctx, cancel := ctx()
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		done <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		msgs := h.messages("review-ticket-foo-x234")
		if len(msgs) > 0 && msgs[0].Kind == kindInstructions && strings.Contains(msgs[0].Body, "seat: alpha\nticket: foo-x234\n") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish("review-ticket-foo-x234", "alpha", "done", "finished", nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, c := range h.calls() {
		if c.Kind == "tk" {
			t.Fatal("continue looked up the code-root")
		}
		if c.Kind == "launch" && c.Dir != filepath.Join(h.dir, "code") {
			t.Fatalf("launch dir %s", c.Dir)
		}
	}
	if n := countKind(h.messages("review-ticket-foo-x234"), kindInstructions); n != 1 {
		t.Fatalf("instructions %d", n)
	}

	cfg.Agents = "bravo=hotseat/claude:c1 alpha=hotseat/grok:g1"
	_, stdout, err := h.run(context.Background(), cfg)
	if err == nil || stdout != "" {
		t.Fatalf("mismatch err %v stdout %q", err, stdout)
	}
	if n := countKind(h.messages("review-ticket-foo-x234"), kindInstructions); n != 1 {
		t.Fatalf("mismatch wrote instructions %d", n)
	}
}

func TestContinueWithoutTaskOnMissingNameIsUsage(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TaskSet = false
	cfg.Conversation = "missing"
	cfg.ConversationSet = true
	stdout, _, err := h.run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "task is required") || stdout != "" {
		t.Fatalf("err %v stdout %q", err, stdout)
	}
	if h.onlyExists() {
		t.Fatal("created")
	}
}

func TestFirstLaunchHasNoResumeAndLaterLaunchReusesIt(t *testing.T) {
	h := newHarness(t)
	h.plan.Locator = map[string]textFile{
		"hotseat/grok":   {Out: "sessionId\n"},
		"hotseat/claude": {Out: "session_id\n"},
	}
	h.plan.Launch = map[string]runFile{
		"bravo": {JSON: `{"session_id":"sess-bravo"}`},
	}
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	deadline := time.Now().Add(8 * time.Second)
	var name string
	for time.Now().Before(deadline) {
		res := h.do("list", struct{}{})
		if res.Conversations != nil && len(*res.Conversations) == 1 {
			mem := h.do("member", client.MemberRequest{Conversation: (*res.Conversations)[0].Name, Name: "bravo"})
			if mem.Member != nil && mem.Member.Status == statusExited {
				name = (*res.Conversations)[0].Name
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if name == "" {
		t.Fatal(h.calls())
	}
	conv, _ := h.conv(name)
	if conv.Directory != h.dir {
		t.Fatalf("directory %s", conv.Directory)
	}
	h.publish(name, "alpha", "turn", "mode: parallel\nlook", []string{"bravo"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "done", "finished", nil)
	cancel()
	bravo := launches(h.calls(), "bravo")
	if len(bravo) < 2 {
		t.Fatalf("launches %+v", bravo)
	}
	if strings.Contains(strings.Join(bravo[0].Argv, " "), "--resume=") {
		t.Fatalf("first argv %v", bravo[0].Argv)
	}
	joined := strings.Join(bravo[1].Argv, " ")
	if !strings.Contains(joined, "--resume=sess-bravo") || !strings.Contains(joined, "--agent") || !strings.Contains(joined, "hotseat/claude") || !strings.Contains(joined, "--model") || !strings.Contains(joined, "c1") || !strings.Contains(joined, "--role") || !strings.Contains(joined, "hotseat/member") || !strings.Contains(joined, "--permission") || !strings.Contains(joined, "auto") {
		t.Fatalf("argv %v", bravo[1].Argv)
	}
	if strings.Contains(bravo[1].Stdin, "sess-bravo") {
		t.Fatalf("prompt has session %s", bravo[1].Stdin)
	}
}

func TestMissingSessionIDTimesOutOneMember(t *testing.T) {
	h := newHarness(t)
	h.plan.Locator = map[string]textFile{
		"hotseat/grok": {Out: "sessionId\n\n"},
	}
	h.plan.Launch = map[string]runFile{
		"alpha": {JSON: `{"sessionId":"UNIQUEFRAGMENT"}`},
		"bravo": {Register: true},
	}
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		alpha := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		bravo := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if alpha.Member != nil && alpha.Member.Status == statusTimedOut && bravo.Member != nil && bravo.Member.Status == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "alpha").Status != statusTimedOut || h.member(name, "bravo").Status != statusExited {
		t.Fatalf("alpha %s bravo %s", h.member(name, "alpha").Status, h.member(name, "bravo").Status)
	}
	var errBody string
	for _, m := range h.messages(name) {
		if m.Kind == kindError {
			errBody += m.Body + "\n"
		}
	}
	if !strings.Contains(errBody, "alpha") || !strings.Contains(errBody, classMissingSession) || strings.Contains(errBody, "UNIQUEFRAGMENT") {
		t.Fatalf("error %q", errBody)
	}
	h.publish(name, "bravo", "done", "no", nil)
	h.publish(name, "alpha", "done", "yes", nil)
	cancel()
}

func TestMissingStartIsNotReportedAsAMissingPermission(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.StartBin = filepath.Join(h.dir, "missing-start")
	stdout, _, err := h.run(context.Background(), cfg)
	if err == nil || strings.Contains(err.Error(), "does not define permission") || !strings.Contains(err.Error(), "missing-start") {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Fatalf("stdout %s", stdout)
	}
	if h.onlyExists() {
		t.Fatal("created")
	}
}

func TestPermissionRefusalCreatesNothing(t *testing.T) {
	h := newHarness(t)
	h.plan.DenyPerm = map[string]bool{"hotseat/grok": true}
	stdout, _, err := h.run(context.Background(), h.base())
	if err == nil || !strings.Contains(err.Error(), "hotseat/grok does not define permission auto") {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Fatalf("stdout %s", stdout)
	}
	res := h.do("list", struct{}{})
	if res.Outcome != bus.OutcomeOK || (res.Conversations != nil && len(*res.Conversations) != 0) {
		t.Fatalf("list %+v %s", res.Conversations, res.Reason)
	}
	if len(launches(h.calls(), "alpha")) != 0 || len(launches(h.calls(), "bravo")) != 0 {
		t.Fatalf("calls %+v", h.calls())
	}
}

func TestEmptyLocatorStdoutTimesOutOneMember(t *testing.T) {
	missingSessionTimesOutOneMember(t, func(h *harness) {
		h.plan.Locator = map[string]textFile{"hotseat/grok": {}}
	})
}

func TestLocatorCommandFailureTimesOutOneMember(t *testing.T) {
	missingSessionTimesOutOneMember(t, func(h *harness) {
		h.plan.Locator = map[string]textFile{"hotseat/grok": {Exit: 1}}
	})
}

func TestLocatorFailureTextIsShownOnce(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {JSON: `{"sessionId":"UNIQUEFRAGMENT"}`},
		"bravo": {Register: true},
	}
	h.plan.Locator = map[string]textFile{
		"hotseat/grok": {Stderr: "no such field\n", Exit: 1},
	}
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		stdout, stderr, err := h.run(cctx, cfg)
		stdoutCh <- stdout
		stderrCh <- stderr
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		alpha := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		if alpha.Member != nil && alpha.Member.Status == statusTimedOut {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "alpha").Status != statusTimedOut {
		t.Fatalf("alpha %s", h.member(name, "alpha").Status)
	}
	h.publish(name, "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	stdout := <-stdoutCh
	stderr := <-stderrCh
	if strings.Count(stderr, "no such field") != 1 || strings.Contains(stdout, "no such field") {
		t.Fatalf("stdout %s\nstderr %s", stdout, stderr)
	}
	var errBody string
	for _, m := range h.messages(name) {
		if m.Kind == kindError {
			errBody += m.Body + "\n"
		}
	}
	if !strings.Contains(errBody, classMissingSession) || strings.Contains(errBody, "no such field") {
		t.Fatalf("error %q", errBody)
	}
}

func TestLaunchJSONWithoutAStringIDTimesOutOneMember(t *testing.T) {
	missingSessionTimesOutOneMember(t, func(h *harness) {
		h.plan.Launch["alpha"] = runFile{JSON: `{"sessionId":1,"note":"UNIQUEFRAGMENT"}`}
	})
}

func missingSessionTimesOutOneMember(t *testing.T, setup func(*harness)) {
	t.Helper()
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {JSON: `{"sessionId":"UNIQUEFRAGMENT"}`},
		"bravo": {Register: true},
	}
	setup(h)
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		alpha := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		bravo := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if alpha.Member != nil && alpha.Member.Status == statusTimedOut && bravo.Member != nil && bravo.Member.Status == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "alpha").Status != statusTimedOut || h.member(name, "bravo").Status != statusExited {
		t.Fatalf("alpha %s bravo %s", h.member(name, "alpha").Status, h.member(name, "bravo").Status)
	}
	var errBody string
	for _, m := range h.messages(name) {
		if m.Kind == kindError {
			errBody += m.Body + "\n"
		}
	}
	if !strings.Contains(errBody, "alpha") || !strings.Contains(errBody, classMissingSession) || strings.Contains(errBody, "UNIQUEFRAGMENT") {
		t.Fatalf("error %q", errBody)
	}
	h.publish(name, "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestMemberStderrIsShownAndSessionTextIsNot(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha": {Exit: 1, Sleep: "1s", JSON: `{"sessionId":"UNIQUEFRAGMENT"}`, Stderr: "model failed\n"},
		"bravo": {Register: true},
	}
	cfg := h.base()
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)
	go func() {
		stdout, stderr, err := h.run(cctx, cfg)
		stdoutCh <- stdout
		stderrCh <- stderr
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mem := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if mem.Member != nil && mem.Member.Registered != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "bravo").Registered == "" {
		t.Fatal("bravo did not register")
	}
	h.release("alpha")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		alpha := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		bravo := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if alpha.Member != nil && alpha.Member.Status == statusTimedOut && bravo.Member != nil && bravo.Member.Status == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "alpha").Status != statusTimedOut || h.member(name, "bravo").Status != statusExited {
		t.Fatalf("alpha %s bravo %s", h.member(name, "alpha").Status, h.member(name, "bravo").Status)
	}
	var errBody string
	for _, m := range h.messages(name) {
		if m.Kind == kindError {
			errBody += m.Body + "\n"
		}
	}
	if !strings.Contains(errBody, "alpha") || !strings.Contains(errBody, classProcessExit) || strings.Contains(errBody, "model failed") || strings.Contains(errBody, "UNIQUEFRAGMENT") {
		t.Fatalf("error %q", errBody)
	}
	h.publish(name, "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	stdout := <-stdoutCh
	stderr := <-stderrCh
	if !strings.Contains(stderr, "model failed") {
		t.Fatalf("stderr %q", stderr)
	}
	if strings.Contains(stdout, "model failed") || strings.Contains(stdout, "UNIQUEFRAGMENT") || strings.Contains(stderr, "UNIQUEFRAGMENT") {
		t.Fatalf("stdout %s\nstderr %s", stdout, stderr)
	}
}

func TestParallelSerialBadTurnAndAct(t *testing.T) {
	h := newHarness(t)
	h.hold("bravo")
	h.hold("charlie")
	h.plan.Launch = map[string]runFile{}
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	stdoutCh := make(chan string, 1)
	go func() {
		stdout, _, _ := h.run(cctx, cfg)
		stdoutCh <- stdout
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 1 && len(launches(h.calls(), "charlie")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.release("bravo")
	h.release("charlie")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.member(name, "bravo").Status == statusExited && h.member(name, "charlie").Status == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.hold("bravo")
	h.hold("charlie")
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"charlie", "bravo"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 2 && len(launches(h.calls(), "charlie")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(launches(h.calls(), "bravo")) < 2 || len(launches(h.calls(), "charlie")) < 2 {
		t.Fatalf("parallel launches not overlapping: %+v", h.calls())
	}
	h.release("bravo")
	h.release("charlie")
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: serial\nreview", []string{"bravo", "charlie"})
	time.Sleep(200 * time.Millisecond)
	if len(launches(h.calls(), "charlie")) != 2 {
		t.Fatalf("serial started charlie early: %d", len(launches(h.calls(), "charlie")))
	}
	h.release("bravo")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "charlie")) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "turn", "nope", nil)
	h.publish(name, "alpha", "act", "go", []string{"bravo"})
	h.publish(name, "alpha", "turn", "mode: parallel\nrest", []string{"nobody", "bravo"})
	deadline = time.Now().Add(8 * time.Second)
	var bad, act int
	for time.Now().Before(deadline) {
		bad, act = 0, 0
		for _, m := range h.messages(name) {
			if m.Kind == kindError && strings.Contains(m.Body, classBadTurn) {
				bad++
			}
			if m.Kind == "act" {
				act++
			}
		}
		if bad >= 2 && act == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "done", "finished", nil)
	stdout := <-stdoutCh
	cancel()
	if bad < 2 || act != 1 {
		t.Fatalf("bad %d act %d", bad, act)
	}
	if !strings.Contains(stdout, "turn: parallel") || !strings.Contains(stdout, "to: charlie") || !strings.Contains(stdout, "outcome: done") {
		t.Fatalf("stdout %s", stdout)
	}
	alpha := launches(h.calls(), "alpha")
	if len(alpha) < 2 {
		t.Fatalf("alpha launches %d", len(alpha))
	}
}

func TestFollowUpWaitsForBootstrapAndLimitSkipsIt(t *testing.T) {
	h := newHarness(t)
	h.hold("alpha")
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Timeout = 300 * time.Millisecond
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if res.Member != nil && res.Member.Status == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "turn", "mode: serial\nreview", []string{"bravo"})
	time.Sleep(150 * time.Millisecond)
	if len(launches(h.calls(), "alpha")) != 1 {
		t.Fatalf("follow-up overlapped bootstrap: %+v", launches(h.calls(), "alpha"))
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if countBody(h.messages(name), classProcessLimit) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(launches(h.calls(), "alpha")) != 1 {
		t.Fatalf("follow-up launched %d", len(launches(h.calls(), "alpha")))
	}
	if countBody(h.messages(name), classProcessLimit) == 0 || countBody(h.messages(name), "alpha") == 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
	cancel()
}

func TestBackupReadFailureStopsInsteadOfSkipping(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	e := &engine{
		root:   context.Background(),
		addr:   addr,
		name:   "kept",
		seat:   "alpha",
		roster: []agent{{name: "bravo"}, {name: "charlie"}},
	}
	name, err := e.backup()
	if err == nil || name != "" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestTimeoutIsStoredWhenActingSeatReadFails(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	launched := statusLaunched
	wrote := h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	if wrote.Outcome != bus.OutcomeOK {
		t.Fatal(wrote.Reason)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/member") && bytes.Contains(body, []byte(`"name":"bravo"`)) && !bytes.Contains(body, []byte(`"status"`)) {
			http.Error(w, "read failed", http.StatusBadGateway)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+h.addr+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})}
	t.Cleanup(func() {
		_ = srv.Close()
	})
	go func() {
		_ = srv.Serve(ln)
	}()
	e := &engine{
		root:   context.Background(),
		addr:   ln.Addr().String(),
		name:   "kept",
		seat:   "alpha",
		roster: []agent{{name: "alpha"}, {name: "bravo"}},
		status: map[string]string{"alpha": statusTimedOut},
		out:    &statusWriter{w: io.Discard},
	}
	err = e.markTimeout("bravo", classProcessLimit)
	if err == nil {
		t.Fatal("read error was dropped")
	}
	if _, ok := e.takeHandoff(); ok {
		t.Fatal("handed off")
	}
	if h.member("kept", "bravo").Status != statusTimedOut {
		t.Fatalf("bravo %s", h.member("kept", "bravo").Status)
	}
	if countBody(h.messages("kept"), classProcessLimit) != 1 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
}

func TestBackupSkipsAMissingRowAndUsesTheNextRegisteredName(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo", "charlie"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	e := &engine{
		root:   context.Background(),
		addr:   h.addr,
		name:   "kept",
		seat:   "alpha",
		roster: []agent{{name: "alpha"}, {name: "bravo"}, {name: "charlie"}},
	}
	name, err := e.backup()
	if err != nil || name != "" {
		t.Fatalf("before register name %q err %v", name, err)
	}
	launched := statusLaunched
	wrote := h.do("member", client.MemberRequest{Conversation: "kept", Name: "charlie", Status: &launched})
	if wrote.Outcome != bus.OutcomeOK {
		t.Fatal(wrote.Reason)
	}
	reg := h.do("register", client.RegisterRequest{Conversation: "kept", Name: "charlie"})
	if reg.Outcome != bus.OutcomeOK {
		t.Fatal(reg.Reason)
	}
	name, err = e.backup()
	if err != nil || name != "charlie" {
		t.Fatalf("name %q err %v", name, err)
	}
}

func TestNoBackupLeavesTheConversationOpen(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{"alpha": {Exit: 1}}
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1"
	cfg.TicketSet = false
	stdout, _, err := h.run(context.Background(), cfg)
	if err == nil || !strings.Contains(stdout, "outcome: error") {
		t.Fatalf("err %v stdout %s", err, stdout)
	}
	conv := h.only()
	if conv.Status == "closed" {
		t.Fatal("closed")
	}
	if countBody(h.messages(conv.Name), classNoBackup) == 0 {
		t.Fatalf("msgs %+v", h.messages(conv.Name))
	}
}

func TestDoneFromTrailerAfterTimeoutAndNotFromAnotherName(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha":   {Exit: 1, Sleep: "1s"},
		"bravo":   {Register: true},
		"charlie": {},
	}
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	stdoutCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		stdout, _, err := h.run(cctx, cfg)
		stdoutCh <- stdout
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mem := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		if mem.Member != nil && mem.Member.Registered != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "bravo").Registered == "" {
		t.Fatal("bravo did not register")
	}
	h.release("alpha")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		if res.Outcome == bus.OutcomeOK && res.Member != nil && res.Member.Status == statusTimedOut {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "charlie", "done", "not me", nil)
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-errCh:
		t.Fatal("stopped on charlie", err)
	default:
	}
	h.publish(name, "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	stdout := <-stdoutCh
	conv, _ := h.conv(name)
	if conv.Status != "closed" || !strings.Contains(stdout, "outcome: done") {
		t.Fatalf("status %s stdout %s", conv.Status, stdout)
	}
	cancel()
}

func TestDoneFromTheBackupCloses(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	timed := statusTimedOut
	exited := statusExited
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &timed})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &exited})
	h.publish("kept", "bravo", "done", "yes", nil)
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = false
	stdout, _, err := h.run(context.Background(), cfg)
	if err != nil || !strings.Contains(stdout, "outcome: done") {
		t.Fatalf("err %v stdout %s", err, stdout)
	}
	conv, _ := h.conv("kept")
	if conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
	if len(launches(h.calls(), "bravo")) != 0 {
		t.Fatal("launched after done")
	}
}

func TestHandoffIsOnlyTheTimeoutFactAndFollowUpStillRuns(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{
		"alpha":   {Exit: 1, Sleep: "1s"},
		"bravo":   {Register: true, Exit: 1, ExitOn: "acting seat"},
		"charlie": {Register: true},
	}
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	h.hold("alpha")
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		bravo := h.do("member", client.MemberRequest{Conversation: name, Name: "bravo"})
		charlie := h.do("member", client.MemberRequest{Conversation: name, Name: "charlie"})
		if bravo.Member != nil && bravo.Member.Registered != "" && charlie.Member != nil && charlie.Member.Registered != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.member(name, "bravo").Registered == "" || h.member(name, "charlie").Registered == "" {
		t.Fatal("backup did not register")
	}
	h.release("alpha")
	deadline = time.Now().Add(8 * time.Second)
	var handoff call
	for time.Now().Before(deadline) {
		for _, c := range launches(h.calls(), "charlie") {
			if strings.Contains(c.Stdin, "acting seat") {
				handoff = c
			}
		}
		if handoff.Time != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if handoff.Time == "" || strings.Contains(handoff.Stdin, "The set has finished.") || !strings.Contains(handoff.Stdin, "bravo is timed-out.") {
		t.Fatalf("handoff %q", handoff.Stdin)
	}
	var first call
	for _, c := range launches(h.calls(), "bravo") {
		if strings.Contains(c.Stdin, "alpha is timed-out.") {
			first = c
		}
	}
	if first.Time == "" || strings.Contains(first.Stdin, "The set has finished.") {
		t.Fatalf("first handoff %q", first.Stdin)
	}
	h.publish(name, "alpha", "turn", "mode: serial\nreview", []string{"charlie"})
	deadline = time.Now().Add(8 * time.Second)
	var follow call
	for time.Now().Before(deadline) {
		for _, c := range launches(h.calls(), "charlie") {
			if strings.Contains(c.Stdin, "The set has finished.") {
				follow = c
			}
		}
		if follow.Time != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if follow.Time == "" {
		t.Fatal("follow-up was dropped", h.calls())
	}
	h.publish(name, "alpha", "done", "yes", nil)
	cancel()
}

func TestFirstRunSkipsATurnWhoseProofIsAlreadyInTheBatch(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	go func() {
		_, _, _ = h.run(cctx, cfg)
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(name, "bravo") == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: serial\none", []string{"bravo"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "turn", "mode: serial\ntwo", []string{"bravo"})
	h.publish(name, "alpha", "turn", "mode: serial\nthree", []string{"bravo"})
	h.release("bravo")
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(launches(h.calls(), "bravo")); n != 3 {
		t.Fatalf("bravo launches %d, want 3 (bootstrap, turn one, turn three)", n)
	}
	h.publish(name, "alpha", "done", "yes", nil)
	cancel()
}

func TestResumeClosesBeforeLaunchWhenDoneIsAlreadyStored(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo", "charlie"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo", "charlie"})
	h.publish("kept", "alpha", "turn", "mode: serial\nreview", []string{"bravo", "charlie"})
	h.publish("kept", "alpha", "done", "finished", nil)
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	stdout, stderr, err := h.run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err, stdout, stderr)
	}
	if len(launches(h.calls(), "bravo")) != 0 || len(launches(h.calls(), "charlie")) != 0 || len(launches(h.calls(), "alpha")) != 0 {
		t.Fatalf("launched after done %+v", h.calls())
	}
	conv, ok := h.conv("kept")
	if !ok || conv.Status != "closed" {
		t.Fatalf("status %+v", conv)
	}
	if !strings.Contains(stdout, "outcome: done") {
		t.Fatalf("stdout %s", stdout)
	}
}

func TestDoneDuringTurnStopsTheNextLaunch(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cfg.TicketSet = false
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(name, "bravo") == statusExited && h.memberStatus(name, "charlie") == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: serial\nreview", []string{"bravo", "charlie"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "done", "finished", nil)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("done left the run waiting on bravo")
	}
	h.release("bravo")
	if len(launches(h.calls(), "charlie")) != 1 {
		t.Fatalf("charlie launches %d", len(launches(h.calls(), "charlie")))
	}
	for _, c := range h.calls() {
		if strings.Contains(c.Stdin, "The set has finished.") {
			t.Fatal("follow-up launched after done")
		}
	}
	conv, _ := h.conv(name)
	if conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
	cancel()
}

func TestHaltMarksAKilledMemberTimedOutWithoutHandoff(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{"charlie": {Sleep: "2s"}}
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Timeout = time.Hour
	cfg.TimeoutSet = true
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1 charlie=hotseat/grok:g2"
	cctx, cancel := ctx()
	defer cancel()
	stdoutCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		stdout, _, err := h.run(cctx, cfg)
		stdoutCh <- stdout
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(name, "bravo") == statusExited && h.memberStatus(name, "charlie") == statusExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.memberStatus(name, "bravo") != statusExited || h.memberStatus(name, "charlie") != statusExited {
		t.Fatalf("bootstrap bravo %s charlie %s", h.memberStatus(name, "bravo"), h.memberStatus(name, "charlie"))
	}
	h.hold("bravo")
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo", "charlie"})
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(name, "bravo") == statusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.memberStatus(name, "bravo") != statusRunning {
		t.Fatalf("bravo %s", h.memberStatus(name, "bravo"))
	}
	h.publish(name, "alpha", "done", "finished", nil)
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if h.memberStatus(name, "bravo") == statusTimedOut {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.memberStatus(name, "bravo") != statusTimedOut {
		t.Fatalf("bravo %s", h.memberStatus(name, "bravo"))
	}
	if countBody(h.messages(name), classProcessExit) != 0 || countBody(h.messages(name), classProcessLimit) != 0 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	stdout := <-stdoutCh
	timed := strings.LastIndex(stdout, "status: timed-out")
	done := strings.LastIndex(stdout, "outcome: done")
	if timed < 0 || done < timed || !strings.HasSuffix(strings.TrimRight(stdout, "\n"), "outcome: done") {
		t.Fatalf("stdout %s", stdout)
	}
	for _, c := range h.calls() {
		if strings.Contains(c.Stdin, "acting seat") {
			t.Fatalf("handoff %q", c.Stdin)
		}
	}
	conv, _ := h.conv(name)
	if conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
	cancel()
}

func TestResumeDoesNotReportARejectedTurnAgain(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	h.publish("kept", "alpha", "turn", "nope", nil)
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	runUntil := func(want int) {
		t.Helper()
		cctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		go func() {
			_, _, err := h.run(cctx, cfg)
			errCh <- err
		}()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if countBody(h.messages("kept"), classBadTurn) >= want {
				break
			}
			select {
			case err := <-errCh:
				t.Fatalf("run ended: %v msgs %+v", err, h.messages("kept"))
			default:
			}
			time.Sleep(20 * time.Millisecond)
		}
		if n := countBody(h.messages("kept"), classBadTurn); n != want {
			t.Fatalf("bad turn errors %d want %d msgs %+v", n, want, h.messages("kept"))
		}
		cancel()
		if err := <-errCh; err == nil {
			t.Fatal("cancel produced no error")
		}
	}
	runUntil(1)
	h.publish("kept", "alpha", "turn", "still nope", nil)
	runUntil(2)
	if n := len(launches(h.calls(), "bravo")); n != 0 {
		t.Fatalf("launched bravo %d", n)
	}
}

func TestTurnCapStopsOnTheNextTurn(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	cfg.MaxTurns = 1
	cfg.MaxTurnsSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	h.publish(name, "alpha", "turn", "nope", nil)
	h.publish(name, "alpha", "turn", "again", nil)
	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), classTurnCap) {
		t.Fatal(err)
	}
	msgs := h.messages(name)
	if countBody(msgs, classBadTurn) != 1 || countBody(msgs, classTurnCap) != 1 {
		t.Fatalf("msgs %+v", msgs)
	}
	conv, _ := h.conv(name)
	if conv.Status == "closed" {
		t.Fatal("closed")
	}
	if len(launches(h.calls(), "bravo")) != 1 {
		t.Fatalf("launches %d", len(launches(h.calls(), "bravo")))
	}
	cancel()
}

func TestDefaultCapSkipsThe501stTurn(t *testing.T) {
	h := newHarness(t)
	cfg := h.base()
	cfg.TicketSet = false
	cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	for i := 0; i < DefaultMaxTurns+1; i++ {
		h.publish(name, "alpha", "turn", "nope", nil)
	}
	err := <-errCh
	if err == nil {
		t.Fatal("cap did not stop")
	}
	msgs := h.messages(name)
	if countBody(msgs, classBadTurn) != DefaultMaxTurns || countBody(msgs, classTurnCap) != 1 {
		t.Fatalf("bad %d cap %d", countBody(msgs, classBadTurn), countBody(msgs, classTurnCap))
	}
	conv, _ := h.conv(name)
	if conv.Status == "closed" {
		t.Fatal("closed")
	}
}

func TestInteractiveKeepsTheCapAndDropsTheProcessLimit(t *testing.T) {
	h := newHarness(t)
	h.plan.Launch = map[string]runFile{"alpha": {Sleep: "300ms"}}
	cfg := h.base()
	cfg.TicketSet = false
	cfg.Timeout = 50 * time.Millisecond
	cfg.Interactive = true
	cfg.MaxTurns = 1
	cfg.MaxTurnsSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	name := h.waitName()
	deadline := time.Now().Add(8 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		res := h.do("member", client.MemberRequest{Conversation: name, Name: "alpha"})
		if res.Member != nil {
			status = res.Member.Status
			if status == statusExited {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != statusExited {
		t.Fatalf("status %s", h.member(name, "alpha").Status)
	}
	h.publish(name, "alpha", "turn", "nope", nil)
	h.publish(name, "alpha", "turn", "again", nil)
	if err := <-errCh; err == nil {
		t.Fatal("cap did not stop")
	}
	if countBody(h.messages(name), classProcessLimit) != 0 || countBody(h.messages(name), classTurnCap) != 1 {
		t.Fatalf("msgs %+v", h.messages(name))
	}
}

func TestRestartDeadPidTimesOutBeforeLaunchAndLivePidIsWaited(t *testing.T) {
	h := newHarness(t)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	pid := int64(os.Getpid())
	// A pid from a process that has exited. Getpid is alive, so spawn and wait.
	child := exec.Command("sleep", "0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := int64(child.Process.Pid)
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = pid
	dir := filepath.Join(h.dir, "code")
	res := h.do("create", client.CreateRequest{
		Name: "review-ticket-foo-x234", Task: "tasks:hotseat/review/ticket", Ticket: "foo-x234",
		Seat: "alpha", Directory: dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("review-ticket-foo-x234", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "review-ticket-foo-x234", Name: "alpha", Status: &launched, PID: &deadPID})
	registered := h.do("register", client.RegisterRequest{Conversation: "review-ticket-foo-x234", Name: "alpha"})
	if registered.Outcome != bus.OutcomeOK {
		t.Fatal(registered.Reason)
	}
	h.publish("review-ticket-foo-x234", "alpha", "done", "yes", nil)
	cfg := h.base()
	cfg.Conversation = "review-ticket-foo-x234"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.AgentsSet = false
	stdout, _, err := h.run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err, stdout)
	}
	if h.member("review-ticket-foo-x234", "alpha").Status != statusTimedOut {
		t.Fatalf("status %s", h.member("review-ticket-foo-x234", "alpha").Status)
	}
	if countBody(h.messages("review-ticket-foo-x234"), classProcessExit) == 0 {
		t.Fatal("no error")
	}
	if len(launches(h.calls(), "alpha")) != 0 {
		t.Fatal("launched after a dead pid")
	}
	conv, _ := h.conv("review-ticket-foo-x234")
	if conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
}

func TestProcessStartMatchesALaunchRecordedAfterIt(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if sameLaunch(pid, old) {
		t.Fatal("process started after the launch matched it")
	}
	if !alive(pid) {
		t.Fatal("match check signalled the process")
	}
	if !sameLaunch(pid, time.Now().UTC().Format(time.RFC3339Nano)) {
		t.Fatal("process did not match a launch recorded after it started")
	}
	start, ok := processStart(pid)
	if !ok || time.Since(start) > 30*time.Second || start.After(time.Now().Add(launchSkew)) {
		t.Fatalf("start %v ok %v", start, ok)
	}
}

func TestResumeDoesNotSignalAProcessStartedAfterLaunch(t *testing.T) {
	h := newHarnessClock(t, func() time.Time { return time.Now().Add(-2 * time.Hour) })
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	h.publish("kept", "alpha", "done", "yes", nil)
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = false
	cfg.Timeout = 200 * time.Millisecond
	cfg.TimeoutSet = true
	stdout, stderr, err := h.run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err, stdout, stderr)
	}
	if !alive(pid) {
		t.Fatal("signalled a process that started after the launch")
	}
	if h.member("kept", "alpha").Status != statusTimedOut {
		t.Fatalf("status %s", h.member("kept", "alpha").Status)
	}
	if countBody(h.messages("kept"), classProcessExit) == 0 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
	if strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
		t.Fatalf("pid leaked\n%s\n%s", stdout, stderr)
	}
	conv, _ := h.conv("kept")
	if conv.Status != "closed" {
		t.Fatalf("status %s", conv.Status)
	}
}

func TestForeignProcessLimitStopsTheProcess(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	if registered := h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"}); registered.Outcome != bus.OutcomeOK {
		t.Fatal(registered.Reason)
	}
	exited := statusExited
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &exited})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = false
	cfg.Timeout = 200 * time.Millisecond
	cfg.TimeoutSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	outCh := make(chan string, 1)
	go func() {
		stdout, stderr, err := h.run(cctx, cfg)
		outCh <- stdout + stderr
		errCh <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) && countBody(h.messages("kept"), classProcessLimit) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatal("process still running after the limit")
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	var stdout, stderr string
	select {
	case err := <-errCh:
		stdout = <-outCh
		if err != nil {
			t.Fatal(err, stdout)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("done left the run waiting")
	}
	if h.member("kept", "alpha").Status != statusTimedOut {
		t.Fatalf("status %s", h.member("kept", "alpha").Status)
	}
	if countBody(h.messages("kept"), classProcessLimit) == 0 {
		t.Fatalf("msgs %+v", h.messages("kept"))
	}
	if strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
		t.Fatalf("pid leaked\n%s\n%s", stdout, stderr)
	}
	if len(launches(h.calls(), "alpha")) != 0 {
		t.Fatal("launched a second process")
	}
}

func TestDoneDuringLivePidWaitStopsBeforeTheLimit(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = false
	cfg.Interactive = true
	cfg.InteractiveSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	outCh := make(chan string, 1)
	go func() {
		stdout, stderr, err := h.run(cctx, cfg)
		outCh <- stdout + stderr
		errCh <- err
	}()
	time.Sleep(200 * time.Millisecond)
	if !alive(pid) {
		t.Fatal("process ended before done")
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	select {
	case err := <-errCh:
		stdout := <-outCh
		if err != nil {
			t.Fatal(err, stdout)
		}
		if alive(pid) {
			t.Fatal("left the process running")
		}
		if countBody(h.messages("kept"), classProcessLimit) != 0 {
			t.Fatalf("msgs %+v", h.messages("kept"))
		}
		if !strings.Contains(stdout, "outcome: done") {
			t.Fatalf("stdout %s", stdout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("done left the run waiting on the live pid")
	}
}

func TestLivePidBlocksTheNextLaunch(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "bravo",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: bravo\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	exited := statusExited
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	n := "alpha"
	s := "sess-alpha"
	h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched})
	if registered := h.do("register", client.RegisterRequest{Conversation: "kept", Name: "bravo"}); registered.Outcome != bus.OutcomeOK {
		t.Fatal(registered.Reason)
	}
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &exited})
	nb := "bravo"
	sb := "sess-bravo"
	h.do("session", client.SessionRequest{Conversation: "kept", Name: &nb, Session: &sb})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.Seat = "bravo"
	cfg.SeatSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	h.publish("kept", "bravo", "turn", "mode: serial\nreview", []string{"alpha"})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			break
		}
		if len(launches(h.calls(), "alpha")) != 0 {
			t.Fatal("launched alpha while its process was still running")
		}
		select {
		case err := <-errCh:
			t.Fatalf("run ended: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "alpha")) >= 1 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("run ended: %v calls %+v", err, h.calls())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(launches(h.calls(), "alpha")) != 1 {
		t.Fatalf("alpha launches %d", len(launches(h.calls(), "alpha")))
	}
	h.publish("kept", "bravo", "done", "yes", nil)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("done left the run waiting")
	}
}

func TestZombieIsNotAlive(t *testing.T) {
	child := exec.Command("sleep", "0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_, _ = child.Process.Wait()
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if zombie(pid) {
			if alive(pid) {
				t.Fatal("zombie looked alive")
			}
			return
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("reaped before the zombie check: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("process did not become a zombie")
}

func TestStoredDoneStopsALivePidWithoutASession(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	exited := statusExited
	deadChild := exec.Command("sleep", "0")
	if err := deadChild.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := int64(deadChild.Process.Pid)
	_ = deadChild.Wait()
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched, PID: &deadPID})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &exited})
	h.publish("kept", "alpha", "done", "yes", nil)
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.Interactive = true
	cfg.InteractiveSet = true
	start := time.Now()
	stdout, stderr, err := h.run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err, stdout, stderr)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited for the live pid: %s", time.Since(start))
	}
	if alive(pid) {
		t.Fatal("left the process running")
	}
	if h.member("kept", "alpha").Status != statusTimedOut {
		t.Fatalf("alpha %s", h.member("kept", "alpha").Status)
	}
	if h.member("kept", "bravo").Status != statusExited {
		t.Fatalf("bravo %s", h.member("kept", "bravo").Status)
	}
	if countBody(h.messages("kept"), classProcessLimit) != 0 || countBody(h.messages("kept"), classProcessExit) != 0 || len(launches(h.calls(), "alpha")) != 0 {
		t.Fatalf("msgs %+v calls %+v", h.messages("kept"), h.calls())
	}
	if !strings.Contains(stdout, "outcome: done") || strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
		t.Fatalf("stdout %s\n%s", stdout, stderr)
	}
}

func TestStoredDoneStopsALivePidWithASession(t *testing.T) {
	h := newHarness(t)
	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	live := int64(pid)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched, PID: &live})
	n := "alpha"
	s := "sess-alpha"
	h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	exited := statusExited
	deadChild := exec.Command("sleep", "0")
	if err := deadChild.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := int64(deadChild.Process.Pid)
	_ = deadChild.Wait()
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &launched, PID: &deadPID})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "bravo", Status: &exited})
	h.publish("kept", "alpha", "done", "yes", nil)
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.Timeout = time.Hour
	cfg.TimeoutSet = true
	start := time.Now()
	stdout, stderr, err := h.run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err, stdout, stderr)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited for the live pid: %s", time.Since(start))
	}
	if alive(pid) {
		t.Fatal("left the process running")
	}
	if h.member("kept", "alpha").Status != statusExited {
		t.Fatalf("alpha %s", h.member("kept", "alpha").Status)
	}
	if countBody(h.messages("kept"), classProcessExit) != 0 || countBody(h.messages("kept"), classProcessLimit) != 0 || len(launches(h.calls(), "alpha")) != 0 {
		t.Fatalf("msgs %+v calls %+v", h.messages("kept"), h.calls())
	}
	if strings.Contains(stdout+stderr, "sess-alpha") || strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
		t.Fatalf("secret leaked\n%s\n%s", stdout, stderr)
	}
}

func TestRestartSkipsAFinishedTurnAndResumesAfterSay(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	exited := statusExited
	for _, name := range []string{"alpha", "bravo"} {
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &launched})
		h.do("register", client.RegisterRequest{Conversation: "kept", Name: name})
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &exited})
		n := name
		s := "sess-" + name
		h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	}
	h.publish("kept", "alpha", "turn", "mode: parallel\nfirst", []string{"bravo"})
	h.publish("kept", "bravo", "say", "reviewed", []string{"alpha"})
	h.publish("kept", "alpha", "say", "note", []string{"bravo"})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = true
	cfg.MaxTurns = 1
	cfg.MaxTurnsSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "alpha")) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(launches(h.calls(), "bravo")) != 0 {
		t.Fatal("relaunched a recipient that had exited")
	}
	if len(launches(h.calls(), "alpha")) != 1 {
		t.Fatalf("follow-ups %d", len(launches(h.calls(), "alpha")))
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestResumeLaunchesAnExitedRecipientWhoHasNotSpoken(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	exited := statusExited
	for _, name := range []string{"alpha", "bravo"} {
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &launched})
		h.do("register", client.RegisterRequest{Conversation: "kept", Name: name})
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &exited})
		n := name
		s := "sess-" + name
		h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	}
	h.publish("kept", "alpha", "turn", "mode: parallel\nfirst", []string{"bravo"})
	h.publish("kept", "alpha", "say", "note", []string{"bravo"})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	var turn call
	for time.Now().Before(deadline) {
		for _, c := range launches(h.calls(), "bravo") {
			if strings.Contains(c.Stdin, "first") {
				turn = c
			}
		}
		if turn.Time != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if turn.Time == "" || !strings.Contains(strings.Join(turn.Argv, " "), "--resume=sess-bravo") {
		t.Fatalf("turn %+v", turn)
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestResumeWithoutRosterWatchesAndALaterRosterLaunches(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	exited := statusExited
	for _, name := range []string{"alpha", "bravo"} {
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &launched})
		h.do("register", client.RegisterRequest{Conversation: "kept", Name: name})
		h.do("member", client.MemberRequest{Conversation: "kept", Name: name, Status: &exited})
		n := name
		s := "sess-" + name
		h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	}
	h.publish("kept", "alpha", "turn", "mode: parallel\nfirst", []string{"bravo"})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = false
	var stdout lockedBuffer
	var stderr bytes.Buffer
	cctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	h.save()
	go func() {
		errCh <- Execute(cctx, cfg, &stdout, &stderr)
	}()
	seen := time.Time{}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("exited before done: %v\n%s\n%s", err, stdout.String(), stderr.String())
		default:
		}
		if strings.Contains(stdout.String(), "conversation: kept") {
			if seen.IsZero() {
				seen = time.Now()
			} else if time.Since(seen) > 300*time.Millisecond {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if seen.IsZero() || strings.Contains(stdout.String(), "turn:") || strings.Contains(stdout.String(), "outcome:") {
		t.Fatalf("stdout %s", stdout.String())
	}
	if len(launches(h.calls(), "bravo")) != 0 || len(launches(h.calls(), "alpha")) != 0 {
		t.Fatalf("launched %+v", h.calls())
	}
	cancel()
	if err := <-errCh; err == nil {
		t.Fatal("cancel produced no error")
	}
	cfg.Agents = "alpha=hotseat/grok:g1 bravo=hotseat/claude:c1"
	cfg.AgentsSet = true
	cctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	errCh = make(chan error, 1)
	go func() {
		_, _, err := h.run(cctx, cfg)
		errCh <- err
	}()
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 1 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("run ended: %v calls %+v", err, h.calls())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(launches(h.calls(), "bravo")) != 1 {
		t.Fatalf("bravo launches %d %+v", len(launches(h.calls(), "bravo")), h.calls())
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRestartLaunchesAnOlderRecipientBeforeALaterTurn(t *testing.T) {
	h := newHarness(t)
	res := h.do("create", client.CreateRequest{
		Name: "kept", Task: "tasks:hotseat/review/ticket", Seat: "alpha",
		Directory: h.dir, Roster: []string{"alpha", "bravo"},
	})
	if res.Outcome != bus.OutcomeOK {
		t.Fatal(res.Reason)
	}
	h.publish("kept", "owner", kindInstructions, "Review\n\nseat: alpha\n", []string{"alpha", "bravo"})
	launched := statusLaunched
	exited := statusExited
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &launched})
	h.do("member", client.MemberRequest{Conversation: "kept", Name: "alpha", Status: &exited})
	n := "alpha"
	s := "sess-alpha"
	h.do("session", client.SessionRequest{Conversation: "kept", Name: &n, Session: &s})
	h.publish("kept", "alpha", "turn", "mode: serial\nold", []string{"bravo"})
	h.publish("kept", "alpha", "turn", "mode: serial\nnew", []string{"alpha"})
	cfg := h.base()
	cfg.Conversation = "kept"
	cfg.ConversationSet = true
	cfg.TaskSet = false
	cfg.TicketSet = false
	cfg.AgentsSet = true
	cctx, cancel := ctx()
	defer cancel()
	errCh := make(chan error, 1)
	outCh := make(chan string, 1)
	go func() {
		stdout, stderr, err := h.run(cctx, cfg)
		outCh <- stdout + stderr
		errCh <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 1 {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("run ended: %v %s calls %+v", err, <-outCh, h.calls())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	calls := h.calls()
	bravoAt, alphaAt := -1, -1
	for i, c := range calls {
		if c.Kind != "launch" {
			continue
		}
		if strings.Contains(c.Stdin, "You are bravo ") && bravoAt < 0 {
			bravoAt = i
		}
		if strings.Contains(c.Stdin, "You are alpha ") && alphaAt < 0 {
			alphaAt = i
		}
	}
	if bravoAt < 0 || (alphaAt >= 0 && alphaAt < bravoAt) {
		t.Fatalf("order bravo %d alpha %d", bravoAt, alphaAt)
	}
	h.publish("kept", "alpha", "done", "yes", nil)
	cancel()
}

func TestStdoutOmitsSessionPidAndToken(t *testing.T) {
	h := newHarness(t)
	tokenPath := filepath.Join(h.dir, "token")
	if err := os.WriteFile(tokenPath, []byte("TOKENVALUE99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := h.base()
	cfg.TicketSet = false
	cfg.TokenFile = tokenPath
	cfg.TokenFileSet = true
	cctx, cancel := ctx()
	defer cancel()
	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)
	go func() {
		stdout, stderr, _ := h.run(cctx, cfg)
		stdoutCh <- stdout
		stderrCh <- stderr
	}()
	name := h.waitName()
	h.publish(name, "alpha", "turn", "mode: parallel\nreview", []string{"bravo"})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(launches(h.calls(), "bravo")) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.publish(name, "alpha", "done", "yes", nil)
	stdout := <-stdoutCh
	stderr := <-stderrCh
	if strings.Contains(stdout, "sess-bravo") || strings.Contains(stdout, "TOKENVALUE99") || strings.Contains(stdout, "pid:") {
		t.Fatalf("stdout %s", stdout)
	}
	if strings.Contains(stderr, "sess-bravo") || strings.Contains(stderr, "TOKENVALUE99") {
		t.Fatalf("stderr %s", stderr)
	}
	for _, c := range h.calls() {
		if strings.Contains(c.Stdin, "sess-bravo") || strings.Contains(c.Stdin, "TOKENVALUE99") {
			t.Fatalf("prompt %s", c.Stdin)
		}
		if strings.Contains(c.Stdin, "Bus address:") && !strings.Contains(c.Stdin, h.addr) {
			t.Fatalf("prompt %s", c.Stdin)
		}
		if c.Kind == "launch" && !strings.Contains(c.Stdin, "Token file: "+tokenPath) {
			t.Fatalf("prompt missing token path %s", c.Stdin)
		}
	}
	if !strings.Contains(stdout, "exit: bravo") || !strings.Contains(stdout, "status: exited") {
		t.Fatalf("stdout %s", stdout)
	}
	cancel()
}

func (h *harness) memberStatus(conv, name string) string {
	h.t.Helper()
	res := h.do("member", client.MemberRequest{Conversation: conv, Name: name})
	if res.Member == nil {
		return ""
	}
	return res.Member.Status
}

func (h *harness) waitName() string {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		res := h.do("list", struct{}{})
		if res.Conversations != nil && len(*res.Conversations) == 1 {
			return (*res.Conversations)[0].Name
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("no conversation", h.calls())
	return ""
}

func countKind(msgs []client.Message, kind string) int {
	n := 0
	for _, m := range msgs {
		if m.Kind == kind {
			n++
		}
	}
	return n
}

func countBody(msgs []client.Message, part string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Body, part) {
			n++
		}
	}
	return n
}

type overlapWriter struct {
	mu     sync.Mutex
	inside int
	max    int
	buf    bytes.Buffer
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.inside++
	if w.inside > w.max {
		w.max = w.inside
	}
	w.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inside--
	return w.buf.Write(p)
}

func TestStatusRecordStaysTogether(t *testing.T) {
	dest := &overlapWriter{}
	w := &statusWriter{w: dest}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		w.record([2]string{"member", "alpha"}, [2]string{"status", "exited"})
	}()
	go func() {
		defer wg.Done()
		<-start
		w.record([2]string{"exit", "bravo"}, [2]string{"code", "0"})
	}()
	close(start)
	wg.Wait()
	if err := w.Err(); err != nil {
		t.Fatal(err)
	}
	got := dest.buf.String()
	alpha := "member: alpha\nstatus: exited\n"
	bravo := "exit: bravo\ncode: 0\n"
	if got != alpha+bravo && got != bravo+alpha {
		t.Fatalf("text %q", got)
	}
}

func TestStderrWritesDoNotOverlap(t *testing.T) {
	dest := &overlapWriter{}
	w := &lockedWriter{w: dest}
	const alpha = "alpha-failed\n"
	const bravo = "bravo-failed\n"
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if _, err := io.WriteString(w, alpha); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		if _, err := io.WriteString(w, bravo); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	if dest.max != 1 {
		t.Fatalf("overlap %d", dest.max)
	}
	got := dest.buf.String()
	if got != alpha+bravo && got != bravo+alpha {
		t.Fatalf("text %q", got)
	}
}
