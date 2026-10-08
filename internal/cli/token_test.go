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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
	"github.com/p3bot/hotseat/internal/store"
)

func TestLocalhostBindsOneLoopbackSocket(t *testing.T) {
	var logs lockedLog
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runBus(ctx, t.TempDir(), "localhost:0", "", bus.DefaultMaxBody, log)
	}()
	deadline := time.Now().Add(3 * time.Second)
	var addr string
	for addr == "" && time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("bus stopped: %v\n%s", err, logs.String())
		default:
		}
		const key = "addr="
		text := logs.String()
		if i := strings.Index(text, key); i >= 0 {
			fields := strings.Fields(text[i+len(key):])
			if len(fields) > 0 {
				addr = strings.Trim(fields[0], `"`)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("log %s", logs.String())
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("bound %s", addr)
	}
	if !strings.Contains(logs.String(), "token_required=false") {
		t.Fatalf("log %s", logs.String())
	}
	waitDial(t, addr, errCh)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bus did not stop")
	}
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestLoadTokenUnknownClassFailsClosed(t *testing.T) {
	_, err := loadToken(0, "")
	if err == nil || !strings.Contains(err.Error(), "not classified") {
		t.Fatalf("err = %v", err)
	}
	_, err = loadToken(bus.ListenLoopback, "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestTokenConfigurationDoesNotListen(t *testing.T) {
	const secret = "hstk-not-a-header value"
	tests := []struct {
		name   string
		listen string
		file   string
		want   string
		omit   string
	}{
		{"remote no file", "192.0.2.1:9", "", "requires a token file", ""},
		{"remote empty", "192.0.2.1:9", "\n", "empty", ""},
		{"remote blank", "192.0.2.1:9", " \t\n", "empty", ""},
		{"remote bad", "192.0.2.1:9", secret + "\n", "header value", secret},
		{"remote missing", "192.0.2.1:9", "missing", "token file", ""},
		{"loopback empty", "127.0.0.1:9", "\n", "empty", ""},
		{"loopback missing", "127.0.0.1:9", "missing", "token file", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			storeDir := filepath.Join(root, "store")
			args := []string{"bus", "start", "--store", storeDir, "--listen", tt.listen}
			if tt.file != "" {
				path := filepath.Join(root, "nope")
				if tt.file != "missing" {
					path = filepath.Join(root, "token")
					if err := os.WriteFile(path, []byte(tt.file), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				args = append(args, "--token-file", path)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := ExecuteArgs(ctx, args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v", err)
			}
			if tt.omit != "" && strings.Contains(err.Error(), tt.omit) {
				t.Fatal("error contains the token")
			}
			if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
				t.Fatalf("store directory was created: %v", statErr)
			}
		})
	}
}

func TestLoopbackIgnoresTokenFile(t *testing.T) {
	const token = "hstk-loopback-file"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runBus(ctx, t.TempDir(), addr, path, bus.DefaultMaxBody, slog.New(slog.DiscardHandler))
	}()
	waitDial(t, addr, errCh)
	defer func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("bus: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("bus did not stop")
		}
	}()

	for _, auth := range []string{"", "Bearer wrong-token"} {
		httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}}
		req, err := http.NewRequest(http.MethodPost, "http://"+addr+bus.PathCreate, bytes.NewReader([]byte(`{"name":"job"}`)))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set(bus.TokenHeader, auth)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		if cerr := resp.Body.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) {
			t.Fatal("response contains the token")
		}
		var out struct {
			Outcome        string `json:"outcome"`
			Reason         string `json:"reason"`
			AlreadyExisted *bool  `json:"already_existed"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		// Loopback ignores the token. The second call finds the name already there.
		if auth == "" && (out.Outcome != bus.OutcomeOK || out.AlreadyExisted == nil || *out.AlreadyExisted) {
			t.Fatalf("no token: %s", raw)
		}
		if auth != "" && (out.Outcome != bus.OutcomeOK || out.AlreadyExisted == nil || !*out.AlreadyExisted) {
			t.Fatalf("wrong token on loopback: %s", raw)
		}
	}
}

func TestTokenIsNotAFlag(t *testing.T) {
	const secret = "hstk-flag-secret"
	for _, args := range [][]string{
		{"list", "--token", secret},
		{"list", "--token=" + secret},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := execute(context.Background(), args, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected an error")
			}
			blob := stdout.String() + stderr.String() + err.Error()
			if strings.Contains(blob, secret) {
				t.Fatalf("error contains the token: %s", strings.ReplaceAll(blob, secret, "<token>"))
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout %s", stdout.String())
			}
		})
	}
}

func TestNonLoopbackClientUsesTokenFile(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	const wrong = "hstk-ffffffffffffffff"
	secrets := []string{token, wrong}
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	tokenPath := filepath.Join(root, "bus.token")
	wrongPath := filepath.Join(root, "other.token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrongPath, []byte(wrong+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr %T", ln.Addr())
	}
	port := strconv.Itoa(tcp.Port)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	// The listener is not loopback. The client still dials this machine.
	listenAddr := net.JoinHostPort("0.0.0.0", port)
	dialAddr := net.JoinHostPort("127.0.0.1", port)

	bin := buildBinary(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = stopBus(ctx, storeDir)
	})
	started := runBinCmd(t, bin, "bus", "start", "--store", storeDir, "--listen", listenAddr, "--token-file", tokenPath)
	if started.code != 0 {
		t.Fatalf("start %d\n%s%s", started.code, started.stdout, started.stderr)
	}
	assertBytesOmit(t, []byte(started.stdout+started.stderr), secrets)
	pid, bound, gotStore := parseBusReport(t, started.stdout)
	if bound != listenAddr || gotStore != storeDir {
		t.Fatalf("bound %s store %s", bound, gotStore)
	}
	busLine := awaitProc(t, pid, "cmdline", []byte(tokenPath))
	busEnv := readProc(t, pid, "environ")
	assertBytesOmit(t, busLine, secrets)
	assertBytesOmit(t, busEnv, secrets)
	waitDial(t, dialAddr, nil)
	running := runBinCmd(t, bin, "bus", "status", "--store", storeDir)
	if running.code != 0 || !strings.Contains(running.stdout, strconv.Itoa(pid)) {
		t.Fatalf("status %d\n%s%s", running.code, running.stdout, running.stderr)
	}
	assertBytesOmit(t, []byte(running.stdout+running.stderr), secrets)
	record, err := os.ReadFile(filepath.Join(storeDir, recordName))
	if err != nil {
		t.Fatal(err)
	}
	assertBytesOmit(t, record, secrets)

	refused := runBin(t, bin, secrets, "create", "--address", dialAddr, "--name", "job")
	if refused.Outcome != bus.OutcomeRefused || refused.Reason != bus.ReasonTokenRequired {
		t.Fatalf("create without token %+v", refused)
	}
	bad := runBin(t, bin, secrets, "create", "--address", dialAddr, "--token-file", wrongPath, "--name", "job")
	if bad.Outcome != bus.OutcomeRefused || bad.Reason != bus.ReasonTokenRejected {
		t.Fatalf("wrong token %+v", bad)
	}
	empty := runBin(t, bin, secrets, "list", "--address", dialAddr, "--token-file", tokenPath)
	if empty.Outcome != bus.OutcomeOK || empty.Conversations == nil || len(*empty.Conversations) != 0 {
		t.Fatalf("list %+v", empty.Conversations)
	}

	created := runBin(t, bin, secrets, "create", "--address", dialAddr, "--token-file", tokenPath, "--name", "job")
	if created.Outcome != bus.OutcomeOK || created.Conversation == nil || created.Conversation.Name != "job" {
		t.Fatalf("create %+v", created.Conversation)
	}
	missed := runBin(t, bin, secrets, "publish", "--address", dialAddr, "--token-file", wrongPath,
		"--conversation", "job", "--from", "alice", "--to", "bob", "--body", "one", "--txid", "k1")
	if missed.Outcome != bus.OutcomeRefused || missed.Reason != bus.ReasonTokenRejected {
		t.Fatalf("publish wrong %+v", missed)
	}
	none := runBin(t, bin, secrets, "read", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--cursor", "0", "--limit", "10")
	if none.Outcome != bus.OutcomeOK || none.Messages == nil || len(*none.Messages) != 0 {
		t.Fatalf("read %+v", none.Messages)
	}
	alice := runBin(t, bin, secrets, "publish", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--from", "alice", "--to", "bob", "--body", "one", "--txid", "k1")
	carol := runBin(t, bin, secrets, "publish", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--from", "carol", "--to", "bob", "--body", "two", "--txid", "k2")
	if alice.Message == nil || alice.Message.From != "alice" || carol.Message == nil || carol.Message.From != "carol" {
		t.Fatalf("from %v %v", alice.Message, carol.Message)
	}
	read := runBin(t, bin, secrets, "read", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--cursor", "0", "--limit", "10")
	if read.Messages == nil || len(*read.Messages) != 2 || (*read.Messages)[0].From != "alice" || (*read.Messages)[1].From != "carol" {
		t.Fatalf("transcript %+v", read.Messages)
	}

	wctx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	bare := exec.CommandContext(wctx, bin, "wait", "--address", dialAddr, "--conversation", "job", "--cursor", "0", "--name", "bob", "--deadline", "30s")
	var bareOut, bareErr bytes.Buffer
	bare.Stdout = &bareOut
	bare.Stderr = &bareErr
	if err := bare.Run(); err != nil {
		t.Fatalf("wait without token: %v", err)
	}
	bareRes := parseStdout(t, bareOut.Bytes())
	if bareRes.Outcome != bus.OutcomeRefused || bareRes.Reason != bus.ReasonTokenRequired {
		t.Fatalf("bare wait %+v", bareRes)
	}
	if bytes.Contains(bareOut.Bytes(), []byte(token)) || bytes.Contains(bareErr.Bytes(), []byte(token)) {
		t.Fatal("wait output contains the token")
	}

	waited := runBin(t, bin, secrets, "wait", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--cursor", "0", "--name", "bob", "--deadline", "5s")
	if waited.Outcome != bus.OutcomeOK || waited.MatchSeq == nil || waited.Messages == nil || len(*waited.Messages) == 0 {
		t.Fatalf("wait %+v", waited)
	}
	open := runBin(t, bin, secrets, "close", "--address", dialAddr, "--conversation", "job")
	if open.Outcome != bus.OutcomeRefused || open.Reason != bus.ReasonTokenRequired {
		t.Fatalf("close without token %+v", open)
	}

	parked := exec.Command(bin, "wait", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--cursor", "99", "--name", "zed", "--deadline", "30s")
	assertArgsOmit(t, parked.Args, secrets)
	if err := parked.Start(); err != nil {
		t.Fatal(err)
	}
	line := awaitProc(t, parked.Process.Pid, "cmdline", []byte(tokenPath))
	env := readProc(t, parked.Process.Pid, "environ")
	assertBytesOmit(t, line, secrets)
	assertBytesOmit(t, env, secrets)
	_ = parked.Process.Kill()
	_ = parked.Wait()

	dave := runBin(t, bin, secrets, "publish", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--from", "dave", "--to", "bob", "--body", "from-bin", "--txid", "bin-1")
	if dave.Outcome != bus.OutcomeOK || dave.Message == nil || dave.Message.From != "dave" || dave.Message.Body != "from-bin" {
		t.Fatalf("dave %+v", dave.Message)
	}
	page := runBin(t, bin, secrets, "read", "--address", dialAddr, "--token-file", tokenPath,
		"--conversation", "job", "--cursor", "0", "--limit", "10")
	sawDave := false
	if page.Messages != nil {
		for _, m := range *page.Messages {
			if m.From == "dave" && m.Body == "from-bin" {
				sawDave = true
			}
		}
	}
	if !sawDave {
		t.Fatal("binary read missed the publish")
	}

	shut := runBin(t, bin, secrets, "close", "--address", dialAddr, "--token-file", tokenPath, "--conversation", "job")
	if shut.Outcome != bus.OutcomeOK || shut.Conversation == nil || shut.Conversation.Status != "closed" {
		t.Fatalf("close %+v", shut.Conversation)
	}
	final := runBin(t, bin, secrets, "list", "--address", dialAddr, "--token-file", tokenPath)
	if final.Conversations == nil || len(*final.Conversations) != 1 || (*final.Conversations)[0].Status != "closed" {
		t.Fatalf("final %+v", final.Conversations)
	}

	stopped := runBinCmd(t, bin, "bus", "stop", "--store", storeDir)
	if stopped.code != 0 {
		t.Fatalf("stop %d\n%s%s", stopped.code, stopped.stdout, stopped.stderr)
	}
	assertBytesOmit(t, []byte(stopped.stdout+stopped.stderr), secrets)
	logText, err := os.ReadFile(filepath.Join(storeDir, logName))
	if err != nil {
		t.Fatal(err)
	}
	assertBytesOmit(t, logText, secrets)
	if !bytes.Contains(logText, []byte("token_required=true")) {
		t.Fatalf("bus log missing token_required: %s", logText)
	}
	db := filepath.Join(storeDir, store.FileName)
	if _, err := os.Stat(db); err != nil {
		t.Fatal(err)
	}
	assertTreeOmits(t, storeDir, secrets...)
}

func assertTreeOmits(t *testing.T, root string, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertBytesOmit(t, b, secrets)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runBin(t *testing.T, bin string, secrets []string, args ...string) client.Result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	assertArgsOmit(t, cmd.Args, secrets)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String() + stderr.String()
	for _, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Fatalf("%v output contains a token", args[0])
		}
	}
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return parseStdout(t, stdout.Bytes())
}

func assertArgsOmit(t *testing.T, args, secrets []string) {
	t.Helper()
	for _, arg := range args {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(arg, secret) {
				t.Fatal("argument contains a token")
			}
		}
	}
}

func assertBytesOmit(t *testing.T, raw []byte, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("bytes contain a token")
		}
	}
}

func readProc(t *testing.T, pid int, name string) []byte {
	t.Helper()
	return awaitProc(t, pid, name, nil)
}

func awaitProc(t *testing.T, pid int, name string, needle []byte) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last []byte
	var lastErr error
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), name))
		if err == nil && (len(needle) == 0 || bytes.Contains(b, needle)) && len(b) > 0 {
			return b
		}
		last = b
		lastErr = err
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("read %s for %d: %v len %d", name, pid, lastErr, len(last))
	return nil
}

func waitDial(t *testing.T, addr string, errCh <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if errCh != nil {
			select {
			case err := <-errCh:
				t.Fatalf("bus stopped: %v", err)
			default:
			}
		}
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not listen on %s: %v", addr, last)
}
