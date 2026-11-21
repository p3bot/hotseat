// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
)

// LaunchMember inserts one row for a roster name.
// A second launch of that name returns ErrLaunched and writes nothing.
// A name outside the roster returns ErrNotRoster and writes nothing.
// A missing conversation returns ErrNotFound and writes nothing.
// pid and its start token are stored only when hasPID is set, and an empty start stores none. The registered time stays empty.
func (s *Store) LaunchMember(ctx context.Context, conv, name, now string, pid int64, hasPID bool, start string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		_, found, err := memberByName(ctx, tx, conv, name)
		if err != nil {
			return err
		}
		if found {
			return ErrLaunched
		}
		var pidVal, startVal any
		if hasPID {
			pidVal = pid
			startVal = nullable(start)
		} else {
			pid, start = 0, ""
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO members (
				conversation, name, launched, registered, status, session, pid, start
			) VALUES (?, ?, ?, '', ?, '', ?, ?)`,
			conv, name, now, MemberLaunched, pidVal, startVal)
		if err != nil {
			return err
		}
		out = Member{Name: name, Status: MemberLaunched, Launched: now, PID: pid, HasPID: hasPID, Start: start}
		return nil
	})
	if err != nil {
		return Member{}, err
	}
	return out, nil
}

// SetMemberStatus updates an existing row to running, exited, or timed-out.
// pid and start replace the stored pid and start token when hasPID is set, and are left unchanged otherwise.
// An empty start with a pid clears the stored token, because it belonged to the old pid.
// A missing row returns ErrNotMember and writes nothing.
func (s *Store) SetMemberStatus(ctx context.Context, conv, name, status string, pid int64, hasPID bool, start string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, conv, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		if hasPID {
			_, err = tx.ExecContext(ctx, `
				UPDATE members SET status = ?, pid = ?, start = ?
				WHERE conversation = ? AND name = ?`,
				status, pid, nullable(start), conv, name)
			cur.PID = pid
			cur.HasPID = true
			cur.Start = start
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE members SET status = ?
				WHERE conversation = ? AND name = ?`,
				status, conv, name)
		}
		if err != nil {
			return err
		}
		cur.Status = status
		out = cur
		return nil
	})
	if err != nil {
		return Member{}, err
	}
	return out, nil
}

// RegisterMember sets the registered time from now.
// Status becomes registered only when the row is launched. Any other status stays.
// A missing row returns ErrNotMember and writes nothing.
func (s *Store) RegisterMember(ctx context.Context, conv, name, now string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, conv, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		status := cur.Status
		if status == MemberLaunched {
			status = MemberRegistered
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE members SET registered = ?, status = ?
			WHERE conversation = ? AND name = ?`,
			now, status, conv, name)
		if err != nil {
			return err
		}
		cur.Registered = now
		cur.Status = status
		out = cur
		return nil
	})
	if err != nil {
		return Member{}, err
	}
	return out, nil
}

// Member returns one row. The session id is not loaded.
// A missing row returns ErrNotMember.
func (s *Store) Member(ctx context.Context, conv, name string) (Member, error) {
	var out Member
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, conv, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		out = cur
		return nil
	})
	if err != nil {
		return Member{}, err
	}
	return out, nil
}

// Members returns the rows of one conversation in roster order.
// A roster name with no row is omitted. Session ids are not loaded.
// A missing conversation returns ErrNotFound.
func (s *Store) Members(ctx context.Context, conv string) ([]Member, error) {
	var out []Member
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		roster, err := rosterOf(ctx, tx, conv)
		if err != nil {
			return err
		}
		out, err = queryRows(ctx, tx, scanMember, `
			SELECT name, launched, registered, status, pid, start
			FROM members WHERE conversation = ?`, conv)
		if err != nil {
			return err
		}
		out = orderByRoster(roster, out, func(m Member) string { return m.Name })
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Member{}
	}
	return out, nil
}

// SetSession stores or replaces the session id for a member row.
// A missing row returns ErrNotMember and writes nothing.
func (s *Store) SetSession(ctx context.Context, conv, name, session string) (MemberSession, error) {
	var out MemberSession
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		_, found, err := memberByName(ctx, tx, conv, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE members SET session = ?
			WHERE conversation = ? AND name = ?`,
			session, conv, name)
		if err != nil {
			return err
		}
		out = MemberSession{Name: name, Session: session}
		return nil
	})
	if err != nil {
		return MemberSession{}, err
	}
	return out, nil
}

// Session returns the stored session id. An unset id is an empty string.
// A missing row returns ErrNotMember.
func (s *Store) Session(ctx context.Context, conv, name string) (MemberSession, error) {
	var out MemberSession
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, conv, name); err != nil {
			return err
		}
		var session string
		err := tx.QueryRowContext(ctx, `
			SELECT session FROM members WHERE conversation = ? AND name = ?`,
			conv, name).Scan(&session)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotMember
		}
		if err != nil {
			return err
		}
		out = MemberSession{Name: name, Session: session}
		return nil
	})
	if err != nil {
		return MemberSession{}, err
	}
	return out, nil
}

// Sessions returns every member name and session id in roster order.
// A missing conversation returns ErrNotFound.
func (s *Store) Sessions(ctx context.Context, conv string) ([]MemberSession, error) {
	var out []MemberSession
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		roster, err := rosterOf(ctx, tx, conv)
		if err != nil {
			return err
		}
		list, err := queryRows(ctx, tx, func(row rowScanner) (MemberSession, error) {
			var item MemberSession
			err := row.Scan(&item.Name, &item.Session)
			return item, err
		}, `SELECT name, session FROM members WHERE conversation = ?`, conv)
		if err != nil {
			return err
		}
		out = orderByRoster(roster, list, func(item MemberSession) string { return item.Name })
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []MemberSession{}
	}
	return out, nil
}

func requireRosterName(ctx context.Context, tx *sql.Tx, conv, name string) error {
	roster, err := rosterOf(ctx, tx, conv)
	if err != nil {
		return err
	}
	for _, n := range roster {
		if n == name {
			return nil
		}
	}
	return ErrNotRoster
}

func rosterOf(ctx context.Context, tx *sql.Tx, conv string) ([]string, error) {
	row, err := conversationByName(ctx, tx, conv)
	if err != nil {
		return nil, err
	}
	return row.Roster, nil
}

func memberByName(ctx context.Context, tx *sql.Tx, conv, name string) (Member, bool, error) {
	m, err := scanMember(tx.QueryRowContext(ctx, `
		SELECT name, launched, registered, status, pid, start
		FROM members WHERE conversation = ? AND name = ?`,
		conv, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, false, nil
	}
	if err != nil {
		return Member{}, false, err
	}
	return m, true, nil
}

// scanMember reads name, launched, registered, status, pid, and start.
func scanMember(row rowScanner) (Member, error) {
	var m Member
	var pid sql.NullInt64
	var start sql.NullString
	if err := row.Scan(&m.Name, &m.Launched, &m.Registered, &m.Status, &pid, &start); err != nil {
		return Member{}, err
	}
	if pid.Valid {
		m.PID = pid.Int64
		m.HasPID = true
	}
	m.Start = start.String
	return m, nil
}

// nullable stores an empty string as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orderByRoster[T any](roster []string, rows []T, name func(T) string) []T {
	pos := make(map[string]int, len(roster))
	for i, n := range roster {
		pos[n] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rosterIndex(pos, name(rows[i])) < rosterIndex(pos, name(rows[j]))
	})
	return rows
}

func rosterIndex(pos map[string]int, name string) int {
	if i, ok := pos[name]; ok {
		return i
	}
	return len(pos)
}
