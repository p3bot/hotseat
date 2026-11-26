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
	"net/http"
	"time"

	"github.com/p3bot/hotseat/internal/store"
)

// Record selectors for /record.
const (
	recordSet      = "set"
	recordGrouping = "grouping"
	recordSettle   = "settle"
	recordFix      = "fix"
	recordHalt     = "halt"
	recordStop     = "stop"
)

type recordRequest struct {
	Chat         *string         `json:"chat"`
	Record       *string         `json:"record"`
	Turn         json.RawMessage `json:"turn"`
	Findings     json.RawMessage `json:"findings"`
	Drops        json.RawMessage `json:"drops"`
	Finding      json.RawMessage `json:"finding"`
	Proposal     json.RawMessage `json:"proposal"`
	ProposalKind *string         `json:"proposal_kind"`
	Fixed        json.RawMessage `json:"fixed"`
	Done         json.RawMessage `json:"done"`
	Holder       *string         `json:"holder"`
}

func (s *server) handleRecord(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req recordRequest
	if !s.decode(w, r, "record", "", start, &req) {
		return
	}
	res, err := s.svc.Record(r.Context(), req)
	s.finish(w, "record", deref(req.Chat), start, res, err)
}

func (s *server) handleRecords(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req struct {
		Chat *string `json:"chat"`
	}
	if !s.decode(w, r, "records", "", start, &req) {
		return
	}
	chat := deref(req.Chat)
	res, err := s.svc.Records(r.Context(), chat)
	s.finish(w, "records", chat, start, res, err)
}

// requireSeq reads a required seq field. Absent and null are missing.
func requireSeq(raw json.RawMessage, missing, bad string) (int64, error) {
	n, has, err := parsePositive(raw, bad)
	if err != nil {
		return 0, err
	}
	if !has {
		return 0, rule(missing)
	}
	return n, nil
}

func parseFindings(raw json.RawMessage) ([]int64, error) {
	items, err := rawList(raw, ReasonFindingsRequired, ReasonFindingsShape)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(items))
	seen := make(map[int64]bool, len(items))
	for _, item := range items {
		n, has, err := parsePositive(item, ReasonFindingsShape)
		if err != nil {
			return nil, err
		}
		if !has {
			return nil, rule(ReasonFindingsShape)
		}
		if seen[n] {
			return nil, rule(ReasonFindingsRepeat)
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

func parseDrops(raw json.RawMessage) ([]store.Drop, error) {
	items, err := rawList(raw, ReasonDropsRequired, ReasonDropsShape)
	if err != nil {
		return nil, err
	}
	out := make([]store.Drop, 0, len(items))
	seen := make(map[int64]bool, len(items))
	for _, item := range items {
		var pair struct {
			Finding json.RawMessage `json:"finding"`
			Kept    json.RawMessage `json:"kept"`
		}
		if bytes.Equal(bytes.TrimSpace(item), []byte("null")) || json.Unmarshal(item, &pair) != nil {
			return nil, rule(ReasonDropsShape)
		}
		finding, err := requireSeq(pair.Finding, ReasonDropsShape, ReasonDropsShape)
		if err != nil {
			return nil, err
		}
		kept, err := requireSeq(pair.Kept, ReasonDropsShape, ReasonDropsShape)
		if err != nil {
			return nil, err
		}
		if seen[finding] {
			return nil, rule(ReasonDropsRepeat)
		}
		seen[finding] = true
		out = append(out, store.Drop{Finding: finding, Kept: kept})
	}
	return out, nil
}

// rawList splits a required JSON array into its elements. An absent field is
// missing. null and any other non-array are shape.
func rawList(raw json.RawMessage, missing, shape string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, rule(missing)
	}
	if trimmed[0] != '[' {
		return nil, rule(shape)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, rule(shape)
	}
	return items, nil
}

// Record checks a /record body and writes the one decision record it selects:
// the chat, the record selector, that record's fields, then the
// holder, which must hold the chat lease. Fields of other records
// are ignored. A replay of a stored record is fenced like any other write.
// Repeated findings and repeated dropped findings are refused here, so the
// store compares sets that hold no repeat.
// It changes no rev and wakes no wait or watch.
func (s *Service) Record(ctx context.Context, req recordRequest) (Result, error) {
	chat := deref(req.Chat)
	if err := requireChat(chat); err != nil {
		return asRefused(err)
	}
	if req.Record == nil {
		return refused(ReasonRecordRequired), nil
	}
	switch *req.Record {
	case recordSet:
		turn, err := requireSeq(req.Turn, ReasonTurnRequired, ReasonTurnRange)
		if err != nil {
			return asRefused(err)
		}
		findings, err := parseFindings(req.Findings)
		if err != nil {
			return asRefused(err)
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var set store.Set
			set, res.Grouping, res.AlreadyStored, err = s.store.WriteSet(ctx, chat, turn, findings)
			res.Set = &set
			return err
		})
	case recordGrouping:
		turn, err := requireSeq(req.Turn, ReasonTurnRequired, ReasonTurnRange)
		if err != nil {
			return asRefused(err)
		}
		drops, err := parseDrops(req.Drops)
		if err != nil {
			return asRefused(err)
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var g store.Grouping
			g, res.AlreadyStored, err = s.store.WriteGrouping(ctx, chat, turn, drops)
			res.Grouping = &g
			return err
		})
	case recordSettle:
		finding, err := requireSeq(req.Finding, ReasonFindingRequired, ReasonFindingRange)
		if err != nil {
			return asRefused(err)
		}
		proposal, err := requireSeq(req.Proposal, ReasonProposalRequired, ReasonProposalRange)
		if err != nil {
			return asRefused(err)
		}
		if req.ProposalKind == nil {
			return refused(ReasonProposalKindRequired), nil
		}
		kind := *req.ProposalKind
		if !validName(kind) {
			return refused(ReasonProposalKindBad), nil
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var settle store.Settle
			settle, res.AlreadyStored, err = s.store.WriteSettle(ctx, chat, finding, proposal, kind)
			res.Settle = &settle
			return err
		})
	case recordFix:
		finding, err := requireSeq(req.Finding, ReasonFindingRequired, ReasonFindingRange)
		if err != nil {
			return asRefused(err)
		}
		fixed, err := requireSeq(req.Fixed, ReasonFixedRequired, ReasonFixedRange)
		if err != nil {
			return asRefused(err)
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var fix store.Fix
			fix, res.AlreadyStored, err = s.store.WriteFix(ctx, chat, finding, fixed)
			res.Fix = &fix
			return err
		})
	case recordHalt:
		finding, err := requireSeq(req.Finding, ReasonFindingRequired, ReasonFindingRange)
		if err != nil {
			return asRefused(err)
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var halt store.Halt
			halt, res.AlreadyStored, err = s.store.WriteHalt(ctx, chat, finding)
			res.Halt = &halt
			return err
		})
	case recordStop:
		done, err := requireSeq(req.Done, ReasonDoneRequired, ReasonDoneRange)
		if err != nil {
			return asRefused(err)
		}
		return s.writeRecord(ctx, chat, req.Holder, func(res *Result) (err error) {
			var stop store.Stop
			stop, res.AlreadyStored, err = s.store.WriteStop(ctx, chat, done)
			res.Stop = &stop
			return err
		})
	default:
		return refused(ReasonRecordBad), nil
	}
}

