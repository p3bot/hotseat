// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// Set is a closed turn and its findings, ascending.
type Set struct {
	Turn     int64
	Findings []int64
	After    int64
}

// Drop is a finding of a set and the finding of the same set that kept its issue.
type Drop struct {
	Finding int64
	Kept    int64
}

// Grouping is a set's duplicates, ascending by dropped finding.
type Grouping struct {
	Turn  int64
	Drops []Drop
	After int64
}

// Settle is the agreed proposal for a finding. ProposalKind is finding, propose, or dismiss.
type Settle struct {
	Finding      int64
	Proposal     int64
	ProposalKind string
	After        int64
}

// Fix is the fixed message for a settled finding. SettledAfter is that settle's After.
type Fix struct {
	Finding      int64
	SettledAfter int64
	Fixed        int64
}

// Halt is the finding a conversation's run stopped on.
type Halt struct {
	Finding int64
	After   int64
}

// Stop is the done that stopped a conversation's run.
type Stop struct {
	Done int64
}

// Records is every decision record of one conversation, each list in key order.
type Records struct {
	Sets      []Set
	Groupings []Grouping
	Settles   []Settle
	Fixes     []Fix
	Halts     []Halt
	Stops     []Stop
}

// WriteSet stores a set and its findings in one transaction.
// A set with exactly one finding also stores its grouping with no drops, and
// that grouping is returned. Otherwise the grouping is nil.
// already is true when the turn already has a set with the same findings,
// compared as a set. The stored records are returned and nothing is written.
// The same turn with other findings returns ErrConflict.
func (s *Store) WriteSet(ctx context.Context, conv string, turn int64, findings []int64) (Set, *Grouping, bool, error) {
	want := slices.Clone(findings)
	slices.Sort(want)
	if want == nil {
		want = []int64{}
	}
	var (
		set      Set
		grouping *Grouping
		already  bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		after, err := recordPosition(ctx, tx, conv)
		if err != nil {
			return err
		}
		stored, found, err := setByTurn(ctx, tx, conv, turn)
		if err != nil {
			return err
		}
		if found {
			if !slices.Equal(stored.Findings, want) {
				return ErrConflict
			}
			set = stored
			if len(stored.Findings) == 1 {
				g, found, err := groupingByTurn(ctx, tx, conv, turn)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("set %d has one finding and no grouping", turn)
				}
				grouping = &g
			}
			already = true
			return errNoChange
		}
		if err := insertRecord(ctx, tx,
			`INSERT INTO sets (conversation, turn, after) VALUES (?, ?, ?)`,
			conv, turn, after); err != nil {
			return err
		}
		for _, f := range want {
			if err := insertRecord(ctx, tx,
				`INSERT INTO set_findings (conversation, turn, finding) VALUES (?, ?, ?)`,
				conv, turn, f); err != nil {
				return err
			}
		}
		set = Set{Turn: turn, Findings: want, After: after}
		if len(want) == 1 {
			if err := insertRecord(ctx, tx,
				`INSERT INTO groupings (conversation, turn, after) VALUES (?, ?, ?)`,
				conv, turn, after); err != nil {
				return err
			}
			grouping = &Grouping{Turn: turn, Drops: []Drop{}, After: after}
		}
		return nil
	})
	if err != nil {
		return Set{}, nil, false, err
	}
	return set, grouping, already, nil
}

// WriteGrouping stores a set's grouping and its drops in one transaction.
// A drop whose finding another drop keeps returns ErrKeptDropped.
// already is true when the turn already has a grouping with the same drops,
// compared as a set. The same turn with other drops returns ErrConflict.
func (s *Store) WriteGrouping(ctx context.Context, conv string, turn int64, drops []Drop) (Grouping, bool, error) {
	want := slices.Clone(drops)
	slices.SortFunc(want, compareDrops)
	if want == nil {
		want = []Drop{}
	}
	var (
		grouping Grouping
		already  bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		after, err := recordPosition(ctx, tx, conv)
		if err != nil {
			return err
		}
		stored, found, err := groupingByTurn(ctx, tx, conv, turn)
		if err != nil {
			return err
		}
		if found {
			if !slices.Equal(stored.Drops, want) {
				return ErrConflict
			}
			grouping = stored
			already = true
			return errNoChange
		}
		dropped := make(map[int64]bool, len(want))
		for _, d := range want {
			dropped[d.Finding] = true
		}
		for _, d := range want {
			// A drop that keeps itself is the schema's finding <> kept check.
			if d.Kept != d.Finding && dropped[d.Kept] {
				return ErrKeptDropped
			}
		}
		if err := insertRecord(ctx, tx,
			`INSERT INTO groupings (conversation, turn, after) VALUES (?, ?, ?)`,
			conv, turn, after); err != nil {
			return err
		}
		for _, d := range want {
			if err := insertRecord(ctx, tx,
				`INSERT INTO drops (conversation, turn, finding, kept) VALUES (?, ?, ?, ?)`,
				conv, turn, d.Finding, d.Kept); err != nil {
				return err
			}
		}
		grouping = Grouping{Turn: turn, Drops: want, After: after}
		return nil
	})
	if err != nil {
		return Grouping{}, false, err
	}
	return grouping, already, nil
}

