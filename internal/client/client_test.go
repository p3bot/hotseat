// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/p3bot/hotseat/internal/bus"
)

func TestDroppedCallRetriesTheSameBody(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "token", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		if n == 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"refused","reason":"conversation not found"}`)
	}))
	defer srv.Close()

	name := "bob"
	res, err := Do(context.Background(), srv.Listener.Addr().String(), "wait", "", WaitRequest{
		Conversation: "job",
		Cursor:       3,
		Name:         &name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonNotFound {
		t.Fatalf("result %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("bodies %q", bodies)
	}
	if !bytes.Contains(bodies[0], []byte(`"cursor":3`)) || !bytes.Contains(bodies[0], []byte(`"name":"bob"`)) {
		t.Fatalf("body %s", bodies[0])
	}
}

func TestDroppedLaunchReturnsTheStoredRow(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		if n == 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		}
		if n == 1 {
			_, _ = io.WriteString(w, `{"outcome":"refused","reason":"`+bus.ReasonAlreadyLaunched+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"ok","member":{"name":"alpha","status":"launched","launched":"2026-10-10T01:00:00Z","registered":"","pid":424242}}`)
	}))
	defer srv.Close()

	status := "launched"
	pid := int64(424242)
	res, err := Do(context.Background(), srv.Listener.Addr().String(), "member", "", MemberRequest{
		Conversation: "job",
		Name:         "alpha",
		Status:       &status,
		PID:          &pid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeOK || res.Member == nil || res.Member.Name != "alpha" || res.Member.Status != "launched" || res.Member.PID == nil || *res.Member.PID != pid {
		t.Fatalf("result %+v", res.Member)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("bodies %q", bodies)
	}
	if bytes.Contains(bodies[2], []byte(`"status"`)) || bytes.Contains(bodies[2], []byte(`"pid"`)) {
		t.Fatalf("read sent a write %s", bodies[2])
	}
}

func TestSecondLaunchIsStillRefused(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		_, _ = io.WriteString(w, `{"outcome":"refused","reason":"`+bus.ReasonAlreadyLaunched+`"}`)
	}))
	defer srv.Close()

	status := "launched"
	res, err := Do(context.Background(), srv.Listener.Addr().String(), "member", "", MemberRequest{
		Conversation: "job",
		Name:         "alpha",
		Status:       &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonAlreadyLaunched {
		t.Fatalf("result %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("posts = %d", n)
	}
}

func TestDroppedMemberRefusalIsReturned(t *testing.T) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen := n
		n++
		mu.Unlock()
		if seen == 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"refused","reason":"`+bus.ReasonNotInRoster+`"}`)
	}))
	defer srv.Close()

	status := "running"
	res, err := Do(context.Background(), srv.Listener.Addr().String(), "member", "", MemberRequest{
		Conversation: "job",
		Name:         "alpha",
		Status:       &status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonNotInRoster {
		t.Fatalf("result %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Fatalf("posts = %d", n)
	}
}

func TestDialFailureIsNotRetried(t *testing.T) {
	dials := 0
	restore := swapClient(&http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials++
				return nil, &net.OpError{Op: "dial", Net: network, Addr: fakeAddr(addr), Err: errors.New("refused")}
			},
		},
	})
	defer restore()

	res, err := Do(context.Background(), "192.0.2.1:9", "list", "", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeConnectionFailure || !strings.Contains(res.Reason, reasonConnect+": 192.0.2.1:9:") || !strings.Contains(res.Reason, "refused") {
		t.Fatalf("result %+v", res)
	}
	if dials != 1 {
		t.Fatalf("dials = %d", dials)
	}
}

func TestDialTimeoutIsConnectionFailure(t *testing.T) {
	restore := swapClient(&http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: network, Addr: fakeAddr(addr), Err: context.DeadlineExceeded}
			},
		},
	})
	defer restore()

	res, err := Do(context.Background(), "192.0.2.1:9", "list", "", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeConnectionFailure || !strings.Contains(res.Reason, reasonConnect+": 192.0.2.1:9:") || !strings.Contains(res.Reason, "deadline exceeded") {
		t.Fatalf("result %+v", res)
	}
}

func TestClosedConnectionIsNotTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepts := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepts++
			_ = conn.Close()
		}
	}()

	res, err := Do(context.Background(), ln.Addr().String(), "wait", "", WaitRequest{
		Conversation: "job",
		Cursor:       1,
		Name:         strPtr("bob"),
	})
	_ = ln.Close()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeConnectionFailure || !strings.HasPrefix(res.Reason, reasonDropped+": "+ln.Addr().String()+":") {
		t.Fatalf("result %+v", res)
	}
	if accepts != 2 {
		t.Fatalf("accepts = %d, want one retry", accepts)
	}
}

func TestNonBusResponseIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	res, err := Do(context.Background(), addr, "list", "", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if res.Outcome != OutcomeConnectionFailure || res.Reason != "http 404 from "+addr {
		t.Fatalf("result %+v", res)
	}
}

func TestUnexpectedOutcomeIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"outcome":"nope"}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	res, err := Do(context.Background(), addr, "list", "", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if res.Outcome != OutcomeConnectionFailure || res.Reason != `unexpected outcome "nope" from `+addr {
		t.Fatalf("result %+v", res)
	}
}

func TestCompletedOutcomesAreNotRetried(t *testing.T) {
	for _, body := range []string{
		`{"outcome":"timeout"}`,
		`{"outcome":"unavailable","reason":"could not make the change durable"}`,
		`{"outcome":"refused","reason":"conversation not found"}`,
		`{"outcome":"ok","messages":[]}`,
	} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			res, err := Do(context.Background(), srv.Listener.Addr().String(), "read", "", ReadRequest{
				Conversation: "job",
				Cursor:       0,
				Limit:        1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
			if body == `{"outcome":"timeout"}` {
				if res.Outcome != bus.OutcomeTimeout || res.Messages != nil {
					t.Fatalf("result %+v", res)
				}
			}
			if body == `{"outcome":"refused","reason":"conversation not found"}` {
				if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonNotFound {
					t.Fatalf("result %+v", res)
				}
			}
		})
	}
}

func TestCanceledCallDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Do(ctx, "127.0.0.1:9", "list", "", struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestBadAddressDoesNotDial(t *testing.T) {
	_, err := Do(context.Background(), "not-an-address", "list", "", struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestPublishSendsEmptyToAndTheCallerKey(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"ok","already_stored":false,"message":{"seq":1,"time":"t","from":"alice","to":[],"body":"<b>","txid":"k"}}`)
	}))
	defer srv.Close()
	res, err := Do(context.Background(), srv.Listener.Addr().String(), "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         "<b>",
		TxID:         "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"to":[]`)) || !bytes.Contains(got, []byte(`"txid":"k"`)) || !bytes.Contains(got, []byte(`"<b>"`)) {
		t.Fatalf("body %s", got)
	}
	if res.Message == nil || res.Message.TxID != "k" || res.Message.Body != "<b>" || res.AlreadyStored == nil || *res.AlreadyStored {
		t.Fatalf("result %+v", res)
	}
}