// writeRecord runs one validated store write under the service lock and the chat lease, and maps its error.
func (s *Service) writeRecord(ctx context.Context, chat string, holder *string, write func(*Result) error) (Result, error) {
	by, err := requireHolder(holder)
	if err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, ErrDropped
	}
	if err := s.fence(ctx, chat, by); err != nil {
		return s.fail(err, "record")
	}
	res := Result{Outcome: OutcomeOK}
	if err := write(&res); err != nil {
		return s.fail(err, "record")
	}
	return res, nil
}

// Records returns every decision record of one chat as six lists.
func (s *Service) Records(ctx context.Context, chat string) (Result, error) {
	if err := requireChat(chat); err != nil {
		return asRefused(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.store.Records(ctx, chat)
	if err != nil {
		return s.fail(err, "records")
	}
	return Result{Outcome: OutcomeOK, Records: &recs}, nil
}

type wireSet struct {
	Turn     int64   `json:"turn"`
	Findings []int64 `json:"findings"`
	After    int64   `json:"after"`
}

type wireDrop struct {
	Finding int64 `json:"finding"`
	Kept    int64 `json:"kept"`
}

type wireGrouping struct {
	Turn  int64      `json:"turn"`
	Drops []wireDrop `json:"drops"`
	After int64      `json:"after"`
}

type wireSettle struct {
	Finding      int64  `json:"finding"`
	Proposal     int64  `json:"proposal"`
	ProposalKind string `json:"proposal_kind"`
	After        int64  `json:"after"`
}

type wireFix struct {
	Finding      int64 `json:"finding"`
	SettledAfter int64 `json:"settled_after"`
	Fixed        int64 `json:"fixed"`
}

type wireHalt struct {
	Finding int64 `json:"finding"`
	After   int64 `json:"after"`
}

type wireStop struct {
	Done int64 `json:"done"`
}

func toWireSet(s store.Set) wireSet {
	findings := s.Findings
	if findings == nil {
		findings = []int64{}
	}
	return wireSet{Turn: s.Turn, Findings: findings, After: s.After}
}

func toWireGrouping(g store.Grouping) wireGrouping {
	drops := make([]wireDrop, 0, len(g.Drops))
	for _, d := range g.Drops {
		drops = append(drops, wireDrop{Finding: d.Finding, Kept: d.Kept})
	}
	return wireGrouping{Turn: g.Turn, Drops: drops, After: g.After}
}

func toWireSettle(s store.Settle) wireSettle {
	return wireSettle{Finding: s.Finding, Proposal: s.Proposal, ProposalKind: s.ProposalKind, After: s.After}
}

func toWireFix(f store.Fix) wireFix {
	return wireFix{Finding: f.Finding, SettledAfter: f.SettledAfter, Fixed: f.Fixed}
}

func toWireHalt(h store.Halt) wireHalt {
	return wireHalt{Finding: h.Finding, After: h.After}
}

func toWireStop(s store.Stop) wireStop {
	return wireStop{Done: s.Done}
}

// recordWire fills the record fields of a /record or /records result.
func recordWire(op string, res Result, out *wire) {
	switch op {
	case "record":
		already := res.AlreadyStored
		out.AlreadyStored = &already
		out.Set = wirePtr(res.Set, toWireSet)
		out.Grouping = wirePtr(res.Grouping, toWireGrouping)
		out.Settle = wirePtr(res.Settle, toWireSettle)
		out.Fix = wirePtr(res.Fix, toWireFix)
		out.Halt = wirePtr(res.Halt, toWireHalt)
		out.Stop = wirePtr(res.Stop, toWireStop)
	case "records":
		var recs store.Records
		if res.Records != nil {
			recs = *res.Records
		}
		out.Sets = wireList(recs.Sets, toWireSet)
		out.Groupings = wireList(recs.Groupings, toWireGrouping)
		out.Settles = wireList(recs.Settles, toWireSettle)
		out.Fixes = wireList(recs.Fixes, toWireFix)
		out.Halts = wireList(recs.Halts, toWireHalt)
		out.Stops = wireList(recs.Stops, toWireStop)
	}
}