// WriteSettle stores the agreed proposal for a finding.
// kind is the proposal's message kind as the caller holds it. The store does
// not look it up: a kind that differs from the stored message is ErrRecordReference.
// A finding that is not a kept finding of a grouped set returns ErrNotKept.
// already is true when the finding already has a settle with the same
// proposal and kind. Any other proposal or kind returns ErrConflict.
func (s *Store) WriteSettle(ctx context.Context, conv string, finding, proposal int64, kind string) (Settle, bool, error) {
	var (
		settle  Settle
		already bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		after, err := recordPosition(ctx, tx, conv)
		if err != nil {
			return err
		}
		stored, found, err := settleByFinding(ctx, tx, conv, finding)
		if err != nil {
			return err
		}
		if found {
			if stored.Proposal != proposal || stored.ProposalKind != kind {
				return ErrConflict
			}
			settle = stored
			already = true
			return errNoChange
		}
		if err := requireKept(ctx, tx, conv, finding); err != nil {
			return err
		}
		if err := insertRecord(ctx, tx, `
			INSERT INTO settles (conversation, finding, proposal, proposal_kind, after)
			VALUES (?, ?, ?, ?, ?)`,
			conv, finding, proposal, kind, after); err != nil {
			return err
		}
		settle = Settle{Finding: finding, Proposal: proposal, ProposalKind: kind, After: after}
		return nil
	})
	if err != nil {
		return Settle{}, false, err
	}
	return settle, already, nil
}

// WriteFix stores the fixed message for a settled finding. SettledAfter is
// read from the stored settle. A finding with no settle returns
// ErrRecordReference, and a settle whose proposal is a dismiss returns
// ErrFixDismissed. already is true when the finding already has a fix with
// the same fixed message. Any other returns ErrConflict.
func (s *Store) WriteFix(ctx context.Context, conv string, finding, fixed int64) (Fix, bool, error) {
	var (
		fix     Fix
		already bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if _, err := conversationByName(ctx, tx, conv); err != nil {
			return err
		}
		stored, found, err := fixByFinding(ctx, tx, conv, finding)
		if err != nil {
			return err
		}
		if found {
			if stored.Fixed != fixed {
				return ErrConflict
			}
			fix = stored
			already = true
			return errNoChange
		}
		settle, found, err := settleByFinding(ctx, tx, conv, finding)
		if err != nil {
			return err
		}
		if !found {
			return ErrRecordReference
		}
		if settle.ProposalKind == "dismiss" {
			return ErrFixDismissed
		}
		if err := insertRecord(ctx, tx, `
			INSERT INTO fixes (conversation, finding, settled_after, fixed)
			VALUES (?, ?, ?, ?)`,
			conv, finding, settle.After, fixed); err != nil {
			return err
		}
		fix = Fix{Finding: finding, SettledAfter: settle.After, Fixed: fixed}
		return nil
	})
	if err != nil {
		return Fix{}, false, err
	}
	return fix, already, nil
}

// WriteHalt stores the finding a conversation's run stopped on. A
// conversation holds at most one halt. A finding that is not a kept finding
// of a grouped set returns ErrNotKept. already is true when the stored halt
// names the same finding. Another finding returns ErrConflict.
func (s *Store) WriteHalt(ctx context.Context, conv string, finding int64) (Halt, bool, error) {
	var (
		halt    Halt
		already bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		after, err := recordPosition(ctx, tx, conv)
		if err != nil {
			return err
		}
		var stored Halt
		err = tx.QueryRowContext(ctx,
			`SELECT finding, after FROM halts WHERE conversation = ?`, conv).Scan(&stored.Finding, &stored.After)
		switch {
		case err == nil:
			if stored.Finding != finding {
				return ErrConflict
			}
			halt = stored
			already = true
			return errNoChange
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := requireKept(ctx, tx, conv, finding); err != nil {
			return err
		}
		if err := insertRecord(ctx, tx,
			`INSERT INTO halts (conversation, finding, after) VALUES (?, ?, ?)`,
			conv, finding, after); err != nil {
			return err
		}
		halt = Halt{Finding: finding, After: after}
		return nil
	})
	if err != nil {
		return Halt{}, false, err
	}
	return halt, already, nil
}

