// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package client performs one call against a running hotseat bus.
// It does not open the store. It does not keep the cursor, the transaction
// id, or the capability token after the call returns.
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

// Result is one completed call, decoded from the bus JSON.
// Outcome is ok, timeout, refused, unavailable, or connection_failure.
// Held, and Since and Expires when held, answer a lease get. TTL answers an
// ok acquire or renew, and Remaining an acquire refused for another holder,
// both in whole seconds.
// More is set on every ok read and watch, true when a further matching
// message follows the page.
type Result struct {
	Outcome        string      `json:"outcome"`
	Reason         string      `json:"reason,omitempty"`
	AlreadyStored  *bool       `json:"already_stored,omitempty"`
	AlreadyExisted *bool       `json:"already_existed,omitempty"`
	MatchSeq       *int64      `json:"match_seq,omitempty"`
	Message        *Message    `json:"message,omitempty"`
	Messages       *[]Message  `json:"messages,omitempty"`
	Chat           *Chat       `json:"chat,omitempty"`
	Chats          *[]Chat     `json:"chats,omitempty"`
	Member         *Member     `json:"member,omitempty"`
	Members        *[]Member   `json:"members,omitempty"`
	Session        *Session    `json:"session,omitempty"`
	Sessions       *[]Session  `json:"sessions,omitempty"`
	More           *bool       `json:"more,omitempty"`
	Rev            *uint64     `json:"rev,omitempty"`
	Set            *Set        `json:"set,omitempty"`
	Grouping       *Grouping   `json:"grouping,omitempty"`
	Settle         *Settle     `json:"settle,omitempty"`
	Fix            *Fix        `json:"fix,omitempty"`
	Halt           *Halt       `json:"halt,omitempty"`
	Stop           *Stop       `json:"stop,omitempty"`
	Sets           *[]Set      `json:"sets,omitempty"`
	Groupings      *[]Grouping `json:"groupings,omitempty"`
	Settles        *[]Settle   `json:"settles,omitempty"`
	Fixes          *[]Fix      `json:"fixes,omitempty"`
	Halts          *[]Halt     `json:"halts,omitempty"`
	Stops          *[]Stop     `json:"stops,omitempty"`
	Held           *bool       `json:"held,omitempty"`
	Since          string      `json:"since,omitempty"`
	Expires        string      `json:"expires,omitempty"`
	TTL            *int64      `json:"ttl,omitempty"`
	Remaining      *int64      `json:"remaining,omitempty"`
}

// Message is one transcript entry as the bus returned it.
// Chat is set by the messages and watch operations and omitted on the others.
// Re is the seq this message responds to, and nil when the bus sent null.
type Message struct {
	Seq  int64    `json:"seq"`
	Time string   `json:"time"`
	From string   `json:"from"`
	To   []string `json:"to"`
	Body string   `json:"body"`
	TxID string   `json:"txid"`
	Kind string   `json:"kind"`
	Re   *int64   `json:"re"`
	Chat string   `json:"chat,omitempty"`
}

// Chat is a name, its status, and its binding.
// JSON from the bus always includes the binding keys.
type Chat struct {
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	Task       string        `json:"task"`
	Ticket     string        `json:"ticket"`
	Seat       string        `json:"seat"`
	Directory  string        `json:"directory"`
	Prompts    []string      `json:"prompts"`
	Custom     string        `json:"custom"`
	Roster     []RosterEntry `json:"roster"`
	Permission string        `json:"permission"`
}

// RosterEntry is one roster element. The bus reads Name only and stores Module and Model as given.
type RosterEntry struct {
	Name   string `json:"name"`
	Module string `json:"module"`
	Model  string `json:"model"`
}

// String is name=module:model, or name alone when module and model are both empty.
func (r RosterEntry) String() string {
	if r.Module == "" && r.Model == "" {
		return r.Name
	}
	return r.Name + "=" + r.Module + ":" + r.Model
}

// RosterNames is the names of roster, in order.
func RosterNames(roster []RosterEntry) []string {
	names := make([]string, len(roster))
	for i, r := range roster {
		names[i] = r.Name
	}
	return names
}

// Member is one roster row as a member route returned it.
// Session is not a field. PID and Start come only from a one-row member read or write,
// and each is absent until a call sets it.
type Member struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Launched string `json:"launched"`
	Joined   string `json:"joined"`
	PID      *int64 `json:"pid,omitempty"`
	Start    string `json:"start,omitempty"`
}

// Session is a member name and the session id stored for it.
type Session struct {
	Name    string `json:"name"`
	Session string `json:"session"`
}

