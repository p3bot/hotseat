// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/p3bot/hotseat/internal/client"
)

// ledgerKinds are the kinds a ledger reads. Stances other than propose and dismiss are never replayed.
var ledgerKinds = []string{kindTurn, kindDone, kindFinding, kindPropose, kindDismiss, kindAsk, kindDiscuss, kindSettled, kindHalted, kindFixed, kindError}

// ledger is what a transcript records about sets and findings. It is a pure reading of its messages.
// A set runs from its turn to its discuss marker. A set with no discuss runs until its follow-up
// target's next turn or done, or without end when there is none.
type ledger struct {
	msgs   []client.Message
	roster []string
	// targets maps a turn sender to its follow-up target. A missing sender has no target.
	targets map[string]string

	sets     []client.Message
	discuss  map[int64]client.Message
	ends     map[int64]int64
	owners   map[int64]int64
	found    []client.Message
	settled  map[int64]settle
	fixed    map[int64]int64
	haltedAt int64
	// haltMarker is the seq of the first halted marker.
	haltMarker int64
	halt       bool
}

type settle struct {
	marker   client.Message
	proposal int64
}

func newLedger(msgs []client.Message, roster []string, targets map[string]string) ledger {
	l := ledger{
		msgs:    msgs,
		roster:  roster,
		targets: targets,
		discuss: make(map[int64]client.Message),
		ends:    make(map[int64]int64),
		owners:  make(map[int64]int64),
		settled: make(map[int64]settle),
		fixed:   make(map[int64]int64),
	}
	var latest int64
	for _, m := range msgs {
		switch {
		case m.Kind == kindTurn:
			if _, _, ok := parseMode(m.Body); ok {
				l.sets = append(l.sets, m)
			}
		case l.marker(m, kindDiscuss):
			if turn, err := strconv.ParseInt(m.Body, 10, 64); err == nil {
				if _, dup := l.discuss[turn]; !dup {
					l.discuss[turn] = m
				}
			}
		case l.marker(m, kindSettled):
			latest = m.Seq
			if f, p, ok := parseSettled(m.Body); ok {
				if _, dup := l.settled[f]; !dup {
					l.settled[f] = settle{marker: m, proposal: p}
				}
			}
		case l.marker(m, kindHalted):
			if f, err := strconv.ParseInt(m.Body, 10, 64); err == nil && !l.halt {
				l.haltedAt, l.haltMarker, l.halt = f, m.Seq, true
			}
		case m.Kind == kindFixed && latest != 0:
			// Discussions are serial, so a fixed belongs to the latest settled marker before it.
			if _, dup := l.fixed[latest]; !dup {
				l.fixed[latest] = m.Seq
			}
		}
	}
	for _, turn := range l.sets {
		l.ends[turn.Seq] = l.setEnd(turn)
	}
	for _, m := range msgs {
		if m.Kind != kindFinding || !slices.Contains(roster, m.From) || l.inDiscussions(m.Seq) {
			continue
		}
		for _, turn := range l.sets {
			if turn.Seq >= m.Seq {
				break
			}
			if m.Seq < l.ends[turn.Seq] && slices.Contains(turn.To, m.From) {
				l.owners[m.Seq] = turn.Seq
				l.found = append(l.found, m)
				break
			}
		}
	}
	return l
}

// inDiscussions reports whether seq lies after a set's discuss and before its discussions ended, so a finding there
// opens nothing, even inside a queued set's window. Findings are read in seq order, and a set's own findings precede
// its discuss, so the sets this asks about are already complete.
func (l ledger) inDiscussions(seq int64) bool {
	for turn, d := range l.discuss {
		if seq <= d.Seq {
			continue
		}
		if end, ok := l.discussed(turn); !ok || seq < end {
			return true
		}
	}
	return false
}

// before is the ledger of the messages stored before seq. A stop result counts only those.
func (l ledger) before(seq int64) ledger {
	i := slices.IndexFunc(l.msgs, func(m client.Message) bool { return m.Seq >= seq })
	if i < 0 {
		return l
	}
	return newLedger(l.msgs[:i], l.roster, l.targets)
}

func (l ledger) marker(m client.Message, kind string) bool {
	return m.Kind == kind && m.From == fromRun
}

func (l ledger) discussOf(turn int64) (client.Message, bool) {
	m, ok := l.discuss[turn]
	return m, ok
}

// later is the first turn or done from the set's follow-up target after the turn.
func (l ledger) later(turn client.Message) (client.Message, bool) {
	target := l.targets[turn.From]
	if target == "" {
		return client.Message{}, false
	}
	for _, m := range l.msgs {
		if m.Seq > turn.Seq && m.From == target && (m.Kind == kindTurn || m.Kind == kindDone) {
			return m, true
		}
	}
	return client.Message{}, false
}

func (l ledger) setEnd(turn client.Message) int64 {
	if d, ok := l.discuss[turn.Seq]; ok {
		return d.Seq
	}
	if m, ok := l.later(turn); ok {
		return m.Seq
	}
	return math.MaxInt64
}

// valid reports whether a finding opened a discussion: it lies in the window of a set whose recipients include its sender,
// and not inside another set's discussions. A finding that lies in several windows belongs to the first.
func (l ledger) valid(finding int64) bool {
	_, ok := l.owners[finding]
	return ok
}

