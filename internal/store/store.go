// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package store is the bus SQLite database.
// One process holds hotseat.db.lock. Cursors and blocked waits are not stored.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	sqlite "modernc.org/sqlite"
)

// FileName is the database file inside the operator's store directory.
const FileName = "hotseat.db"

// LockName is the process lock beside the database.
// It is a different file so the lock does not share an inode with SQLite.
const LockName = FileName + ".lock"

// SchemaVersion is the schema this process serves.
const SchemaVersion = "1"

const (
	// StatusOpen is a conversation that accepts publishes.
	StatusOpen = "open"
	// StatusClosed is a recorded state. Publish still appends. The transcript stays readable.
	StatusClosed = "closed"
)

var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT`,
	`CREATE TABLE IF NOT EXISTS conversations (
  name TEXT PRIMARY KEY,
  status TEXT NOT NULL CHECK (status IN ('open', 'closed'))
) STRICT`,
	`CREATE TABLE IF NOT EXISTS messages (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  seq INTEGER NOT NULL CHECK (seq >= 1),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  PRIMARY KEY (conversation, seq),
  UNIQUE (conversation, sender, txid)
) STRICT`,
}

var (
	// ErrNotFound means the conversation does not exist.
	ErrNotFound = errors.New("conversation not found")
	// ErrConflict means the transaction id is already stored with different content.
	ErrConflict = errors.New("transaction id reused with different content")
	// ErrHeld means another process already has the database.
	ErrHeld = errors.New("database is held by another process")
	// ErrSchema means the file was written by a different store version.
	ErrSchema = errors.New("store schema is not supported by this binary")

	// errNoChange tells withTx to roll the transaction back and return nil.
	// Idempotent publish and close of an already-closed conversation take it
	// so a reread does not commit an empty write.
	errNoChange = errors.New("store transaction made no change")
)

// Conversation is a name and its status.
type Conversation struct {
	Name   string
	Status string
}

// Message is one accepted publish. To keeps the caller's order.
type Message struct {
	Seq  int64
	Time string
	From string
	To   []string
	Body string
	TxID string
}

// Publish is a validated publish to commit.
type Publish struct {
	Conversation string
	From         string
	To           []string
	Body         string
	TxID         string
	Time         string
}

// Store is the open database. Close releases it for another process.
type Store struct {
	mu   sync.Mutex
	db   *sql.DB
	lock *os.File
	path string
}

// Open creates or opens dir/hotseat.db and holds dir/hotseat.db.lock until Close.
// A relative directory is resolved before any file is created, so the database
// location does not follow a later change of working directory.
// The directory is owner-only. A symlink for the database, its write-ahead log,
// its shared-memory file, or the lock file is refused and left unchanged.
// A second process gets ErrHeld and does not become a writer.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store directory is required")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("store directory: %w", err)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create store directory: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("store directory: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("store path is not a directory: %s", dir)
	}
	// Owner-only before any database file is created, including a directory
	// that already existed with a looser mode. Another user then cannot plant
	// a symlink for the open below to follow.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("set store directory permissions: %w", err)
	}

	path := filepath.Join(dir, FileName)
	lockPath := filepath.Join(dir, LockName)
	for _, p := range append(storeFiles(path), lockPath) {
		if err := rejectSymlink(p); err != nil {
			return nil, err
		}
	}
	// Separate inode from the database. On macOS, flock shares SQLite's lock
	// table, and closing any descriptor drops every lock on that file.
	// O_NOFOLLOW closes the gap between the symlink check and this open.
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, symlinkErr(lockPath)
		}
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		cerr := lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			if cerr != nil {
				return nil, fmt.Errorf("%w: %s; close: %v", ErrHeld, lockPath, cerr)
			}
			return nil, fmt.Errorf("%w: %s", ErrHeld, lockPath)
		}
		if cerr != nil {
			return nil, fmt.Errorf("lock store: %w; close: %v", err, cerr)
		}
		return nil, fmt.Errorf("lock store: %w", err)
	}
	if err := unix.Fchmod(int(lock.Fd()), 0o600); err != nil {
		rerr := releaseLock(lock)
		if rerr != nil {
			return nil, fmt.Errorf("set lock file permissions: %w; unlock: %v", err, rerr)
		}
		return nil, fmt.Errorf("set lock file permissions: %w", err)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		rerr := releaseLock(lock)
		if rerr != nil {
			return nil, fmt.Errorf("open database: %w; unlock: %v", err, rerr)
		}
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, lock: lock, path: path}
	if err := db.Ping(); err != nil {
		return nil, s.openFailed(path, err)
	}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, s.openFailed(path, err)
	}
	if err := s.holdExclusiveLock(); err != nil {
		return nil, s.openFailed(path, err)
	}
	if err := tighten(path); err != nil {
		return nil, s.openFailed(path, err)
	}
	return s, nil
}

func (s *Store) openFailed(path string, err error) error {
	rerr := s.Close()
	if isBusy(err) {
		err = fmt.Errorf("%w: %s", ErrHeld, path)
	} else if !errors.Is(err, ErrSchema) {
		err = fmt.Errorf("open database: %w", err)
	}
	if rerr != nil {
		return fmt.Errorf("%w; close: %v", err, rerr)
	}
	return err
}

// Path returns the database file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Schema reads the schema version recorded in meta.
// The read uses the open connection and writes nothing.
func (s *Store) Schema(ctx context.Context) (string, error) {
	if s == nil {
		return "", errors.New("store is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", errors.New("store is closed")
	}
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema'`).Scan(&version)
	if err != nil {
		return "", err
	}
	return version, nil
}

