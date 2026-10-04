// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package client performs one call against a running hotseat bus.
// It does not open the store. It does not keep the cursor or the idempotency
// key after the call returns.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/p3bot/hotseat/internal/bus"
)

const (
	// OutcomeConnectionFailure is a call that never completed.
	// It is not a bus outcome. Timeout is only bus.OutcomeTimeout.
	OutcomeConnectionFailure = "connection_failure"
)

const (
	reasonConnect = "connection failed"
	reasonDropped = "connection dropped"
)

// callFail is a transport error. kind is reasonConnect or reasonDropped.
// A full HTTP response is notBus instead, and is not retried.
type callFail struct {
	kind  string
	cause error
}

func (e *callFail) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return e.kind
	}
	return e.kind + ": " + e.cause.Error()
}

func (e *callFail) Unwrap() error { return e.cause }

type notBus struct {
	status  int
	outcome string
}

func (e *notBus) Error() string {
	switch {
	case e.status != 0 && e.status != http.StatusOK:
		return fmt.Sprintf("http %d", e.status)
	case e.outcome != "":
		return fmt.Sprintf("unexpected outcome %q", e.outcome)
	default:
		return "response was not a bus result"
	}
}

// Result is the JSON object every client command prints.
// Outcome is ok, timeout, closed, refused, unavailable, or connection_failure.
type Result struct {
	Outcome       string          `json:"outcome"`
	Reason        string          `json:"reason,omitempty"`
	AlreadyStored *bool           `json:"already_stored,omitempty"`
	MatchSeq      *int64          `json:"match_seq,omitempty"`
	Message       *Message        `json:"message,omitempty"`
	Messages      *[]Message      `json:"messages,omitempty"`
	Conversation  *Conversation   `json:"conversation,omitempty"`
	Conversations *[]Conversation `json:"conversations,omitempty"`
}

// Message is one transcript entry as the bus returned it.
type Message struct {
	Seq  int64    `json:"seq"`
	Time string   `json:"time"`
	From string   `json:"from"`
	To   []string `json:"to"`
	Body string   `json:"body"`
	Key  string   `json:"idempotency_key"`
}

// Conversation is a name and its open or closed status.
type Conversation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	Name string `json:"name"`
}

// PublishRequest is the publish body. To keeps the caller's order.
// A nil To is sent as an empty array.
type PublishRequest struct {
	Conversation   string   `json:"conversation"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	Body           string   `json:"body"`
	IdempotencyKey string   `json:"idempotency_key"`
}

// ReadRequest is the read body. A nil Name omits the field.
// A pointer to an empty string sends that empty name.
type ReadRequest struct {
	Conversation string  `json:"conversation"`
	Cursor       int64   `json:"cursor"`
	Limit        int64   `json:"limit"`
	Name         *string `json:"name,omitempty"`
}

// WaitRequest is the wait body. A nil Name or Deadline omits that field.
type WaitRequest struct {
	Conversation string  `json:"conversation"`
	Cursor       int64   `json:"cursor"`
	Name         *string `json:"name,omitempty"`
	Deadline     *string `json:"deadline,omitempty"`
}

// CloseRequest is the close body. Close sends no message.
type CloseRequest struct {
	Conversation string `json:"conversation"`
}

// httpClient has no request timeout. A wait blocks until the bus replies.
// The dial timeout is only how long we wait to establish a connection.
// Keep-alives are off so the one retry cannot reuse the connection that dropped.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Do posts one operation and returns the bus result.
// A dropped call is retried once with the same body. A failure to connect
// is not retried. Neither result is a timeout.
// A publish body or idempotency key that is not valid UTF-8 is refused and not posted.
func Do(ctx context.Context, address, op string, body any) (Result, error) {
	if op == "publish" {
		// JSON replaces invalid UTF-8, so the bus would store a different message or a different key.
		if req, ok := body.(PublishRequest); ok {
			switch {
			case !utf8.ValidString(req.Body):
				return Result{Outcome: bus.OutcomeRefused, Reason: bus.ReasonBodyUTF8}, nil
			case !utf8.ValidString(req.IdempotencyKey):
				return Result{Outcome: bus.OutcomeRefused, Reason: bus.ReasonKeyUTF8}, nil
			}
		}
	}
	raw, err := marshalBody(op, body)
	if err != nil {
		return Result{}, err
	}
	url, err := operationURL(address, op)
	if err != nil {
		return Result{}, err
	}
	res, err := post(ctx, url, raw)
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if !isDrop(err) {
		return failureFrom(address, err), nil
	}
	res, err = post(ctx, url, raw)
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if isBad(err) {
		return failureFrom(address, err), nil
	}
	return failure(reasonDropped + ": " + address + ": " + causeText(err)), nil
}

func isDrop(err error) bool {
	var fail *callFail
	return errors.As(err, &fail) && fail.kind == reasonDropped
}

func isBad(err error) bool {
	var bad *notBus
	return errors.As(err, &bad)
}

func failureFrom(address string, err error) Result {
	var bad *notBus
	if errors.As(err, &bad) {
		return failure(bad.Error() + " from " + address)
	}
	var fail *callFail
	if !errors.As(err, &fail) {
		return failure(reasonConnect + ": " + address + ": " + err.Error())
	}
	return failure(reasonConnect + ": " + address + ": " + causeText(err))
}

func causeText(err error) string {
	var fail *callFail
	if errors.As(err, &fail) && fail.cause != nil {
		return fail.cause.Error()
	}
	return err.Error()
}

func failure(reason string) Result {
	return Result{Outcome: OutcomeConnectionFailure, Reason: reason}
}

func marshalBody(op string, body any) ([]byte, error) {
	if op == "publish" {
		req, ok := body.(PublishRequest)
		if !ok {
			return nil, errors.New("publish body has the wrong type")
		}
		if req.To == nil {
			req.To = []string{}
		}
		body = req
	}
	if body == nil {
		body = struct{}{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func operationURL(address, op string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("address: %w", err)
	}
	path, err := operationPath(op)
	if err != nil {
		return "", err
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}

func operationPath(op string) (string, error) {
	switch op {
	case "create":
		return bus.PathCreate, nil
	case "publish":
		return bus.PathPublish, nil
	case "read":
		return bus.PathRead, nil
	case "wait":
		return bus.PathWait, nil
	case "close":
		return bus.PathClose, nil
	case "list":
		return bus.PathList, nil
	default:
		return "", fmt.Errorf("unknown operation %s", op)
	}
}

func post(ctx context.Context, url string, body []byte) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	// The transport replays a request that sets GetBody. This client owns
	// the single retry, so the transport must not send the body again.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))
	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, classify(ctx, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return Result{}, &notBus{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, classify(ctx, err)
	}
	var res Result
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&res); err != nil || res.Outcome == "" {
		return Result{}, &notBus{}
	}
	switch res.Outcome {
	case bus.OutcomeOK, bus.OutcomeTimeout, bus.OutcomeClosed, bus.OutcomeRefused, bus.OutcomeUnavailable:
		return res, nil
	default:
		return Result{}, &notBus{outcome: res.Outcome}
	}
}

// classify separates the caller's cancellation from a transport failure.
// A dial timeout is a failure to connect. Only the caller's context should
// abandon the invocation.
func classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return &callFail{kind: reasonConnect, cause: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &callFail{kind: reasonDropped, cause: err}
}
