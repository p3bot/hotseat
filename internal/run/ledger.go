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
var ledgerKinds = []string{kindTurn, kindDone, kindSay, kindFinding, kindPropose, kindDismiss, kindAsk, kindFixed, kindError}

// records are the decision records of one conversation that run has read or written.
// A record is a fact that never changes, so each key is held once.
type records struct {
	sets      []client.Set
	groupings []client.Grouping
	settles   []client.Settle
	fixes     []client.Fix
	halts     []client.Halt
	stops     []client.Stop
}

// add keeps every record a /records or /record result carries that is not already held.
func (r *records) add(res client.Result) {
	if res.Sets != nil {
		r.sets = appendNew(r.sets, setKey, *res.Sets...)
	}
	if res.Set != nil {
		r.sets = appendNew(r.sets, setKey, *res.Set)
	}
	if res.Groupings != nil {
		r.groupings = appendNew(r.groupings, groupingKey, *res.Groupings...)
	}
	if res.Grouping != nil {
		r.groupings = appendNew(r.groupings, groupingKey, *res.Grouping)
	}
	if res.Settles != nil {
		r.settles = appendNew(r.settles, settleKey, *res.Settles...)
	}
	if res.Settle != nil {
		r.settles = appendNew(r.settles, settleKey, *res.Settle)
	}
	if res.Fixes != nil {
		r.fixes = appendNew(r.fixes, fixKey, *res.Fixes...)
	}
	if res.Fix != nil {
		r.fixes = appendNew(r.fixes, fixKey, *res.Fix)
	}
	if res.Halts != nil {
		r.halts = appendNew(r.halts, haltKey, *res.Halts...)
	}
	if res.Halt != nil {
		r.halts = appendNew(r.halts, haltKey, *res.Halt)
	}
	if res.Stops != nil {
		r.stops = appendNew(r.stops, stopKey, *res.Stops...)
	}
	if res.Stop != nil {
		r.stops = appendNew(r.stops, stopKey, *res.Stop)
	}
}

func setKey(s client.Set) int64           { return s.Turn }
func groupingKey(g client.Grouping) int64 { return g.Turn }
func settleKey(s client.Settle) int64     { return s.Finding }
func fixKey(f client.Fix) int64           { return f.Finding }

// haltKey is constant because a conversation holds at most one halt.
func haltKey(client.Halt) int64 { return 0 }

// stopKey is constant because a conversation holds at most one stop.
func stopKey(client.Stop) int64 { return 0 }

func appendNew[T any](list []T, key func(T) int64, items ...T) []T {
	for _, v := range items {
		if !slices.ContainsFunc(list, func(o T) bool { return key(o) == key(v) }) {
			list = append(list, v)
		}
	}
	return list
}

// clip is r with every list clipped, so a later add cannot write into a snapshot.
func (r records) clip() records {
	return records{
		sets:      slices.Clip(r.sets),
		groupings: slices.Clip(r.groupings),
		settles:   slices.Clip(r.settles),
		fixes:     slices.Clip(r.fixes),
		halts:     slices.Clip(r.halts),
		stops:     slices.Clip(r.stops),
	}
}

// stopped returns the stop record, the done that stopped the run.
func (r records) stopped() (client.Stop, bool) {
	if len(r.stops) == 0 {
		return client.Stop{}, false
	}
	return r.stops[0], true
}

// before is the records a stop at seq counts: each with after below seq, and each fix whose fixed is below seq.
// The stop record is carried whole, since it is the cut and counts toward nothing.
func (r records) before(seq int64) records {
	keep := func(after int64) bool { return after < seq }
	return records{
		sets:      filter(r.sets, func(s client.Set) bool { return keep(s.After) }),
		groupings: filter(r.groupings, func(g client.Grouping) bool { return keep(g.After) }),
		settles:   filter(r.settles, func(s client.Settle) bool { return keep(s.After) }),
		fixes:     filter(r.fixes, func(f client.Fix) bool { return keep(f.Fixed) }),
		halts:     filter(r.halts, func(h client.Halt) bool { return keep(h.After) }),
		stops:     r.stops,
	}
}