// WriteStop stores the done that stopped a conversation's run. A
// conversation holds at most one stop. A seq that is not a done of the
// conversation returns ErrRecordReference. already is true when the stored
// stop names the same done. Another done returns ErrConflict.
func (s *Store) WriteStop(ctx context.Context, conv string, done int64) (Stop, bool, error) {
	var (
		stop    Stop
		already bool
	)
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if _, err := conversationByName(ctx, tx, conv); err != nil {
			return err
		}
		var stored Stop
		err := tx.QueryRowContext(ctx,
			`SELECT done FROM stops WHERE conversation = ?`, conv).Scan(&stored.Done)
		switch {
		case err == nil:
			if stored.Done != done {
				return ErrConflict
			}
			stop = stored
			already = true
			return errNoChange
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := insertRecord(ctx, tx,
			`INSERT INTO stops (conversation, done) VALUES (?, ?)`, conv, done); err != nil {
			return err
		}
		stop = Stop{Done: done}
		return nil
	})
	if err != nil {
		return Stop{}, false, err
	}
	return stop, already, nil
}

// Records returns every decision record of one conversation. Each list is in
// key order and is empty rather than nil. A missing conversation returns ErrNotFound.
func (s *Store) Records(ctx context.Context, conv string) (Records, error) {
	var out Records
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if _, err := conversationByName(ctx, tx, conv); err != nil {
			return err
		}
		var err error
		if out.Sets, err = readSets(ctx, tx, conv, nil); err != nil {
			return err
		}
		if out.Groupings, err = readGroupings(ctx, tx, conv, nil); err != nil {
			return err
		}
		if out.Settles, err = readSettles(ctx, tx, conv, nil); err != nil {
			return err
		}
		if out.Fixes, err = readFixes(ctx, tx, conv, nil); err != nil {
			return err
		}
		out.Halts, err = queryRows(ctx, tx, func(row rowScanner) (Halt, error) {
			var h Halt
			err := row.Scan(&h.Finding, &h.After)
			return h, err
		}, `SELECT finding, after FROM halts WHERE conversation = ?`, conv)
		if err != nil {
			return err
		}
		out.Stops, err = queryRows(ctx, tx, func(row rowScanner) (Stop, error) {
			var st Stop
			err := row.Scan(&st.Done)
			return st, err
		}, `SELECT done FROM stops WHERE conversation = ?`, conv)
		return err
	})
	if err != nil {
		return Records{}, err
	}
	return out, nil
}

// recordPosition reads the conversation and its highest seq, which is after
// for a record committed in this transaction. Seq order is commit order only
// because the store has one writer. A conversation with no message has
// nothing a record can reference.
func recordPosition(ctx context.Context, tx *sql.Tx, conv string) (int64, error) {
	if _, err := conversationByName(ctx, tx, conv); err != nil {
		return 0, err
	}
	var after sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM messages WHERE conversation = ?`, conv).Scan(&after); err != nil {
		return 0, err
	}
	if !after.Valid {
		return 0, ErrRecordReference
	}
	return after.Int64, nil
}

// requireKept returns ErrNotKept for a finding of a set that has no grouping,
// or that a drop names. A finding in no set is left to the settles and halts
// foreign keys.
func requireKept(ctx context.Context, tx *sql.Tx, conv string, finding int64) error {
	var grouped, dropped bool
	err := tx.QueryRowContext(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM groupings g WHERE g.conversation = sf.conversation AND g.turn = sf.turn),
			EXISTS (SELECT 1 FROM drops d WHERE d.conversation = sf.conversation AND d.finding = sf.finding)
		FROM set_findings sf
		WHERE sf.conversation = ? AND sf.finding = ?`, conv, finding).Scan(&grouped, &dropped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !grouped || dropped {
		return ErrNotKept
	}
	return nil
}

// insertRecord runs one record insert. The record's own key is compared before
// any insert, so every constraint failure here is a reference, kind, check, or
// uniqueness rule of the schema.
func insertRecord(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	_, err := tx.ExecContext(ctx, query, args...)
	switch sqliteCode(err) {
	case sqliteConstraintForeignKey, sqliteConstraintCheck, sqliteConstraintPrimaryKey, sqliteConstraintUnique:
		return ErrRecordReference
	}
	return err
}

func setByTurn(ctx context.Context, tx *sql.Tx, conv string, turn int64) (Set, bool, error) {
	sets, err := readSets(ctx, tx, conv, &turn)
	if err != nil || len(sets) == 0 {
		return Set{}, false, err
	}
	return sets[0], true, nil
}

func groupingByTurn(ctx context.Context, tx *sql.Tx, conv string, turn int64) (Grouping, bool, error) {
	gs, err := readGroupings(ctx, tx, conv, &turn)
	if err != nil || len(gs) == 0 {
		return Grouping{}, false, err
	}
	return gs[0], true, nil
}