func TestInvalidUTF8PublishBodyDoesNotPost(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"outcome":"ok","message":{"seq":1,"time":"t","from":"alice","to":[],"body":"x","txid":"k"}}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	res, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         string([]byte{0xff}),
		TxID:         "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonBodyUTF8 {
		t.Fatalf("result %+v", res)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
	okRes, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         "café",
		TxID:         "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if okRes.Outcome != bus.OutcomeOK || calls != 1 {
		t.Fatalf("outcome %s calls %d", okRes.Outcome, calls)
	}
}

func TestInvalidUTF8PublishKeyDoesNotPost(t *testing.T) {
	calls := 0
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"ok","already_stored":false,"message":{"seq":1,"time":"t","from":"alice","to":[],"body":"hi","txid":"k"}}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	res, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         "hi",
		TxID:         string([]byte{0xff}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonKeyUTF8 {
		t.Fatalf("result %+v", res)
	}
	both, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         string([]byte{0xfe}),
		TxID:         string([]byte{0xff}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if both.Outcome != bus.OutcomeRefused || both.Reason != bus.ReasonBodyUTF8 {
		t.Fatalf("both %+v", both)
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
	okRes, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         "hi",
		TxID:         "\uFFFD",
	})
	if err != nil {
		t.Fatal(err)
	}
	if okRes.Outcome != bus.OutcomeOK || calls != 1 || !bytes.Contains(got, []byte(`"txid":"`+"\uFFFD"+`"`)) {
		t.Fatalf("outcome %s calls %d body %s", okRes.Outcome, calls, got)
	}
	if _, err := Do(context.Background(), addr, "publish", "", PublishRequest{
		Conversation: "job",
		From:         "alice",
		Body:         "hi",
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !bytes.Contains(got, []byte(`"txid":""`)) {
		t.Fatalf("empty key calls %d body %s", calls, got)
	}
}

func TestReadOmitsANilName(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"ok","messages":[]}`)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	if _, err := Do(context.Background(), addr, "read", "", ReadRequest{Conversation: "job", Cursor: 0, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`"name"`)) {
		t.Fatalf("name was sent: %s", got)
	}
	empty := ""
	if _, err := Do(context.Background(), addr, "read", "", ReadRequest{Conversation: "job", Cursor: 0, Limit: 2, Name: &empty}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"name":""`)) {
		t.Fatalf("empty name was not sent: %s", got)
	}
}

func TestTokenHeaderIsRetriedWithTheSameValue(t *testing.T) {
	const token = "hstk-7f3c9a1e4b26"
	var mu sync.Mutex
	var auths []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, append([]byte(nil), body...))
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if n == 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"outcome":"ok","conversations":[]}`)
	}))
	defer srv.Close()

	res, err := Do(context.Background(), srv.Listener.Addr().String(), "list", token, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != bus.OutcomeOK {
		t.Fatalf("result %+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Fatal("result contains the token")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 || auths[0] != "Bearer "+token || auths[1] != auths[0] {
		t.Fatalf("auths %q", auths)
	}
	for _, body := range bodies {
		if bytes.Contains(body, []byte(token)) {
			t.Fatal("body contains the token")
		}
	}
}

func TestUnusableTokenDoesNotDial(t *testing.T) {
	const secret = "sekret-zzzz extra"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"outcome":"ok","conversations":[]}`)
	}))
	defer srv.Close()
	_, err := Do(context.Background(), srv.Listener.Addr().String(), "list", secret, struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "sekret-zzzz") || strings.Contains(err.Error(), "extra") {
		t.Fatal("error contains the token")
	}
	if calls != 0 {
		t.Fatalf("calls = %d", calls)
	}
}

func swapClient(c *http.Client) func() {
	prev := httpClient
	httpClient = c
	return func() { httpClient = prev }
}

func strPtr(s string) *string { return &s }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