// awaitsFix reports whether a settled change has no fix record, so a stored fixed may still be credited to it.
func (r records) awaitsFix() bool {
	for _, s := range r.settles {
		if changes(s) && !slices.ContainsFunc(r.fixes, func(f client.Fix) bool { return f.Finding == s.Finding }) {
			return true
		}
	}
	return false
}

func filter[T any](list []T, keep func(T) bool) []T {
	var out []T
	for _, v := range list {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// ruledOutDones returns the seq of every done in msgs that a stored bad done error names.
// Senders are caller-supplied, so only an error exactly as run stores it counts: its transaction id and body
// are derived from that done.
func ruledOutDones(msgs []client.Message) []int64 {
	senders := make(map[int64]string)
	for _, m := range msgs {
		if m.Kind == kindDone {
			senders[m.Seq] = m.From
		}
	}
	var seqs []int64
	for _, m := range msgs {
		if m.Kind != kindError || m.From != fromRun || m.Re == nil || !slices.Equal(m.To, []string{fromOwner}) {
			continue
		}
		from, ok := senders[*m.Re]
		if ok && m.TxID == factTx(classBadDone, seqText(*m.Re)) && m.Body == errBody(from, classBadDone) {
			seqs = append(seqs, *m.Re)
		}
	}
	return seqs
}

// changes reports whether a settle's agreed proposal asks for a fix. Only a dismiss does not.
func changes(s client.Settle) bool {
	return s.ProposalKind != kindDismiss
}

// ledger is what the transcript and the decision records say about sets and findings. It is a pure reading of them.
// A set with a set record ends at its after. An open set ends at its follow-up target's next turn or done, or has no end.
// A message is inside a record when its seq is at or below the record's after.
type ledger struct {
	msgs   []client.Message
	recs   records
	roster []string
	// targets maps a turn sender to its follow-up target. A missing sender has no target.
	targets map[string]string
	// fixers are the seat and its backup. Either may publish the fixed of any settled change, as the finder may.
	fixers []string

	bySeq   map[int64]client.Message
	turns   []client.Message
	closed  map[int64]client.Set
	grouped map[int64]client.Grouping
	// dropped maps a finding the aggregate dropped to the finding that kept its issue.
	dropped map[int64]int64
	settled map[int64]client.Settle
	fixes   map[int64]client.Fix
	halt    *client.Halt
	// ends maps a turn to the last seq inside its set.
	ends   map[int64]int64
	owners map[int64]int64
	found  []client.Message
}

// buildLedger is the ledger of one conversation from bus reads: the binding's roster and seat, the member rows, the
// transcript, and the decision records. Messages of kinds a ledger does not read are ignored.
// A turn sender's follow-up target is itself while it is a roster member that has not timed out, and otherwise the
// seat's backup. The backup is a fixer only while a settled change has no fix record.
func buildLedger(roster []string, seat string, rows []client.Member, msgs []client.Message, recs records) ledger {
	backup := backupOf(roster, seat, rows)
	targets := make(map[string]string)
	for _, m := range msgs {
		if m.Kind != kindTurn {
			continue
		}
		if _, ok := targets[m.From]; ok {
			continue
		}
		if followsItself(roster, m.From, rowStatus(rows, m.From)) {
			targets[m.From] = m.From
		} else {
			targets[m.From] = backup
		}
	}
	fixers := []string{seat}
	if backup != "" && recs.awaitsFix() {
		fixers = append(fixers, backup)
	}
	return newLedger(filter(msgs, func(m client.Message) bool { return slices.Contains(ledgerKinds, m.Kind) }), recs, roster, targets, fixers)
}

// Outcomes of a stop result.
const (
	outcomeDone      = "done"
	outcomeDeadlock  = "deadlock"
	outcomeUnsettled = "unsettled"
)

// stopResult is what a run that a done stopped reports: each kept finding with no settle, each settled change with no
// fix, and the outcome.
type stopResult struct {
	unsettled []int64
	unfixed   []int64
	outcome   string
}

// stopResultAt is the stop result of a done at seq, read from the ledger cut before it. With nothing unresolved the
// outcome is done. Otherwise it is deadlock when a halt is below the done, and unsettled when none is.
func (l ledger) stopResultAt(done int64) stopResult {
	cut := l.before(done)
	r := stopResult{outcome: outcomeDone}
	r.unsettled, r.unfixed = cut.unresolved()
	if len(r.unsettled) == 0 && len(r.unfixed) == 0 {
		return r
	}
	r.outcome = outcomeUnsettled
	if _, halted := cut.halted(); halted {
		r.outcome = outcomeDeadlock
	}
	return r
}

func newLedger(msgs []client.Message, recs records, roster []string, targets map[string]string, fixers []string) ledger {
	l := ledger{
		msgs:    msgs,
		recs:    recs,
		roster:  roster,
		targets: targets,
		fixers:  fixers,
		bySeq:   make(map[int64]client.Message, len(msgs)),
		closed:  make(map[int64]client.Set),
		grouped: make(map[int64]client.Grouping),
		dropped: make(map[int64]int64),
		settled: make(map[int64]client.Settle),
		fixes:   make(map[int64]client.Fix),
		ends:    make(map[int64]int64),
		owners:  make(map[int64]int64),
	}
	for _, m := range msgs {
		l.bySeq[m.Seq] = m
		if m.Kind == kindTurn {
			if _, _, ok := parseMode(m.Body); ok {
				l.turns = append(l.turns, m)
			}
		}
	}
	for _, s := range recs.sets {
		l.closed[s.Turn] = s
		for _, f := range s.Findings {
			l.owners[f] = s.Turn
		}
	}
	for _, g := range recs.groupings {
		l.grouped[g.Turn] = g
		for _, d := range g.Drops {
			l.dropped[d.Finding] = d.Kept
		}
	}
	for _, s := range recs.settles {
		l.settled[s.Finding] = s
	}
	for _, f := range recs.fixes {
		l.fixes[f.Finding] = f
	}
	if len(recs.halts) > 0 {
		l.halt = &recs.halts[0]
	}
	for _, turn := range l.turns {
		l.ends[turn.Seq] = l.setEnd(turn)
	}
	// Closed sets' findings come first, because inDiscussions reads them while open sets take theirs.
	for _, m := range msgs {
		if _, ok := l.owners[m.Seq]; ok && m.Kind == kindFinding {
			l.found = append(l.found, m)
		}
	}
	for _, m := range msgs {
		if m.Kind != kindFinding || !slices.Contains(roster, m.From) {
			continue
		}
		if _, owned := l.owners[m.Seq]; owned || l.inDiscussions(m.Seq) {
			continue
		}
		for _, turn := range l.turns {
			if turn.Seq >= m.Seq {
				break
			}
			if _, ok := l.closed[turn.Seq]; ok {
				continue
			}
			if concerns(m, turn.Seq) && l.accepts(turn, m) {
				l.owners[m.Seq] = turn.Seq
				l.found = append(l.found, m)
				break
			}
		}
	}
	slices.SortFunc(l.found, func(a, b client.Message) int { return cmp.Compare(a.Seq, b.Seq) })
	return l
}

// inDiscussions reports whether seq lies after a set record and inside that set's discussions, so a finding there
// opens nothing, even inside an open set's window. The discussions of a set with two or more findings and no grouping
// have not ended.
func (l ledger) inDiscussions(seq int64) bool {
	for turn, s := range l.closed {
		if seq <= s.After {
			continue
		}
		if end, ok := l.discussed(turn); !ok || seq <= end {
			return true
		}
	}
	return false
}

// before is the ledger a stop at seq counts: the messages stored before seq and the records below it.
func (l ledger) before(seq int64) ledger {
	msgs := l.msgs
	if i := slices.IndexFunc(msgs, func(m client.Message) bool { return m.Seq >= seq }); i >= 0 {
		msgs = msgs[:i]
	}
	return newLedger(msgs, l.recs.before(seq), l.roster, l.targets, l.fixers)
}

// recorded reports whether the set opened by turn has a set record.
func (l ledger) recorded(turn int64) bool {
	_, ok := l.closed[turn]
	return ok
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

// accepts reports whether the open set opened by turn takes finding: the finding is inside the set and its sender is a recipient.
func (l ledger) accepts(turn, finding client.Message) bool {
	return finding.Seq > turn.Seq && finding.Seq <= l.ends[turn.Seq] && slices.Contains(turn.To, finding.From)
}

func (l ledger) setEnd(turn client.Message) int64 {
	if s, ok := l.closed[turn.Seq]; ok {
		return s.After
	}
	if m, ok := l.later(turn); ok {
		return m.Seq - 1
	}
	return math.MaxInt64
}

// valid reports whether a finding opened a discussion. A closed set's findings are its set record's. An open set
// takes a finding whose sender is a recipient and that is not inside another set's discussions. A finding with re
// belongs only to the open set its re names. One with no re that lies in several windows belongs to the first.
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

// needsAggregate reports whether the set's record holds two or more findings and the set has no grouping.
func (l ledger) needsAggregate(turn int64) bool {
	s, ok := l.closed[turn]
	return ok && len(s.Findings) > 1 && !l.isGrouped(turn)
}

func (l ledger) isGrouped(turn int64) bool {
	_, ok := l.grouped[turn]
	return ok
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

// unanswered lists each roster recipient of turn, in to order, that published no reply inside the set.
func (l ledger) unanswered(turn client.Message) []string {
	end := l.ends[turn.Seq]
	var out []string
	for _, name := range turn.To {
		if !slices.Contains(l.roster, name) || slices.Contains(out, name) {
			continue
		}
		answered := false
		for _, m := range l.msgs {
			if m.Seq > turn.Seq && m.Seq <= end && replies(m, name, turn) {
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

// settledOf is the settle record of a finding.
func (l ledger) settledOf(finding int64) (client.Settle, bool) {
	s, ok := l.settled[finding]
	return s, ok
}

// sender is the member that published the message at seq, or empty when the ledger does not hold it.
func (l ledger) sender(seq int64) string {
	return l.bySeq[seq].From
}

// fixOf is the fixed that applied a finding's settled change: the one its fix record names, or else the first creditable one.
func (l ledger) fixOf(finding int64) (int64, bool) {
	if f, ok := l.fixes[finding]; ok {
		return f.Fixed, true
	}
	return l.creditable(finding)
}

// creditable is the first fixed stored after the finding's settle, with re naming the finding, from the finder,
// the seat, or the seat's backup. A fixed with no re is credited only inside the fix launch that published it.
func (l ledger) creditable(finding int64) (int64, bool) {
	s, ok := l.settled[finding]
	if !ok {
		return 0, false
	}
	finder := l.sender(finding)
	for _, m := range l.msgs {
		if m.Kind != kindFixed || m.Seq <= s.After || m.Re == nil || *m.Re != finding {
			continue
		}
		if m.From == finder || slices.Contains(l.fixers, m.From) {
			return m.Seq, true
		}
	}
	return 0, false
}

// hasFixRecord reports whether a fix record names the finding.
func (l ledger) hasFixRecord(finding int64) bool {
	_, ok := l.fixes[finding]
	return ok
}

// needsFix reports whether a finding settled on a change that nothing fixed.
func (l ledger) needsFix(finding int64) bool {
	s, ok := l.settled[finding]
	if !ok || !changes(s) {
		return false
	}
	_, fixed := l.fixOf(finding)
	return !fixed
}

// halted is the finding of the halt record.
func (l ledger) halted() (int64, bool) {
	if l.halt == nil {
		return 0, false
	}
	return l.halt.Finding, true
}

// unresolved lists each valid finding the aggregate kept with no settle, and each settled change with no fix.
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

// underWay is the finding being worked on: the first kept finding in seq order whose discussion has started and
// that has no settle, or settled on a change with no fix. It is false when there is none or a halt names it, since
// findings are worked one at a time and a halt ends every discussion.
func (l ledger) underWay() (int64, bool) {
	for _, f := range l.findings() {
		if _, started := l.discussionStart(f.Seq); !started {
			continue
		}
		if _, settled := l.settled[f.Seq]; settled && !l.needsFix(f.Seq) {
			continue
		}
		if halted, ok := l.halted(); ok && halted == f.Seq {
			return 0, false
		}
		return f.Seq, true
	}
	return 0, false
}

// discussionStart is where the discussion of a finding begins: the latest of where its set's discussions opened and
// the settle and fix of each earlier kept finding of the set. It is false before the set's discussions open.
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
		start = max(start, l.resolvedAt(f.Seq))
	}
	return start, true
}

// resolvedAt is the latest of a finding's settle after and its fixed, or zero when it has neither.
func (l ledger) resolvedAt(finding int64) int64 {
	var at int64
	if s, ok := l.settled[finding]; ok {
		at = s.After
	}
	if fixed, ok := l.fixOf(finding); ok {
		at = max(at, fixed)
	}
	return at
}

// asksOf lists each ask a roster member stored during the discussion of a finding, in seq order.
// The discussion ends at the after of the finding's settle or halt.
func (l ledger) asksOf(finding int64) []int64 {
	start, ok := l.discussionStart(finding)
	if !ok {
		return nil
	}
	end := int64(math.MaxInt64)
	if s, ok := l.settled[finding]; ok {
		end = s.After
	}
	if l.halt != nil && l.halt.Finding == finding {
		end = min(end, l.halt.After)
	}
	var out []int64
	for _, m := range l.msgs {
		if m.Kind == kindAsk && m.Seq > start && m.Seq <= end && slices.Contains(l.roster, m.From) {
			out = append(out, m.Seq)
		}
	}
	return out
}

// opened is where the set's discussions begin: its grouping's after, or its set record's after when it has no finding.
func (l ledger) opened(turn int64) (int64, bool) {
	if g, ok := l.grouped[turn]; ok {
		return g.After, true
	}
	if s, ok := l.closed[turn]; ok && len(s.Findings) == 0 {
		return s.After, true
	}
	return 0, false
}

// discussed is where the discussions of the set opened by turn ended: the latest of where they opened and each kept
// finding's settle after and fixed. It is false while a kept finding is unsettled or a settled change is unfixed.
func (l ledger) discussed(turn int64) (int64, bool) {
	end, ok := l.opened(turn)
	if !ok {
		return 0, false
	}
	for _, f := range l.findingsOf(turn) {
		if _, ok := l.settled[f.Seq]; !ok || l.needsFix(f.Seq) {
			return 0, false
		}
		end = max(end, l.resolvedAt(f.Seq))
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

// fixesOf counts the fix records of the findings of the set opened by turn.
func (l ledger) fixesOf(turn int64) int {
	n := 0
	for finding := range l.fixes {
		if l.owners[finding] == turn {
			n++
		}
	}
	return n
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

// drops are the grouping record's drops for issues: each duplicate and the finding that kept its issue.
func drops(groups [][]int64) []client.Drop {
	var out []client.Drop
	for _, g := range groups {
		for _, dup := range g[1:] {
			out = append(out, client.Drop{Finding: dup, Kept: g[0]})
		}
	}
	return out
}

// singles is the issues of a set whose aggregate failed: each finding is its own issue.
func singles(findings []int64) [][]int64 {
	groups := make([][]int64, len(findings))
	for i, seq := range findings {
		groups[i] = []int64{seq}
	}
	return groups
}