// Set is a closed turn and its findings in seq order, as the record routes return it.
type Set struct {
	Turn     int64   `json:"turn"`
	Findings []int64 `json:"findings"`
	After    int64   `json:"after"`
}

// Drop is a finding of a set and the finding of the same set that kept its issue.
type Drop struct {
	Finding int64 `json:"finding"`
	Kept    int64 `json:"kept"`
}

// Grouping is a set's drops in finding order, as the record routes return it.
type Grouping struct {
	Turn  int64  `json:"turn"`
	Drops []Drop `json:"drops"`
	After int64  `json:"after"`
}

// Settle is the agreed proposal for a finding. ProposalKind is finding, propose, or dismiss.
type Settle struct {
	Finding      int64  `json:"finding"`
	Proposal     int64  `json:"proposal"`
	ProposalKind string `json:"proposal_kind"`
	After        int64  `json:"after"`
}

// Fix is the fixed message for a settled finding. SettledAfter is that settle's After.
type Fix struct {
	Finding      int64 `json:"finding"`
	SettledAfter int64 `json:"settled_after"`
	Fixed        int64 `json:"fixed"`
}

// Halt is the finding a chat's run stopped on.
type Halt struct {
	Finding int64 `json:"finding"`
	After   int64 `json:"after"`
}

// Stop is the done that stopped a chat's run.
type Stop struct {
	Done int64 `json:"done"`
}

// RecordWrite is one decision record: SetRecord, GroupingRecord,
// SettleRecord, FixRecord, HaltRecord, or StopRecord. Each sends its own
// record selector and only its own fields. The bus assigns after.
type RecordWrite interface {
	recordBody(holder string) any
}

// RecordRequest is the record body: one RecordWrite and the holder of the
// chat lease, which the bus requires on every record write.
type RecordRequest struct {
	Holder string
	Record RecordWrite
}

// SetRecord writes a closed turn and its findings. A nil Findings is sent as an empty array.
type SetRecord struct {
	Chat     string
	Turn     int64
	Findings []int64
}

func (r SetRecord) recordBody(holder string) any {
	findings := r.Findings
	if findings == nil {
		findings = []int64{}
	}
	return struct {
		Chat     string  `json:"chat"`
		Record   string  `json:"record"`
		Turn     int64   `json:"turn"`
		Findings []int64 `json:"findings"`
		Holder   string  `json:"holder,omitempty"`
	}{r.Chat, "set", r.Turn, findings, holder}
}

// GroupingRecord writes a set's drops. A nil Drops is sent as an empty array.
type GroupingRecord struct {
	Chat  string
	Turn  int64
	Drops []Drop
}

func (r GroupingRecord) recordBody(holder string) any {
	drops := r.Drops
	if drops == nil {
		drops = []Drop{}
	}
	return struct {
		Chat   string `json:"chat"`
		Record string `json:"record"`
		Turn   int64  `json:"turn"`
		Drops  []Drop `json:"drops"`
		Holder string `json:"holder,omitempty"`
	}{r.Chat, "grouping", r.Turn, drops, holder}
}

// SettleRecord writes the agreed proposal for a finding.
// ProposalKind is the proposal message's kind: finding, propose, or dismiss.
type SettleRecord struct {
	Chat         string
	Finding      int64
	Proposal     int64
	ProposalKind string
}

func (r SettleRecord) recordBody(holder string) any {
	return struct {
		Chat         string `json:"chat"`
		Record       string `json:"record"`
		Finding      int64  `json:"finding"`
		Proposal     int64  `json:"proposal"`
		ProposalKind string `json:"proposal_kind"`
		Holder       string `json:"holder,omitempty"`
	}{r.Chat, "settle", r.Finding, r.Proposal, r.ProposalKind, holder}
}

// FixRecord writes the fixed message for a settled finding.
type FixRecord struct {
	Chat    string
	Finding int64
	Fixed   int64
}

func (r FixRecord) recordBody(holder string) any {
	return struct {
		Chat    string `json:"chat"`
		Record  string `json:"record"`
		Finding int64  `json:"finding"`
		Fixed   int64  `json:"fixed"`
		Holder  string `json:"holder,omitempty"`
	}{r.Chat, "fix", r.Finding, r.Fixed, holder}
}

// HaltRecord writes the finding a chat's run stopped on.
type HaltRecord struct {
	Chat    string
	Finding int64
}