// Close releases the database and the process lock. It is safe to call twice.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	if s.lock != nil {
		if rerr := releaseLock(s.lock); err == nil {
			err = rerr
		}
		s.lock = nil
	}
	return err
}

// Create inserts an open conversation.
// A name already present returns that conversation and already true, and writes nothing.
func (s *Store) Create(ctx context.Context, name string) (Conversation, bool, error) {
	var conv Conversation
	var already bool
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO conversations (name, status) VALUES (?, ?)`,
			name, StatusOpen)
		if err == nil {
			conv = Conversation{Name: name, Status: StatusOpen}
			return nil
		}
		if !isConstraint(err) {
			return err
		}
		err = tx.QueryRowContext(ctx,
			`SELECT name, status FROM conversations WHERE name = ?`, name).Scan(&conv.Name, &conv.Status)
		if err != nil {
			return err
		}
		already = true
		return errNoChange
	})
	if err != nil {
		return Conversation{}, false, err
	}
	return conv, already, nil
}

// CloseConversation sets status to closed and appends nothing.
// Closing a conversation that is already closed writes nothing and returns nil.
func (s *Store) CloseConversation(ctx context.Context, name string) error {
	return s.withTx(ctx, true, func(tx *sql.Tx) error {
		var status string
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM conversations WHERE name = ?`, name).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == StatusClosed {
			return errNoChange
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE conversations SET status = ? WHERE name = ?`,
			StatusClosed, name)
		return err
	})
}

// List returns every conversation, open and closed, ordered by name.
func (s *Store) List(ctx context.Context) ([]Conversation, error) {
	var out []Conversation
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT name, status FROM conversations ORDER BY name`)
		if err != nil {
			return err
		}
		out, err = scanConversations(rows)
		return err
	})
	return out, err
}