// findings are the valid findings, in seq order.
func (l ledger) findings() []client.Message {
	return l.found
}

// findingsOf are the valid findings of the set opened by turn, in seq order.
func (l ledger) findingsOf(turn int64) []client.Message {
	var out []client.Message
	for _, f := range l.found {
		if l.owners[f.Seq] == turn {
			out = append(out, f)
		}
	}
	return out
}

func settledBody(finding, proposal int64) string {
	return strconv.FormatInt(finding, 10) + " " + strconv.FormatInt(proposal, 10)
}

func parseSettled(body string) (finding, proposal int64, ok bool) {
	a, b, found := strings.Cut(body, " ")
	if !found {
		return 0, 0, false
	}
	f, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	p, err := strconv.ParseInt(b, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return f, p, true
}

// settledOf is the settled marker of a finding and the agreed proposal's seq.
func (l ledger) settledOf(finding int64) (client.Message, int64, bool) {
	s, ok := l.settled[finding]
	return s.marker, s.proposal, ok
}

// change reports whether the agreed proposal asks for a fix. Only a dismiss does not.
func (l ledger) change(proposal int64) bool {
	for _, m := range l.msgs {
		if m.Seq == proposal {
			return m.Kind != kindDismiss
		}
	}
	return true
}

// needsFix reports whether a finding settled on a change and no fixed belongs to that settle.
func (l ledger) needsFix(finding int64) bool {
	s, ok := l.settled[finding]
	if !ok || !l.change(s.proposal) {
		return false
	}
	_, fixed := l.fixed[s.marker.Seq]
	return !fixed
}

// halted is the finding of the first halted marker.
func (l ledger) halted() (int64, bool) {
	return l.haltedAt, l.halt
}

// unresolved lists each valid finding with no settled marker, and each settled change with no fixed.
func (l ledger) unresolved() (unsettled, unfixed []int64) {
	for _, f := range l.found {
		if _, ok := l.settled[f.Seq]; !ok {
			unsettled = append(unsettled, f.Seq)
			continue
		}
		if l.needsFix(f.Seq) {
			unfixed = append(unfixed, f.Seq)
		}
	}
	return unsettled, unfixed
}

// discussionStart is where the discussion of a finding begins: the latest of its set's discuss marker and the settled or fixed of the set's earlier findings.
// It is false before the set's discuss.
func (l ledger) discussionStart(finding int64) (int64, bool) {
	turn, ok := l.owners[finding]
	if !ok {
		return 0, false
	}
	d, ok := l.discuss[turn]
	if !ok {
		return 0, false
	}
	start := d.Seq
	for _, f := range l.findingsOf(turn) {
		if f.Seq >= finding {
			break
		}
		s, ok := l.settled[f.Seq]
		if !ok {
			continue
		}
		start = max(start, s.marker.Seq)
		if fixed, ok := l.fixed[s.marker.Seq]; ok {
			start = max(start, fixed)
		}
	}
	return start, true
}

// asksOf lists each ask a roster member stored during the discussion of a finding, in seq order.
// The discussion ends at the finding's settled or halted marker.
func (l ledger) asksOf(finding int64) []int64 {
	start, ok := l.discussionStart(finding)
	if !ok {
		return nil
	}
	end := int64(math.MaxInt64)
	if s, ok := l.settled[finding]; ok {
		end = s.marker.Seq
	}
	if l.halt && l.haltedAt == finding {
		end = min(end, l.haltMarker)
	}
	var out []int64
	for _, m := range l.msgs {
		if m.Kind == kindAsk && m.Seq > start && m.Seq < end && slices.Contains(l.roster, m.From) {
			out = append(out, m.Seq)
		}
	}
	return out
}

// discussed is where the discussions of the set opened by turn ended: its discuss with no finding,
// otherwise the last settled or fixed of its findings. It is false while one of them is open.
func (l ledger) discussed(turn int64) (int64, bool) {
	d, ok := l.discuss[turn]
	if !ok {
		return 0, false
	}
	end := d.Seq
	for _, f := range l.findingsOf(turn) {
		s, ok := l.settled[f.Seq]
		if !ok || l.needsFix(f.Seq) {
			return 0, false
		}
		end = max(end, s.marker.Seq)
		if fixed, ok := l.fixed[s.marker.Seq]; ok {
			end = max(end, fixed)
		}
	}
	return end, true
}

// followedUp reports whether the follow-up target posted a turn or done after the set's discussions ended.
// A turn posted during a stall or fix launch is stored before that point and does not count.
func (l ledger) followedUp(turn client.Message) bool {
	end, ok := l.discussed(turn.Seq)
	if !ok {
		return false
	}
	target := l.targets[turn.From]
	for _, m := range l.msgs {
		if m.Seq > end && m.From == target && (m.Kind == kindTurn || m.Kind == kindDone) {
			return true
		}
	}
	return false
}

// fixedSince counts fixed messages stored after seq.
func (l ledger) fixedSince(seq int64) int {
	n := 0
	for _, m := range l.msgs {
		if m.Seq > seq && m.Kind == kindFixed {
			n++
		}
	}
	return n
}

// reported reports whether run already stored an error with this exact body.
func (l ledger) reported(body string) bool {
	for _, m := range l.msgs {
		if m.Kind == kindError && m.From == fromRun && m.Body == body {
			return true
		}
	}
	return false
}
