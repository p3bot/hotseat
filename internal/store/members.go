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
// A missing chat returns ErrNotFound and writes nothing.
// pid and its start token are stored only when hasPID is set, and an empty start stores none. The joined time stays empty.
func (s *Store) LaunchMember(ctx context.Context, chat, name, now string, pid int64, hasPID bool, start string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		_, found, err := memberByName(ctx, tx, chat, name)
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
				chat, name, launched, joined, status, session, pid, start
			) VALUES (?, ?, ?, '', ?, '', ?, ?)`,
			chat, name, now, MemberLaunched, pidVal, startVal)
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
func (s *Store) SetMemberStatus(ctx context.Context, chat, name, status string, pid int64, hasPID bool, start string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, chat, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		if hasPID {
			_, err = tx.ExecContext(ctx, `
				UPDATE members SET status = ?, pid = ?, start = ?
				WHERE chat = ? AND name = ?`,
				status, pid, nullable(start), chat, name)
			cur.PID = pid
			cur.HasPID = true
			cur.Start = start
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE members SET status = ?
				WHERE chat = ? AND name = ?`,
				status, chat, name)
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

// JoinMember sets the joined time from now.
// Status becomes joined only when the row is launched. Any other status stays.
// A missing row returns ErrNotMember and writes nothing.
func (s *Store) JoinMember(ctx context.Context, chat, name, now string) (Member, error) {
	var out Member
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, chat, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		status := cur.Status
		if status == MemberLaunched {
			status = MemberJoined
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE members SET joined = ?, status = ?
			WHERE chat = ? AND name = ?`,
			now, status, chat, name)
		if err != nil {
			return err
		}
		cur.Joined = now
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
func (s *Store) Member(ctx context.Context, chat, name string) (Member, error) {
	var out Member
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		cur, found, err := memberByName(ctx, tx, chat, name)
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

// Members returns the rows of one chat in roster order.
// A roster name with no row is omitted. Session ids are not loaded.
// A missing chat returns ErrNotFound.
func (s *Store) Members(ctx context.Context, chat string) ([]Member, error) {
	var out []Member
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		roster, err := rosterOf(ctx, tx, chat)
		if err != nil {
			return err
		}
		out, err = queryRows(ctx, tx, scanMember, `
			SELECT name, launched, joined, status, pid, start
			FROM members WHERE chat = ?`, chat)
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
func (s *Store) SetSession(ctx context.Context, chat, name, session string) (MemberSession, error) {
	var out MemberSession
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		_, found, err := memberByName(ctx, tx, chat, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotMember
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE members SET session = ?
			WHERE chat = ? AND name = ?`,
			session, chat, name)
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
func (s *Store) Session(ctx context.Context, chat, name string) (MemberSession, error) {
	var out MemberSession
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		if err := requireRosterName(ctx, tx, chat, name); err != nil {
			return err
		}
		var session string
		err := tx.QueryRowContext(ctx, `
			SELECT session FROM members WHERE chat = ? AND name = ?`,
			chat, name).Scan(&session)
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
// A missing chat returns ErrNotFound.
func (s *Store) Sessions(ctx context.Context, chat string) ([]MemberSession, error) {
	var out []MemberSession
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		roster, err := rosterOf(ctx, tx, chat)
		if err != nil {
			return err
		}
		list, err := queryRows(ctx, tx, func(row rowScanner) (MemberSession, error) {
			var item MemberSession
			err := row.Scan(&item.Name, &item.Session)
			return item, err
		}, `SELECT name, session FROM members WHERE chat = ?`, chat)
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

func requireRosterName(ctx context.Context, tx *sql.Tx, chat, name string) error {
	roster, err := rosterOf(ctx, tx, chat)
	if err != nil {
		return err
	}
	for _, r := range roster {
		if r.Name == name {
			return nil
		}
	}
	return ErrNotRoster
}

func rosterOf(ctx context.Context, tx *sql.Tx, chat string) ([]RosterEntry, error) {
	row, err := chatByName(ctx, tx, chat)
	if err != nil {
		return nil, err
	}
	return row.Roster, nil
}

func memberByName(ctx context.Context, tx *sql.Tx, chat, name string) (Member, bool, error) {
	m, err := scanMember(tx.QueryRowContext(ctx, `
		SELECT name, launched, joined, status, pid, start
		FROM members WHERE chat = ? AND name = ?`,
		chat, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, false, nil
	}
	if err != nil {
		return Member{}, false, err
	}
	return m, true, nil
}

// scanMember reads name, launched, joined, status, pid, and start.
func scanMember(row rowScanner) (Member, error) {
	var m Member
	var pid sql.NullInt64
	var start sql.NullString
	if err := row.Scan(&m.Name, &m.Launched, &m.Joined, &m.Status, &pid, &start); err != nil {
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

func orderByRoster[T any](roster []RosterEntry, rows []T, name func(T) string) []T {
	pos := make(map[string]int, len(roster))
	for i, r := range roster {
		pos[r.Name] = i
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