func (r HaltRecord) recordBody(holder string) any {
	return struct {
		Chat    string `json:"chat"`
		Record  string `json:"record"`
		Finding int64  `json:"finding"`
		Holder  string `json:"holder,omitempty"`
	}{r.Chat, "halt", r.Finding, holder}
}

// StopRecord writes the done that stopped a chat's run.
type StopRecord struct {
	Chat string
	Done int64
}

func (r StopRecord) recordBody(holder string) any {
	return struct {
		Chat   string `json:"chat"`
		Record string `json:"record"`
		Done   int64  `json:"done"`
		Holder string `json:"holder,omitempty"`
	}{r.Chat, "stop", r.Done, holder}
}

// RecordsRequest is the records body.
type RecordsRequest struct {
	Chat string `json:"chat"`
}

// CreateRequest is the create body.
// Omitted binding fields are stored empty. Prompts and Roster keep their order.
// Permission is the one member launches take.
type CreateRequest struct {
	Name       string        `json:"name"`
	Task       string        `json:"task,omitempty"`
	Ticket     string        `json:"ticket,omitempty"`
	Seat       string        `json:"seat,omitempty"`
	Directory  string        `json:"directory,omitempty"`
	Prompts    []string      `json:"prompts,omitempty"`
	Custom     string        `json:"custom,omitempty"`
	Roster     []RosterEntry `json:"roster,omitempty"`
	Permission string        `json:"permission,omitempty"`
}

// SendRequest is the send body. To keeps the caller's order.
// A nil To is sent as an empty array. Kind is required by the bus.
// A nil Re omits the field. Any other value is sent, and the bus refuses one below 1.
// An empty Holder omits the field and the send is not fenced. Any other
// holder must hold the chat lease.
type SendRequest struct {
	Chat   string   `json:"chat"`
	From   string   `json:"from"`
	To     []string `json:"to"`
	Body   string   `json:"body"`
	TxID   string   `json:"txid"`
	Kind   string   `json:"kind"`
	Re     *int64   `json:"re,omitempty"`
	Holder string   `json:"holder,omitempty"`
}

// ReadRequest is the read body. A nil Name omits the field.
// A pointer to an empty string sends that empty name.
// A nil Kinds omits the field. A pointer to an empty slice sends an empty array.
type ReadRequest struct {
	Chat   string    `json:"chat"`
	Cursor int64     `json:"cursor"`
	Limit  int64     `json:"limit"`
	Name   *string   `json:"name,omitempty"`
	Kinds  *[]string `json:"kinds,omitempty"`
}

// WaitRequest is the wait body. A nil Name, Deadline, or Kinds omits that field.
type WaitRequest struct {
	Chat     string    `json:"chat"`
	Cursor   int64     `json:"cursor"`
	Name     *string   `json:"name,omitempty"`
	Deadline *string   `json:"deadline,omitempty"`
	Kinds    *[]string `json:"kinds,omitempty"`
}

// WatchRequest is the watch body. An empty Chat watches every
// chat. A nil Cursor watches rev only. A nil Rev, Limit, or
// Deadline omits that field.
type WatchRequest struct {
	Chat     string  `json:"chat,omitempty"`
	Cursor   *int64  `json:"cursor,omitempty"`
	Rev      *uint64 `json:"rev,omitempty"`
	Limit    *int64  `json:"limit,omitempty"`
	Deadline *string `json:"deadline,omitempty"`
}

// MessagesRequest is the messages body. A nil Before omits the field.
type MessagesRequest struct {
	Limit  int64  `json:"limit"`
	Before *int64 `json:"before,omitempty"`
}

// JoinRequest is the join body.
type JoinRequest struct {
	Chat string `json:"chat"`
	Name string `json:"name"`
}

// MemberRequest is the member body.
// A nil Status and a nil PID reads the row. A PID without a Status, or a Start without a PID, is refused.
// Start is the host's start token for PID. An empty Start with a PID clears the stored one.
// A write with a Status requires Holder to hold the chat lease.
type MemberRequest struct {
	Chat   string  `json:"chat"`
	Name   string  `json:"name"`
	Status *string `json:"status,omitempty"`
	PID    *int64  `json:"pid,omitempty"`
	Start  string  `json:"start,omitempty"`
	Holder string  `json:"holder,omitempty"`
}

// SessionRequest is the session body.
// A nil Name lists every member. A nil Session reads one id.
// A write with a Session requires Holder to hold the chat lease.
type SessionRequest struct {
	Chat    string  `json:"chat"`
	Name    *string `json:"name,omitempty"`
	Session *string `json:"session,omitempty"`
	Holder  string  `json:"holder,omitempty"`
}

