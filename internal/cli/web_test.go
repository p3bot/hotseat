// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

func TestWebCommandServesList(t *testing.T) {
	wd, home := isolateHome(t)
	busAddr, stop := startBus(t)
	defer stop()
	res, err := client.Do(context.Background(), busAddr, "create", "", client.CreateRequest{Name: "job"})
	if err != nil || res.Outcome != bus.OutcomeOK {
		t.Fatalf("create %v %+v", err, res)
	}
	webAddr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- execute(ctx, []string{"web", "--address", busAddr, "--listen", webAddr}, io.Discard, &stderr)
	}()
	waitDial(t, webAddr, errCh)

	status, body := httpGet(t, "http://"+webAddr+"/")
	if status != http.StatusOK || !strings.Contains(body, ">job<") || !strings.Contains(body, `class="status">open<`) {
		t.Fatalf("list %d %s", status, body)
	}
	cancel()
	waitErr(t, errCh)
	if !strings.Contains(stderr.String(), "token_configured=false") {
		t.Fatalf("stderr %s", stderr.String())
	}
	assertEmptyDir(t, wd)
	assertEmptyDir(t, home)
}

func TestWebCommandUsesTokenFile(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	wd, home := isolateHome(t)
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	tokenPath := filepath.Join(root, "token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
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
	listenAddr := net.JoinHostPort("0.0.0.0", port)
	dialAddr := net.JoinHostPort("127.0.0.1", port)

	busCtx, busCancel := context.WithCancel(context.Background())
	defer busCancel()
	var busLogs bytes.Buffer
	busErr := make(chan error, 1)
	go func() {
		busErr <- runBus(busCtx, storeDir, listenAddr, tokenPath, bus.DefaultMaxBody, slog.New(slog.NewTextHandler(&busLogs, nil)))
	}()
	waitDial(t, dialAddr, busErr)

	created, err := client.Do(context.Background(), dialAddr, "create", token, client.CreateRequest{Name: "job"})
	if err != nil || created.Outcome != bus.OutcomeOK {
		t.Fatalf("create %v %+v", err, created)
	}

	webAddr := freeAddr(t)
	webCtx, webCancel := context.WithCancel(context.Background())
	defer webCancel()
	var stderr bytes.Buffer
	webErr := make(chan error, 1)
	go func() {
		webErr <- execute(webCtx, []string{
			"web", "--address", dialAddr, "--listen", webAddr, "--token-file", tokenPath,
		}, io.Discard, &stderr)
	}()
	waitDial(t, webAddr, webErr)

	_, body := httpGet(t, "http://"+webAddr+"/")
	form := url.Values{}
	form.Set("conversation", "job")
	form.Set("from", "alice")
	form.Set("to", "bob")
	form.Set("body", "hello")
	form.Set("txid", "idem-web")
	_, stored := httpPost(t, "http://"+webAddr+"/publish", form)
	_, read := httpGet(t, "http://"+webAddr+"/c?conversation=job&cursor=0&limit=10")
	if !strings.Contains(body, ">job<") || !strings.Contains(stored, `id="stored"`) || !strings.Contains(read, ">hello<") {
		t.Fatalf("list %s\npublish %s\nread %s", body, stored, read)
	}
	for _, part := range []struct{ name, text string }{
		{"list", body},
		{"publish", stored},
		{"read", read},
		{"stderr", stderr.String()},
		{"bus", busLogs.String()},
	} {
		if strings.Contains(part.text, token) {
			t.Fatalf("%s contains the token", part.name)
		}
	}
	if !strings.Contains(stderr.String(), "token_configured=true") {
		t.Fatalf("stderr %s", stderr.String())
	}
	webCancel()
	waitErr(t, webErr)
	busCancel()
	waitErr(t, busErr)
	assertEmptyDir(t, wd)
	assertEmptyDir(t, home)
}

func TestWebNonLoopbackDoesNotListen(t *testing.T) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
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
	listenAddr := net.JoinHostPort("0.0.0.0", port)
	dialAddr := net.JoinHostPort("127.0.0.1", port)
	err = execute(context.Background(), []string{"web", "--listen", listenAddr}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "page listen address must be loopback") {
		t.Fatalf("err %v", err)
	}
	conn, dialErr := net.DialTimeout("tcp", dialAddr, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("listened")
	}
}

func TestWebBadTokenFileDoesNotListen(t *testing.T) {
	addr := freeAddr(t)
	err := execute(context.Background(), []string{
		"web", "--listen", addr, "--token-file", filepath.Join(t.TempDir(), "missing"),
	}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "token file") {
		t.Fatalf("err %v", err)
	}
	conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("listened")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitErr(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

func httpGet(t *testing.T, rawURL string) (int, string) {
	t.Helper()
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func httpPost(t *testing.T, rawURL string, form url.Values) (int, string) {
	t.Helper()
	resp, err := http.PostForm(rawURL, form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}