// Publish commits one message, or returns the original when this sender
// already stored this txid with the same to and body.
// The same sender and txid with different to or body returns ErrConflict.
// Another sender using that txid is a different attempt.
// A matching attempt returns the original message and writes nothing.
// The status is the conversation state committed with that result.
// A missing conversation returns ErrNotFound and writes nothing.
func (s *Store) Publish(ctx context.Context, in Publish) (Message, string, bool, error) {
	var msg Message
	var status string
	var already bool
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM conversations WHERE name = ?`, in.Conversation).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		stored, found, err := messageByAttempt(ctx, tx, in.Conversation, in.From, in.TxID)
		if err != nil {
			return err
		}
		if found {
			if stored.From == in.From && stored.Body == in.Body && sameTo(stored.To, in.To) {
				msg = stored
				already = true
				return errNoChange
			}
			return ErrConflict
		}

		var seq int64
		err = tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE conversation = ?`,
			in.Conversation).Scan(&seq)
		if err != nil {
			return err
		}
		rec, err := marshalTo(in.To)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO messages (
				conversation, seq, time, sender, recipients, body, txid
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.Conversation, seq, in.Time, in.From, rec, in.Body, in.TxID)
		if err != nil {
			return err
		}
		msg = Message{
			Seq:  seq,
			Time: in.Time,
			From: in.From,
			To:   copyTo(in.To),
			Body: in.Body,
			TxID: in.TxID,
		}
		return nil
	})
	if err != nil {
		return Message{}, "", false, err
	}
	return msg, status, already, nil
}

// Transcript returns the conversation status and every message with seq greater than after.
// The messages are oldest first. A missing conversation returns ErrNotFound.
func (s *Store) Transcript(ctx context.Context, name string, after int64) (string, []Message, error) {
	msgs := make([]Message, 0)
	status, err := s.Scan(ctx, name, after, func(m Message) bool {
		msgs = append(msgs, m)
		return true
	})
	if err != nil {
		return "", nil, err
	}
	return status, msgs, nil
}

// Scan calls fn for each message with seq greater than after, oldest first.
// fn returns false to stop. Messages after the stop are not read.
// A missing conversation returns ErrNotFound.
func (s *Store) Scan(ctx context.Context, name string, after int64, fn func(Message) bool) (string, error) {
	if fn == nil {
		return "", errors.New("scan function is required")
	}
	var status string
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM conversations WHERE name = ?`, name).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT seq, time, sender, recipients, body, txid
			FROM messages
			WHERE conversation = ? AND seq > ?
			ORDER BY seq`, name, after)
		if err != nil {
			return err
		}
		for rows.Next() {
			m, err := scanOne(rows)
			if err != nil {
				return joinClose(err, rows.Close())
			}
			if !fn(m) {
				return rows.Close()
			}
		}
		if err := rows.Err(); err != nil {
			return joinClose(err, rows.Close())
		}
		return rows.Close()
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

