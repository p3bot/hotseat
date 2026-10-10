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
// pid is stored only when hasPID is set. The registered time stays empty.
func (s *Store) LaunchMember(ctx context.Context, conv, name, now string, pid int64, hasPID bool) (Member, error) {
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
		var pidVal any
		if hasPID {
			pidVal = pid
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO members (
				conversation, name, launched, registered, status, session, pid
			) VALUES (?, ?, ?, '', ?, '', ?)`,
			conv, name, now, MemberLaunched, pidVal)
		if err != nil {
			return err
		}
		out = Member{Name: name, Status: MemberLaunched, Launched: now, PID: pid, HasPID: hasPID}
		return nil
	})
	if err != nil {
		return Member{}, err
	}
	return out, nil
}

// SetMemberStatus updates an existing row to running, exited, or timed-out.
// pid replaces the stored pid when hasPID is set, and is left unchanged otherwise.
// A missing row returns ErrNotMember and writes nothing.
func (s *Store) SetMemberStatus(ctx context.Context, conv, name, status string, pid int64, hasPID bool) (Member, error) {
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
				UPDATE members SET status = ?, pid = ?
				WHERE conversation = ? AND name = ?`,
				status, pid, conv, name)
			cur.PID = pid
			cur.HasPID = true
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
		rows, err := tx.QueryContext(ctx, `
			SELECT name, launched, registered, status, pid
			FROM members WHERE conversation = ?`, conv)
		if err != nil {
			return err
		}
		out, err = scanMembers(rows)
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
		rows, err := tx.QueryContext(ctx, `
			SELECT name, session FROM members WHERE conversation = ?`, conv)
		if err != nil {
			return err
		}
		list := make([]MemberSession, 0)
		for rows.Next() {
			var item MemberSession
			if err := rows.Scan(&item.Name, &item.Session); err != nil {
				return joinClose(err, rows.Close())
			}
			list = append(list, item)
		}
		if err := rows.Err(); err != nil {
			return joinClose(err, rows.Close())
		}
		if err := rows.Close(); err != nil {
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
	var m Member
	var pid sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT name, launched, registered, status, pid
		FROM members WHERE conversation = ? AND name = ?`,
		conv, name).Scan(&m.Name, &m.Launched, &m.Registered, &m.Status, &pid)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, false, nil
	}
	if err != nil {
		return Member{}, false, err
	}
	if pid.Valid {
		m.PID = pid.Int64
		m.HasPID = true
	}
	return m, true, nil
}

func scanMembers(rows *sql.Rows) ([]Member, error) {
	out := make([]Member, 0)
	for rows.Next() {
		var m Member
		var pid sql.NullInt64
		if err := rows.Scan(&m.Name, &m.Launched, &m.Registered, &m.Status, &pid); err != nil {
			return nil, joinClose(err, rows.Close())
		}
		if pid.Valid {
			m.PID = pid.Int64
			m.HasPID = true
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, joinClose(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
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