// LeaseRequest is the lease body. Lease is bus.LeaseAcquire, bus.LeaseRenew,
// bus.LeaseRelease, or bus.LeaseGet. An empty Holder omits the field, which
// only get allows.
type LeaseRequest struct {
	Chat   string `json:"chat"`
	Lease  string `json:"lease"`
	Holder string `json:"holder,omitempty"`
}

// MembersRequest is the members body.
type MembersRequest struct {
	Chat string `json:"chat"`
}

// CloseRequest is the close body. Close sends no message.
type CloseRequest struct {
	Chat string `json:"chat"`
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
// token is the capability secret. Empty omits the header. A token that
// cannot be a header is an error and is not posted.
// A dropped call is retried once with the same body and the same token.
// A dropped launch whose retry is refused as already launched returns the stored row.
// A failure to connect is not retried. Neither result is a timeout.
// A send body or transaction id that is not valid UTF-8 is refused and not posted.
func Do(ctx context.Context, address, op, token string, body any) (Result, error) {
	if token != "" {
		if err := bus.WriteToken(make(http.Header), token); err != nil {
			return Result{}, err
		}
	}
	if op == "send" {
		// JSON replaces invalid UTF-8, so the bus would store a different message or a different transaction id.
		if req, ok := body.(SendRequest); ok {
			switch {
			case !utf8.ValidString(req.Body):
				return Result{Outcome: bus.OutcomeRefused, Reason: bus.ReasonBodyUTF8}, nil
			case !utf8.ValidString(req.TxID):
				return Result{Outcome: bus.OutcomeRefused, Reason: bus.ReasonKeyUTF8}, nil
			case !utf8.ValidString(req.Kind):
				return Result{Outcome: bus.OutcomeRefused, Reason: bus.ReasonKindBad}, nil
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
	res, err := post(ctx, url, token, raw)
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if !isDrop(err) {
		return failureFrom(address, err), nil
	}
	res, err = post(ctx, url, token, raw)
	if err == nil {
		return committedLaunch(ctx, address, token, body, res)
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if isBad(err) {
		return failureFrom(address, err), nil
	}
	return failure(reasonDropped + ": " + address + ": " + causeText(err)), nil
}

// committedLaunch returns the stored row when a retried launch is refused as already launched.
// A deliberate second launch completes on its first post and does not reach this.
func committedLaunch(ctx context.Context, address, token string, body any, res Result) (Result, error) {
	req, ok := body.(MemberRequest)
	if !ok || req.Status == nil || *req.Status != "launched" {
		return res, nil
	}
	if res.Outcome != bus.OutcomeRefused || res.Reason != bus.ReasonAlreadyLaunched {
		return res, nil
	}
	req.Status = nil
	req.PID = nil
	req.Start = ""
	req.Holder = ""
	return Do(ctx, address, "member", token, req)
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
	if op == "send" {
		req, ok := body.(SendRequest)
		if !ok {
			return nil, errors.New("send body has the wrong type")
		}
		if req.To == nil {
			req.To = []string{}
		}
		body = req
	}
	if op == "record" {
		req, ok := body.(RecordRequest)
		if !ok || req.Record == nil {
			return nil, errors.New("record body has the wrong type")
		}
		body = req.Record.recordBody(req.Holder)
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
	case "send":
		return bus.PathSend, nil
	case "read":
		return bus.PathRead, nil
	case "wait":
		return bus.PathWait, nil
	case "watch":
		return bus.PathWatch, nil
	case "close":
		return bus.PathClose, nil
	case "list":
		return bus.PathList, nil
	case "messages":
		return bus.PathMessages, nil
	case "join":
		return bus.PathJoin, nil
	case "member":
		return bus.PathMember, nil
	case "session":
		return bus.PathSession, nil
	case "members":
		return bus.PathMembers, nil
	case "record":
		return bus.PathRecord, nil
	case "records":
		return bus.PathRecords, nil
	case "lease":
		return bus.PathLease, nil
	default:
		return "", fmt.Errorf("unknown operation %s", op)
	}
}

func post(ctx context.Context, url, token string, body []byte) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	// The transport replays a request that sets GetBody. This client owns
	// the single retry, so the transport must not send the body again.
	req.GetBody = nil
	if err := bus.WriteToken(req.Header, token); err != nil {
		return Result{}, err
	}
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
	case bus.OutcomeOK, bus.OutcomeTimeout, bus.OutcomeRefused, bus.OutcomeUnavailable:
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
