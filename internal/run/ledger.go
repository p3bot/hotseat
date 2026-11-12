// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/p3bot/hotseat/internal/client"
)

// ledgerKinds are the kinds a ledger reads. Stances other than propose and dismiss are never replayed.
var ledgerKinds = []string{kindTurn, kindDone, kindSay, kindFinding, kindPropose, kindDismiss, kindAsk, kindDiscuss, kindIssues, kindSettled, kindHalted, kindFixed, kindError}

// ledger is what a transcript records about sets and findings. It is a pure reading of its messages.
// A set runs from its turn to its discuss marker. A set with no discuss runs until its follow-up
// target's next turn or done, or without end when there is none.
type ledger struct {
	msgs   []client.Message
	roster []string
	// targets maps a turn sender to its follow-up target. A missing sender has no target.
	targets map[string]string

	sets    []client.Message
	discuss map[int64]client.Message
	// issues maps a turn to its set's issues marker.
	issues map[int64]client.Message
	// dropped maps a finding the aggregate dropped to the finding that kept its issue.
	dropped  map[int64]int64
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
		issues:  make(map[int64]client.Message),
		dropped: make(map[int64]int64),
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
		case l.marker(m, kindIssues):
			if turn, _, ok := parseIssues(m.Body); ok {
				if _, dup := l.issues[turn]; !dup {
					l.issues[turn] = m
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
	for turn, m := range l.issues {
		_, groups, _ := parseIssues(m.Body)
		for _, g := range groups {
			for _, dup := range g[1:] {
				if l.owners[dup] == turn && l.owners[g[0]] == turn {
					l.dropped[dup] = g[0]
				}
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

// findings are the valid findings the aggregate did not drop, in seq order.
func (l ledger) findings() []client.Message {
	var out []client.Message
	for _, f := range l.found {
		if _, gone := l.dropped[f.Seq]; !gone {
			out = append(out, f)
		}
	}
	return out
}

// findingsOf are the valid findings of the set opened by turn that the aggregate did not drop, in seq order.
func (l ledger) findingsOf(turn int64) []client.Message {
	var out []client.Message
	for _, f := range l.findings() {
		if l.owners[f.Seq] == turn {
			out = append(out, f)
		}
	}
	return out
}

// needsAggregate reports whether the set has a discuss, more than one finding, no issues marker, and no settled finding.
// A set whose discussions began without a marker keeps every finding.
func (l ledger) needsAggregate(turn int64) bool {
	if _, ok := l.discuss[turn]; !ok {
		return false
	}
	if _, ok := l.issues[turn]; ok {
		return false
	}
	fs := l.findingsOf(turn)
	for _, f := range fs {
		if _, ok := l.settled[f.Seq]; ok {
			return false
		}
	}
	return len(fs) > 1
}

// duplicatesOf lists the findings the aggregate dropped as duplicates of kept, in seq order.
func (l ledger) duplicatesOf(kept int64) []int64 {
	var out []int64
	for _, f := range l.found {
		if k, ok := l.dropped[f.Seq]; ok && k == kept {
			out = append(out, f.Seq)
		}
	}
	return out
}

// droppedAll lists every finding the aggregate dropped, in seq order.
func (l ledger) droppedAll() []int64 {
	var out []int64
	for _, f := range l.found {
		if _, ok := l.dropped[f.Seq]; ok {
			out = append(out, f.Seq)
		}
	}
	return out
}

// droppedOf lists the findings the aggregate dropped from the set opened by turn, in seq order.
func (l ledger) droppedOf(turn int64) []int64 {
	var out []int64
	for _, seq := range l.droppedAll() {
		if l.owners[seq] == turn {
			out = append(out, seq)
		}
	}
	return out
}

// unanswered lists each roster recipient of turn, in to order, that published no reply inside the set's window.
func (l ledger) unanswered(turn client.Message) []string {
	end := l.ends[turn.Seq]
	var out []string
	for _, name := range turn.To {
		if !slices.Contains(l.roster, name) || slices.Contains(out, name) {
			continue
		}
		answered := false
		for _, m := range l.msgs {
			if m.Seq > turn.Seq && m.Seq < end && replies(m, name, turn) {
				answered = true
				break
			}
		}
		if !answered {
			out = append(out, name)
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

// unresolved lists each valid finding the aggregate kept with no settled marker, and each settled change with no fixed.
func (l ledger) unresolved() (unsettled, unfixed []int64) {
	for _, f := range l.findings() {
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

// discussionStart is where the discussion of a finding begins: the latest of its set's discuss and issues markers and
// the settled or fixed of the set's earlier findings. It is false before the set's discuss.
func (l ledger) discussionStart(finding int64) (int64, bool) {
	turn, ok := l.owners[finding]
	if !ok {
		return 0, false
	}
	start, ok := l.opened(turn)
	if !ok {
		return 0, false
	}
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

// opened is where the set's discussions begin: its issues marker, or its discuss when the set has no issues marker.
func (l ledger) opened(turn int64) (int64, bool) {
	d, ok := l.discuss[turn]
	if !ok {
		return 0, false
	}
	if m, ok := l.issues[turn]; ok {
		return max(d.Seq, m.Seq), true
	}
	return d.Seq, true
}

// discussed is where the discussions of the set opened by turn ended: where they opened with no kept finding,
// otherwise the last settled or fixed of its kept findings. It is false while one of them is open.
func (l ledger) discussed(turn int64) (int64, bool) {
	end, ok := l.opened(turn)
	if !ok {
		return 0, false
	}
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

// issuesBody is the issues marker: the turn's seq, then one line per issue with the kept finding first and its dropped duplicates after.
func issuesBody(turn int64, groups [][]int64) string {
	lines := []string{strconv.FormatInt(turn, 10)}
	for _, g := range groups {
		lines = append(lines, seqList(g))
	}
	return strings.Join(lines, "\n")
}

func parseIssues(body string) (turn int64, groups [][]int64, ok bool) {
	lines := strings.Split(body, "\n")
	turn, err := strconv.ParseInt(lines[0], 10, 64)
	if err != nil {
		return 0, nil, false
	}
	for _, line := range lines[1:] {
		g, ok := parseGroup(line)
		if !ok {
			return 0, nil, false
		}
		groups = append(groups, g)
	}
	return turn, groups, true
}

// parseGroup reads one issue line: one or more seqs separated by spaces.
func parseGroup(line string) ([]int64, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil, false
	}
	g := make([]int64, len(fields))
	for i, f := range fields {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n < 1 {
			return nil, false
		}
		g[i] = n
	}
	return g, true
}

// aggregate checks a seat's aggregate body against the set's findings and returns its issues with each kept finding
// first and its duplicates after, in seq order. Every finding is on exactly one line.
func aggregate(body string, findings []int64) ([][]int64, bool) {
	seen := make(map[int64]bool)
	var groups [][]int64
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		g, ok := parseGroup(line)
		if !ok {
			return nil, false
		}
		for _, seq := range g {
			if seen[seq] || !slices.Contains(findings, seq) {
				return nil, false
			}
			seen[seq] = true
		}
		slices.Sort(g[1:])
		groups = append(groups, g)
	}
	if len(seen) != len(findings) {
		return nil, false
	}
	slices.SortFunc(groups, func(a, b []int64) int { return cmp.Compare(a[0], b[0]) })
	return groups, true
}

// singles is the issues of a set whose aggregate failed: each finding is its own issue.
func singles(findings []int64) [][]int64 {
	groups := make([][]int64, len(findings))
	for i, seq := range findings {
		groups[i] = []int64{seq}
	}
	return groups
}