func settleByFinding(ctx context.Context, tx *sql.Tx, conv string, finding int64) (Settle, bool, error) {
	ss, err := readSettles(ctx, tx, conv, &finding)
	if err != nil || len(ss) == 0 {
		return Settle{}, false, err
	}
	return ss[0], true, nil
}

func fixByFinding(ctx context.Context, tx *sql.Tx, conv string, finding int64) (Fix, bool, error) {
	fs, err := readFixes(ctx, tx, conv, &finding)
	if err != nil || len(fs) == 0 {
		return Fix{}, false, err
	}
	return fs[0], true, nil
}

// readSets reads the sets of conv in turn order. A non-nil key reads only that turn.
func readSets(ctx context.Context, tx *sql.Tx, conv string, key *int64) ([]Set, error) {
	only := nullSeq(key)
	sets, err := queryRows(ctx, tx, func(row rowScanner) (Set, error) {
		s := Set{Findings: []int64{}}
		err := row.Scan(&s.Turn, &s.After)
		return s, err
	}, `SELECT turn, after FROM sets
		WHERE conversation = ? AND (? IS NULL OR turn = ?)
		ORDER BY turn`, conv, only, only)
	if err != nil {
		return nil, err
	}
	type member struct{ turn, finding int64 }
	members, err := queryRows(ctx, tx, func(row rowScanner) (member, error) {
		var m member
		err := row.Scan(&m.turn, &m.finding)
		return m, err
	}, `SELECT turn, finding FROM set_findings
		WHERE conversation = ? AND (? IS NULL OR turn = ?)
		ORDER BY turn, finding`, conv, only, only)
	if err != nil {
		return nil, err
	}
	at := make(map[int64]int, len(sets))
	for i, s := range sets {
		at[s.Turn] = i
	}
	for _, m := range members {
		i := at[m.turn]
		sets[i].Findings = append(sets[i].Findings, m.finding)
	}
	return sets, nil
}

// readGroupings reads the groupings of conv in turn order. A non-nil key reads only that turn.
func readGroupings(ctx context.Context, tx *sql.Tx, conv string, key *int64) ([]Grouping, error) {
	only := nullSeq(key)
	gs, err := queryRows(ctx, tx, func(row rowScanner) (Grouping, error) {
		g := Grouping{Drops: []Drop{}}
		err := row.Scan(&g.Turn, &g.After)
		return g, err
	}, `SELECT turn, after FROM groupings
		WHERE conversation = ? AND (? IS NULL OR turn = ?)
		ORDER BY turn`, conv, only, only)
	if err != nil {
		return nil, err
	}
	type drop struct {
		turn int64
		Drop
	}
	drops, err := queryRows(ctx, tx, func(row rowScanner) (drop, error) {
		var d drop
		err := row.Scan(&d.turn, &d.Finding, &d.Kept)
		return d, err
	}, `SELECT turn, finding, kept FROM drops
		WHERE conversation = ? AND (? IS NULL OR turn = ?)
		ORDER BY turn, finding`, conv, only, only)
	if err != nil {
		return nil, err
	}
	at := make(map[int64]int, len(gs))
	for i, g := range gs {
		at[g.Turn] = i
	}
	for _, d := range drops {
		i := at[d.turn]
		gs[i].Drops = append(gs[i].Drops, d.Drop)
	}
	return gs, nil
}

// readSettles reads the settles of conv in finding order. A non-nil key reads only that finding.
func readSettles(ctx context.Context, tx *sql.Tx, conv string, key *int64) ([]Settle, error) {
	only := nullSeq(key)
	return queryRows(ctx, tx, func(row rowScanner) (Settle, error) {
		var s Settle
		err := row.Scan(&s.Finding, &s.Proposal, &s.ProposalKind, &s.After)
		return s, err
	}, `SELECT finding, proposal, proposal_kind, after FROM settles
		WHERE conversation = ? AND (? IS NULL OR finding = ?)
		ORDER BY finding`, conv, only, only)
}

// readFixes reads the fixes of conv in finding order. A non-nil key reads only that finding.
func readFixes(ctx context.Context, tx *sql.Tx, conv string, key *int64) ([]Fix, error) {
	only := nullSeq(key)
	return queryRows(ctx, tx, func(row rowScanner) (Fix, error) {
		var f Fix
		err := row.Scan(&f.Finding, &f.SettledAfter, &f.Fixed)
		return f, err
	}, `SELECT finding, settled_after, fixed FROM fixes
		WHERE conversation = ? AND (? IS NULL OR finding = ?)
		ORDER BY finding`, conv, only, only)
}

func compareDrops(a, b Drop) int {
	if c := cmp.Compare(a.Finding, b.Finding); c != 0 {
		return c
	}
	return cmp.Compare(a.Kept, b.Kept)
}
