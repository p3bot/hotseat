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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenFileAndHeader(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(token+"\r\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTokenFile(path)
	if err != nil || got != token {
		t.Fatalf("token %q err %v", got, err)
	}

	h := make(http.Header)
	if err := WriteToken(h, got); err != nil {
		t.Fatal(err)
	}
	if h.Get(TokenHeader) != "Bearer "+token {
		t.Fatalf("header %q", h.Get(TokenHeader))
	}
	if checkToken(h, token) != tokenOK {
		t.Fatal("round trip")
	}
	lower := h.Clone()
	lower.Set(TokenHeader, "bEaReR "+token)
	if checkToken(lower, token) != tokenOK {
		t.Fatal("scheme case")
	}
	if checkToken(nil, token) != tokenMissing {
		t.Fatal("nil header")
	}
	if checkToken(make(http.Header), token) != tokenMissing {
		t.Fatal("absent header")
	}
	if checkToken(h, "") != tokenRejected {
		t.Fatal("empty want")
	}
	wrong := h.Clone()
	wrong.Set(TokenHeader, "Bearer hstk-ffffffffffffffff")
	if checkToken(wrong, token) != tokenRejected {
		t.Fatal("wrong token")
	}
	basic := h.Clone()
	basic.Set(TokenHeader, "Basic "+token)
	if checkToken(basic, token) != tokenRejected {
		t.Fatal("wrong scheme")
	}

	if err := WriteToken(nil, token); err == nil {
		t.Fatal("nil header was accepted")
	}
	blank := make(http.Header)
	if err := WriteToken(blank, ""); err != nil || blank.Get(TokenHeader) != "" {
		t.Fatalf("empty token header %q err %v", blank.Get(TokenHeader), err)
	}
	spaced := "sekret-zzzz extra"
	if err := WriteToken(blank, spaced); err == nil || strings.Contains(err.Error(), "sekret-zzzz") || strings.Contains(err.Error(), "extra") {
		t.Fatalf("err %v", err)
	}
	if blank.Get(TokenHeader) != "" {
		t.Fatal("rejected token was written")
	}

	for _, raw := range []string{"", "\n", " \t\n"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadTokenFile(path)
		if err == nil || !strings.Contains(err.Error(), "empty") || strings.Contains(err.Error(), token) {
			t.Fatalf("raw %q err %v", raw, err)
		}
	}
	bad := "nope nope"
	if err := os.WriteFile(path, []byte(bad+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadTokenFile(path)
	if err == nil || strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "header value") {
		t.Fatalf("err %v", err)
	}
	_, err = ReadTokenFile("")
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err %v", err)
	}
	_, err = ReadTokenFile(filepath.Join(dir, "missing"))
	if err == nil || !strings.Contains(err.Error(), "token file") || strings.Contains(err.Error(), token) {
		t.Fatalf("err %v", err)
	}
}

func TestTokenGateRefusesBeforeWrite(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	const wrong = "hstk-ffffffffffffffff"
	var logs bytes.Buffer
	base, dir, _, _ := startServer(t, Options{
		Token:  token,
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	secrets := []string{token, wrong}

	missing, _ := authPost(t, base, PathCreate, "", []byte(`{"name":"job"}`), secrets)
	if missing.Outcome != OutcomeRefused || missing.Reason != ReasonTokenRequired {
		t.Fatalf("missing %+v", missing)
	}
	listed, _ := authPost(t, base, PathList, "Bearer "+token, []byte(`{}`), secrets)
	if listed.Outcome != OutcomeOK || listed.Conversations == nil || len(*listed.Conversations) != 0 {
		t.Fatalf("list after refused create %+v", listed.Conversations)
	}

	garbage, _ := authPost(t, base, PathPublish, "Bearer "+wrong, []byte("not-json"), secrets)
	if garbage.Outcome != OutcomeRefused || garbage.Reason != ReasonTokenRejected {
		t.Fatalf("garbage %+v", garbage)
	}
	unknown, _ := authPost(t, base, "/nope", "", []byte(`{}`), secrets)
	if unknown.Outcome != OutcomeRefused || unknown.Reason != ReasonTokenRequired {
		t.Fatalf("unknown without token %+v", unknown)
	}
	named, _ := authPost(t, base, "/nope", "bearer "+token, []byte(`{}`), secrets)
	if named.Outcome != OutcomeRefused || named.Reason != ReasonUnknownOp {
		t.Fatalf("unknown with token %+v", named)
	}

	created, _ := authPost(t, base, PathCreate, "Bearer "+token, []byte(`{"name":"job"}`), secrets)
	if created.Outcome != OutcomeOK || created.Conversation == nil || created.Conversation.Name != "job" {
		t.Fatalf("create %+v", created.Conversation)
	}
	badKey, _ := authPost(t, base, PathPublish, "Bearer "+wrong, []byte(`{"conversation":"job","from":"alice","to":["bob"],"body":"one","txid":"k1"}`), secrets)
	if badKey.Outcome != OutcomeRefused || badKey.Reason != ReasonTokenRejected {
		t.Fatalf("bad publish %+v", badKey)
	}
	empty, _ := authPost(t, base, PathRead, "Bearer "+token, []byte(`{"conversation":"job","cursor":0,"limit":10}`), secrets)
	if empty.Outcome != OutcomeOK || empty.Messages == nil || len(*empty.Messages) != 0 {
		t.Fatalf("read after refused publish %+v", empty.Messages)
	}

	alice := publishAuth(t, base, token, "alice", "k1", "one", secrets)
	if alice.From != "alice" || alice.Body != "one" {
		t.Fatalf("alice %+v", alice)
	}
	replay, _ := authPost(t, base, PathPublish, "Bearer "+wrong, []byte(`{"conversation":"job","from":"alice","to":["bob"],"body":"one","txid":"k1"}`), secrets)
	if replay.Outcome != OutcomeRefused || replay.Reason != ReasonTokenRejected || replay.AlreadyStored != nil {
		t.Fatalf("replay without token %+v", replay)
	}
	again := publishAuth(t, base, token, "alice", "k1", "one", secrets)
	if again.Seq != alice.Seq {
		t.Fatalf("seq %d then %d", alice.Seq, again.Seq)
	}
	carol := publishAuth(t, base, token, "carol", "k2", "two", secrets)
	if carol.From != "carol" {
		t.Fatalf("carol %+v", carol)
	}
	read, _ := authPost(t, base, PathRead, "Bearer "+token, []byte(`{"conversation":"job","cursor":0,"limit":10}`), secrets)
	if read.Messages == nil || len(*read.Messages) != 2 || (*read.Messages)[0].From != "alice" || (*read.Messages)[1].From != "carol" {
		t.Fatalf("messages %+v", read.Messages)
	}

	waited, _ := authPost(t, base, PathWait, "", []byte(`{"conversation":"job","cursor":0,"name":"bob","deadline":"30s"}`), secrets)
	if waited.Outcome != OutcomeRefused || waited.Reason != ReasonTokenRequired {
		t.Fatalf("wait %+v", waited)
	}
	closed, _ := authPost(t, base, PathClose, "", []byte(`{"conversation":"job"}`), secrets)
	if closed.Outcome != OutcomeRefused || closed.Reason != ReasonTokenRequired {
		t.Fatalf("close %+v", closed)
	}
	still, _ := authPost(t, base, PathList, "Bearer "+token, []byte(`{}`), secrets)
	if still.Conversations == nil || len(*still.Conversations) != 1 || (*still.Conversations)[0].Status != "open" {
		t.Fatalf("still %+v", still.Conversations)
	}
	shut, _ := authPost(t, base, PathClose, "Bearer "+token, []byte(`{"conversation":"job"}`), secrets)
	if shut.Outcome != OutcomeOK || shut.Conversation == nil || shut.Conversation.Status != "closed" {
		t.Fatalf("shut %+v", shut.Conversation)
	}

	if bytes.Contains(logs.Bytes(), []byte(token)) || bytes.Contains(logs.Bytes(), []byte(wrong)) {
		t.Fatal("log contains a token")
	}
	assertTreeOmits(t, dir, secrets...)
}

func TestLoopbackListenerIgnoresPresentedToken(t *testing.T) {
	base, _, _, _ := startServer(t, Options{})
	res, raw := authPost(t, base, PathCreate, "Bearer hstk-not-required", []byte(`{"name":"job"}`), []string{"hstk-not-required"})
	if res.Outcome != OutcomeOK || bytes.Contains(raw, []byte("hstk-not-required")) {
		t.Fatalf("create %s", raw)
	}
}

func publishAuth(t *testing.T, base, token, from, key, body string, secrets []string) wireMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"conversation": "job",
		"from":         from,
		"to":           []string{"bob"},
		"body":         body,
		"txid":         key,
		"kind":         "say",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := authPost(t, base, PathPublish, "Bearer "+token, payload, secrets)
	if res.Outcome != OutcomeOK || res.Message == nil || res.AlreadyStored == nil {
		t.Fatalf("publish %s %+v", key, res)
	}
	return *res.Message
}

func authPost(t *testing.T, base, path, auth string, body []byte, secrets []string) (wire, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set(TokenHeader, auth)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status %d", path, resp.StatusCode)
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("%s response contains a token", path)
		}
	}
	var out wire
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s json: %v", path, err)
	}
	return out, raw
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
		for _, secret := range secrets {
			if secret != "" && bytes.Contains(b, []byte(secret)) {
				t.Errorf("%s contains a token", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