func (s *Store) ensureSchema(ctx context.Context) error {
	return s.withTx(ctx, true, func(tx *sql.Tx) error {
		for _, stmt := range schemaStmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("create schema: %w", err)
			}
		}
		var version string
		err := tx.QueryRowContext(ctx,
			`SELECT value FROM meta WHERE key = 'schema'`).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO meta (key, value) VALUES ('schema', ?)`, SchemaVersion)
			if err != nil {
				return fmt.Errorf("record schema version: %w", err)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if version != SchemaVersion {
			return fmt.Errorf("%w: %s", ErrSchema, version)
		}
		return nil
	})
}

func (s *Store) withTx(ctx context.Context, write bool, fn func(*sql.Tx) error) error {
	if s == nil {
		return errors.New("store is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return errors.New("store is closed")
	}
	var opts *sql.TxOptions
	if !write {
		opts = &sql.TxOptions{ReadOnly: true}
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		rerr := tx.Rollback()
		if errors.Is(err, errNoChange) {
			if rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
				return rerr
			}
			return nil
		}
		if rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			return fmt.Errorf("%w; rollback: %v", err, rerr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			return fmt.Errorf("%w; rollback: %v", err, rerr)
		}
		return err
	}
	return nil
}

func scanConversations(rows *sql.Rows) ([]Conversation, error) {
	out := make([]Conversation, 0)
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.Name, &c.Status); err != nil {
			return nil, joinClose(err, rows.Close())
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, joinClose(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanOne(rows *sql.Rows) (Message, error) {
	var m Message
	var rec string
	if err := rows.Scan(&m.Seq, &m.Time, &m.From, &rec, &m.Body, &m.TxID); err != nil {
		return Message{}, err
	}
	to, err := unmarshalTo(rec)
	if err != nil {
		return Message{}, fmt.Errorf("message %d recipients: %w", m.Seq, err)
	}
	m.To = to
	return m, nil
}

func joinClose(err, cerr error) error {
	if cerr == nil {
		return err
	}
	if err == nil {
		return cerr
	}
	return fmt.Errorf("%w; close rows: %v", err, cerr)
}

func messageByAttempt(ctx context.Context, tx *sql.Tx, conversation, sender, txid string) (Message, bool, error) {
	var m Message
	var rec string
	err := tx.QueryRowContext(ctx, `
		SELECT seq, time, sender, recipients, body, txid
		FROM messages
		WHERE conversation = ? AND sender = ? AND txid = ?`,
		conversation, sender, txid).Scan(&m.Seq, &m.Time, &m.From, &rec, &m.Body, &m.TxID)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	to, err := unmarshalTo(rec)
	if err != nil {
		return Message{}, false, err
	}
	m.To = to
	return m, true, nil
}

// holdExclusiveLock fails open unless SQLite kept its exclusive lock.
// The process flock is hotseat.db.lock, so it does not share the database inode.
func (s *Store) holdExclusiveLock() error {
	var mode string
	if err := s.db.QueryRow(`PRAGMA locking_mode`).Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "exclusive") {
		return fmt.Errorf("locking_mode = %s", mode)
	}
	return nil
}

func dsn(path string) string {
	// path is absolute. A relative path is encoded as file://<first-segment>/...
	// and SQLite rejects that first segment as a URI authority.
	// busy_timeout 0: another holder's lock fails this process instead of stalling it.
	// locking_mode EXCLUSIVE: this connection keeps the SQLite lock until close.
	// synchronous FULL: Commit returns only after the write is durable.
	u := url.URL{Scheme: "file", Path: path}
	return u.String() +
		"?_txlock=immediate" +
		"&_pragma=busy_timeout(0)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=locking_mode(EXCLUSIVE)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(FULL)"
}

func releaseLock(f *os.File) error {
	if f == nil {
		return nil
	}
	err := unix.Flock(int(f.Fd()), unix.LOCK_UN)
	cerr := f.Close()
	if err != nil {
		return err
	}
	return cerr
}

func storeFiles(path string) []string {
	return []string{path, path + "-wal", path + "-shm"}
}

func symlinkErr(path string) error {
	return fmt.Errorf("store file is a symlink: %s", path)
}

// rejectSymlink reports a symlink and does not change it.
// chmod on a path follows a symlink, so callers must not chmod after this fails.
func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return symlinkErr(path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("store file is not a regular file: %s", path)
	}
	return nil
}

// ownFile sets owner read-write on an existing regular file.
// A missing path is ignored. A symlink is an error and is not changed.
// The mode change uses chmod rather than opening the file. SQLite's locks are
// POSIX record locks, and closing any descriptor for that file drops them for
// the whole process. The directory is already owner-only, so another user
// cannot swap in a symlink between the check and the chmod.
func ownFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return symlinkErr(path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("store file is not a regular file: %s", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

func tighten(path string) error {
	for _, p := range storeFiles(path) {
		if err := ownFile(p); err != nil {
			return err
		}
	}
	return nil
}

func sqlitePrimary(err error) int {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return 0
	}
	return se.Code() & 0xff
}

func isConstraint(err error) bool {
	return sqlitePrimary(err) == 19
}

func isBusy(err error) bool {
	switch sqlitePrimary(err) {
	case 5, 6:
		return true
	default:
		return false
	}
}
