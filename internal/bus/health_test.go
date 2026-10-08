// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

func TestHealthPassIgnoresToken(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	const wrong = "hstk-ffffffffffffffff"
	var logs bytes.Buffer
	base, dir, _, _ := startServer(t, Options{
		Token:  token,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	for _, auth := range []string{"", "Bearer " + wrong, "Bearer " + token} {
		code, body := getHealth(t, base, auth)
		if code != http.StatusOK || body != "pass\n" {
			t.Fatalf("auth %q -> %d %q", auth, code, body)
		}
		if strings.Contains(body, token) || strings.Contains(body, wrong) || strings.Contains(body, dir) {
			t.Fatalf("body %q", body)
		}
	}
	posted, raw := authPost(t, base, PathHealth, "", []byte(`{}`), []string{token, wrong})
	if posted.Outcome != OutcomeRefused || posted.Reason != ReasonTokenRequired || bytes.Contains(raw, []byte("pass")) {
		t.Fatalf("POST /health %+v %s", posted, raw)
	}
	withToken, raw := authPost(t, base, PathHealth, "Bearer "+token, []byte(`{}`), []string{token})
	if withToken.Outcome != OutcomeRefused || withToken.Reason != ReasonUnknownOp {
		t.Fatalf("POST /health with token %+v %s", withToken, raw)
	}
	missing, _ := authPost(t, base, PathList, "", []byte(`{}`), []string{token})
	if missing.Outcome != OutcomeRefused || missing.Reason != ReasonTokenRequired {
		t.Fatalf("list %+v", missing)
	}
	text := logs.String()
	if !strings.Contains(text, "op=health") || !strings.Contains(text, "outcome=pass") {
		t.Fatalf("log %s", text)
	}
	if strings.Contains(text, token) || strings.Contains(text, wrong) || strings.Contains(text, dir) {
		t.Fatalf("log %s", text)
	}

	open, openDir, _, _ := startServer(t, Options{})
	code, body := getHealth(t, open, "Bearer hstk-not-required")
	if code != http.StatusOK || body != "pass\n" || strings.Contains(body, openDir) || strings.Contains(body, "hstk-not-required") {
		t.Fatalf("loopback %d %q", code, body)
	}
	posted, raw = authPost(t, open, PathHealth, "", []byte(`{}`), nil)
	if posted.Outcome != OutcomeRefused || posted.Reason != ReasonUnknownOp || string(raw) == "pass\n" {
		t.Fatalf("loopback POST /health %+v %s", posted, raw)
	}
}

func TestHealthFail(t *testing.T) {
	base, dir, _, _ := startServer(t, Options{
		schema: func(context.Context) (string, error) { return "99", nil },
	})
	code, body := getHealth(t, base, "")
	if code != http.StatusServiceUnavailable || body != "fail\n" || strings.Contains(body, dir) {
		t.Fatalf("mismatch %d %q", code, body)
	}

	broken, _, _, _ := startServer(t, Options{
		schema: func(context.Context) (string, error) { return "", errors.New("schema") },
	})
	code, body = getHealth(t, broken, "Bearer hstk-ffffffffffffffff")
	if code != http.StatusServiceUnavailable || body != "fail\n" {
		t.Fatalf("read %d %q", code, body)
	}

	closed, closedDir, live, _ := startServer(t, Options{})
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	code, body = getHealth(t, closed, "")
	if code != http.StatusServiceUnavailable || body != "fail\n" || strings.Contains(body, closedDir) {
		t.Fatalf("closed %d %q", code, body)
	}
}

func TestHealthReadErrorIsLogged(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	var logs bytes.Buffer
	base, dir, _, _ := startServer(t, Options{
		Token:  token,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		schema: func(context.Context) (string, error) {
			return "", errors.New("meta missing")
		},
	})
	code, body := getHealth(t, base, "")
	if code != http.StatusServiceUnavailable || body != "fail\n" {
		t.Fatalf("read %d %q", code, body)
	}
	text := logs.String()
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "outcome=fail") || !strings.Contains(text, "meta missing") {
		t.Fatalf("log %s", text)
	}

	logs.Reset()
	base, _, _, _ = startServer(t, Options{
		Token:  token,
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		schema: func(context.Context) (string, error) {
			return "", errors.New("open " + dir + " with " + token)
		},
	})
	code, body = getHealth(t, base, "")
	if code != http.StatusServiceUnavailable || body != "fail\n" {
		t.Fatalf("hidden %d %q", code, body)
	}
	text = logs.String()
	if !strings.Contains(text, "err=schema") || strings.Contains(text, dir) || strings.Contains(text, token) {
		t.Fatalf("log %s", text)
	}

	logs.Reset()
	base, _, _, _ = startServer(t, Options{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		schema: func(context.Context) (string, error) { return "99", nil },
	})
	code, body = getHealth(t, base, "")
	if code != http.StatusServiceUnavailable || body != "fail\n" {
		t.Fatalf("version %d %q", code, body)
	}
	text = logs.String()
	if !strings.Contains(text, "level=INFO") || !strings.Contains(text, "outcome=fail") || strings.Contains(text, "err=") {
		t.Fatalf("log %s", text)
	}
}

func TestHealthCancelIsDropped(t *testing.T) {
	var logs lockedLog
	started := make(chan struct{})
	base, _, _, _ := startServer(t, Options{
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		schema: func(ctx context.Context) (string, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+PathHealth, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-started
		cancel()
	}()
	resp, err := httpClient.Do(req)
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	var text string
	for {
		text = logs.String()
		if strings.Contains(text, "outcome=dropped") || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(text, "outcome=dropped") || strings.Contains(text, "outcome=fail") || strings.Contains(text, "level=ERROR") {
		t.Fatalf("log %s err %v", text, err)
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

func TestHealthDecision(t *testing.T) {
	code, word := healthDecision(store.SchemaVersion, nil)
	if code != http.StatusOK || word != "pass" {
		t.Fatalf("pass %d %s", code, word)
	}
	code, word = healthDecision("99", nil)
	if code != http.StatusServiceUnavailable || word != "fail" {
		t.Fatalf("version %d %s", code, word)
	}
	code, word = healthDecision(store.SchemaVersion, errors.New("schema"))
	if code != http.StatusServiceUnavailable || word != "fail" {
		t.Fatalf("err %d %s", code, word)
	}
}

func getHealth(t *testing.T, base, auth string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+PathHealth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set(TokenHeader, auth)
	}
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
	return resp.StatusCode, string(raw)
}
