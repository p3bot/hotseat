// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package store is the bus SQLite database.
// One process holds hotseat.db.lock. Cursors and blocked waits are not stored.
// A message carries kind and an optional re, the seq of an earlier message of
// the same conversation it responds to. A conversation row holds the binding.
// A member row holds registration, status, pid, and session id for one roster name.
//
// Decision records are hotseat run's insert-only facts about messages: a set
// closed with its findings, a set's grouping of duplicates, a settle, a fix,
// and a halt. Each references messages of its conversation by composite
// foreign keys that also fix the referenced kind (turn, finding, propose,
// dismiss, fixed). after on a record is the conversation's highest seq when
// it commits, read in the write transaction. A write that matches the stored
// record on its key, ignoring after, returns it and writes nothing. One that
// differs returns ErrConflict. Every other constraint failure on a record
// table is ErrRecordReference, and the three rules that span rows are
// ErrKeptDropped, ErrNotKept, and ErrFixDismissed.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

// Member status values stored on a roster row.
const (
	MemberLaunched   = "launched"
	MemberRegistered = "registered"
	MemberRunning    = "running"
	MemberExited     = "exited"
	MemberTimedOut   = "timed-out"
)

const conversationColumns = `name, status, task, ticket, seat, directory, prompts, custom, roster`

const messageColumns = `seq, time, sender, recipients, body, txid, kind, re`

var schemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT`,
	`CREATE TABLE IF NOT EXISTS conversations (
  name TEXT PRIMARY KEY,
  status TEXT NOT NULL CHECK (status IN ('open', 'closed')),
  task TEXT NOT NULL,
  ticket TEXT NOT NULL,
  seat TEXT NOT NULL,
  directory TEXT NOT NULL,
  prompts TEXT NOT NULL,
  custom TEXT NOT NULL,
  roster TEXT NOT NULL
) STRICT`,
	`CREATE TABLE IF NOT EXISTS messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation TEXT NOT NULL REFERENCES conversations(name),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  kind TEXT NOT NULL,
  re INTEGER CHECK (re < seq),
  UNIQUE (conversation, sender, txid),
  UNIQUE (conversation, seq),
  UNIQUE (conversation, seq, kind),
  FOREIGN KEY (conversation, re) REFERENCES messages (conversation, seq)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS members (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  name TEXT NOT NULL,
  launched TEXT NOT NULL,
  registered TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('launched', 'registered', 'running', 'exited', 'timed-out')),
  session TEXT NOT NULL,
  pid INTEGER,
  PRIMARY KEY (conversation, name)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS sets (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  turn INTEGER NOT NULL,
  turn_kind TEXT NOT NULL DEFAULT 'turn' CHECK (turn_kind = 'turn'),
  after INTEGER NOT NULL,
  PRIMARY KEY (conversation, turn),
  FOREIGN KEY (conversation, turn, turn_kind) REFERENCES messages (conversation, seq, kind),
  CHECK (after >= turn)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS set_findings (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  turn INTEGER NOT NULL,
  finding INTEGER NOT NULL,
  finding_kind TEXT NOT NULL DEFAULT 'finding' CHECK (finding_kind = 'finding'),
  PRIMARY KEY (conversation, finding),
  UNIQUE (conversation, turn, finding),
  FOREIGN KEY (conversation, turn) REFERENCES sets (conversation, turn),
  FOREIGN KEY (conversation, finding, finding_kind) REFERENCES messages (conversation, seq, kind)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS groupings (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  turn INTEGER NOT NULL,
  after INTEGER NOT NULL,
  PRIMARY KEY (conversation, turn),
  FOREIGN KEY (conversation, turn) REFERENCES sets (conversation, turn),
  CHECK (after >= turn)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS drops (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  turn INTEGER NOT NULL,
  finding INTEGER NOT NULL,
  kept INTEGER NOT NULL,
  PRIMARY KEY (conversation, finding),
  FOREIGN KEY (conversation, turn) REFERENCES groupings (conversation, turn),
  FOREIGN KEY (conversation, turn, finding) REFERENCES set_findings (conversation, turn, finding),
  FOREIGN KEY (conversation, turn, kept) REFERENCES set_findings (conversation, turn, finding),
  CHECK (finding <> kept)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS settles (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  finding INTEGER NOT NULL,
  proposal INTEGER NOT NULL,
  proposal_kind TEXT NOT NULL CHECK (proposal_kind IN ('finding', 'propose', 'dismiss')),
  after INTEGER NOT NULL,
  PRIMARY KEY (conversation, finding),
  UNIQUE (conversation, finding, after),
  FOREIGN KEY (conversation, finding) REFERENCES set_findings (conversation, finding),
  FOREIGN KEY (conversation, proposal, proposal_kind) REFERENCES messages (conversation, seq, kind),
  CHECK (proposal >= finding AND after >= proposal),
  CHECK (proposal_kind <> 'finding' OR proposal = finding)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS fixes (
  conversation TEXT NOT NULL REFERENCES conversations(name),
  finding INTEGER NOT NULL,
  settled_after INTEGER NOT NULL,
  fixed INTEGER NOT NULL,
  fixed_kind TEXT NOT NULL DEFAULT 'fixed' CHECK (fixed_kind = 'fixed'),
  PRIMARY KEY (conversation, finding),
  UNIQUE (conversation, fixed),
  FOREIGN KEY (conversation, finding, settled_after) REFERENCES settles (conversation, finding, after),
  FOREIGN KEY (conversation, fixed, fixed_kind) REFERENCES messages (conversation, seq, kind),
  CHECK (fixed > settled_after)
) STRICT`,
	`CREATE TABLE IF NOT EXISTS halts (
  conversation TEXT NOT NULL PRIMARY KEY REFERENCES conversations(name),
  finding INTEGER NOT NULL,
  after INTEGER NOT NULL,
  FOREIGN KEY (conversation, finding) REFERENCES set_findings (conversation, finding),
  CHECK (after >= finding)
) STRICT`,
}

// recordTables are the decision record tables. A version-1 file lacking any of them is ErrSchema.
var recordTables = []string{"sets", "set_findings", "groupings", "drops", "settles", "fixes", "halts"}

var (
	// ErrNotFound means the conversation does not exist.
	ErrNotFound = errors.New("conversation not found")
	// ErrConflict means the transaction id is already stored with different content.
	ErrConflict = errors.New("transaction id reused with different content")
	// ErrHeld means another process already has the database.
	ErrHeld = errors.New("database is held by another process")
	// ErrSchema means this binary will not serve the file.
	// The recorded version differs, or a version-1 file lacks kind, re, the binding columns, the member table,
	// any decision record table (sets, set_findings, groupings, drops, settles, fixes, halts),
	// or the unique keys (conversation, seq) and (conversation, seq, kind) on messages,
	// or its messages primary key is not seq alone.
	ErrSchema = errors.New("store schema is not supported by this binary")
	// ErrNotRoster means the name is not on the conversation roster.
	ErrNotRoster = errors.New("name is not in the roster")
	// ErrNotMember means the roster name has no member row.
	ErrNotMember = errors.New("member has not been launched")
	// ErrLaunched means launched was asked for a name that already has a row.
	ErrLaunched = errors.New("member is already launched")
	// ErrReplyTarget means re is not the seq of an earlier message of the same conversation.
	ErrReplyTarget = errors.New("re is not an earlier message of the conversation")
	// ErrRecordReference means a record breaks a foreign key, kind, CHECK, or uniqueness rule
	// of the record tables other than its own key.
	ErrRecordReference = errors.New("record breaks a reference, kind, or uniqueness rule")
	// ErrKeptDropped means a grouping drops a finding that another of its drops keeps.
	ErrKeptDropped = errors.New("a kept finding is itself dropped")
	// ErrNotKept means a settle or a halt names a finding whose set has no grouping, or that is dropped.
	ErrNotKept = errors.New("finding is not a kept finding of a grouped set")
	// ErrFixDismissed means a fix names a finding whose settle proposal is a dismiss.
	ErrFixDismissed = errors.New("fix belongs to a dismissed settle")

	// errNoChange tells withTx to roll the transaction back and return nil.
	// Idempotent publish and close of an already-closed conversation take it
	// so a reread does not commit an empty write.
	errNoChange = errors.New("store transaction made no change")
)

// Binding is the conversation row beside the name and status.
// Prompts and Roster keep the caller's order.
type Binding struct {
	Task      string
	Ticket    string
	Seat      string
	Directory string
	Prompts   []string
	Custom    string
	Roster    []string
}

// Conversation is a name, its status, and its binding.
type Conversation struct {
	Name   string
	Status string
	Binding
}

// Message is one accepted publish. To keeps the caller's order.
// Seq is assigned at commit and increases across the whole store. Within one
// conversation it increases with gaps where other conversations published.
// Conversation is set by Newest and After. Other reads leave it empty.
// Re is nil when the message responds to nothing.
type Message struct {
	Conversation string
	Seq          int64
	Time         string
	From         string
	To           []string
	Body         string
	TxID         string
	Kind         string
	Re           *int64
}

// Publish is a validated publish to commit. A nil Re responds to nothing.
type Publish struct {
	Conversation string
	From         string
	To           []string
	Body         string
	TxID         string
	Kind         string
	Re           *int64
	Time         string
}

// Member is one roster row without its session id.
// PID is set only when HasPID is true.
type Member struct {
	Name       string
	Status     string
	Launched   string
	Registered string
	PID        int64
	HasPID     bool
}

// MemberSession is a roster name and the session id stored for it.
type MemberSession struct {
	Name    string
	Session string
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

// Create inserts an open conversation and its binding.
// A name already present returns the stored conversation and already true, and writes nothing.
// The binding on that call is ignored.
func (s *Store) Create(ctx context.Context, name string, b Binding) (Conversation, bool, error) {
	b = normalizeBinding(b)
	prompts, err := marshalTo(b.Prompts)
	if err != nil {
		return Conversation{}, false, err
	}
	roster, err := marshalTo(b.Roster)
	if err != nil {
		return Conversation{}, false, err
	}
	var conv Conversation
	var already bool
	err = s.withTx(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO conversations (
				name, status, task, ticket, seat, directory, prompts, custom, roster
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			name, StatusOpen, b.Task, b.Ticket, b.Seat, b.Directory, prompts, b.Custom, roster)
		if err == nil {
			conv = Conversation{Name: name, Status: StatusOpen, Binding: b}
			return nil
		}
		if !isConstraint(err) {
			return err
		}
		conv, err = conversationByName(ctx, tx, name)
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
// already is true when the conversation was closed before this call. That
// path writes nothing and returns the stored row.
func (s *Store) CloseConversation(ctx context.Context, name string) (Conversation, bool, error) {
	var conv Conversation
	var already bool
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		conv, err = conversationByName(ctx, tx, name)
		if err != nil {
			return err
		}
		if conv.Status == StatusClosed {
			already = true
			return errNoChange
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE conversations SET status = ? WHERE name = ?`,
			StatusClosed, name)
		if err != nil {
			return err
		}
		conv.Status = StatusClosed
		return nil
	})
	if err != nil {
		return Conversation{}, false, err
	}
	return conv, already, nil
}

// Newest returns at most limit messages across every conversation, highest
// seq first, each with its conversation. A before greater than zero keeps
// only messages whose seq is less than before. A before of zero is no bound.
func (s *Store) Newest(ctx context.Context, limit int, before int64) ([]Message, error) {
	if limit < 1 {
		return nil, errors.New("newest limit must be positive")
	}
	if before < 0 {
		return nil, errors.New("newest before must not be negative")
	}
	var out []Message
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		if before == 0 {
			out, err = queryRows(ctx, tx, scanPlaced, `
				SELECT conversation, `+messageColumns+`
				FROM messages
				ORDER BY seq DESC
				LIMIT ?`, limit)
		} else {
			out, err = queryRows(ctx, tx, scanPlaced, `
				SELECT conversation, `+messageColumns+`
				FROM messages
				WHERE seq < ?
				ORDER BY seq DESC
				LIMIT ?`, before, limit)
		}
		return err
	})
	return out, err
}

// After returns at most limit messages with seq greater than after, oldest
// first, each with its conversation. An empty conversation reads every
// conversation. A conversation that does not exist yields no messages and
// no error. more reports a further message after the last one returned.
func (s *Store) After(ctx context.Context, conversation string, after int64, limit int) (msgs []Message, more bool, err error) {
	if limit < 1 {
		return nil, false, errors.New("after limit must be positive")
	}
	// One row past the limit answers more without a second count query.
	probe := int64(limit) + 1
	if probe < 1 {
		probe = -1
	}
	err = s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		if conversation == "" {
			msgs, err = queryRows(ctx, tx, scanPlaced, `
				SELECT conversation, `+messageColumns+`
				FROM messages
				WHERE seq > ?
				ORDER BY seq
				LIMIT ?`, after, probe)
		} else {
			msgs, err = queryRows(ctx, tx, scanPlaced, `
				SELECT conversation, `+messageColumns+`
				FROM messages
				WHERE conversation = ? AND seq > ?
				ORDER BY seq
				LIMIT ?`, conversation, after, probe)
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(msgs) > limit {
		return msgs[:limit], true, nil
	}
	return msgs, false, nil
}

// List returns every conversation, open and closed.
// The conversation whose newest message has the highest seq is first.
// Conversations with no message follow, ordered by name.
func (s *Store) List(ctx context.Context) ([]Conversation, error) {
	var out []Conversation
	err := s.withTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		out, err = queryRows(ctx, tx, scanConversation, `
			SELECT `+conversationColumns+`
			FROM conversations
			ORDER BY (
				SELECT MAX(seq) FROM messages
				WHERE conversation = conversations.name
			) DESC NULLS LAST, name`)
		return err
	})
	return out, err
}

// Publish commits one message, or returns the original when this sender
// already stored this txid with the same kind, to, body, and re.
// The same sender and txid with a different kind, to, body, or re returns ErrConflict.
// A re that is not the seq of an earlier message of the conversation returns
// ErrReplyTarget and writes nothing.
// Another sender using that txid is a different attempt.
// A matching attempt returns the original message and writes nothing.
// The conversation is the row committed with that result.
// A missing conversation returns ErrNotFound and writes nothing.
func (s *Store) Publish(ctx context.Context, in Publish) (Message, Conversation, bool, error) {
	var msg Message
	var conv Conversation
	var already bool
	err := s.withTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		conv, err = conversationByName(ctx, tx, in.Conversation)
		if err != nil {
			return err
		}

		stored, found, err := messageByAttempt(ctx, tx, in.Conversation, in.From, in.TxID)
		if err != nil {
			return err
		}
		if found {
			if stored.From == in.From && stored.Kind == in.Kind && stored.Body == in.Body &&
				sameTo(stored.To, in.To) && sameRe(stored.Re, in.Re) {
				msg = stored
				already = true
				return errNoChange
			}
			return ErrConflict
		}

		rec, err := marshalTo(in.To)
		if err != nil {
			return err
		}
		// Seq order is commit order only because the store has one writer.
		var seq int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO messages (
				conversation, time, sender, recipients, body, txid, kind, re
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING seq`,
			in.Conversation, in.Time, in.From, rec, in.Body, in.TxID, in.Kind, nullSeq(in.Re)).Scan(&seq)
		if isBadReply(err) {
			return ErrReplyTarget
		}
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
			Kind: in.Kind,
			Re:   copySeq(in.Re),
		}
		return nil
	})
	if err != nil {
		return Message{}, Conversation{}, false, err
	}
	return msg, conv, already, nil
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
		conv, err := conversationByName(ctx, tx, name)
		if err != nil {
			return err
		}
		status = conv.Status
		rows, err := tx.QueryContext(ctx, `
			SELECT `+messageColumns+`
			FROM messages
			WHERE conversation = ? AND seq > ?
			ORDER BY seq`, name, after)
		if err != nil {
			return err
		}
		for rows.Next() {
			m, err := scanMessage(rows, false)
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
		version, hasVersion, err := recordedSchema(ctx, tx)
		if err != nil {
			return err
		}
		if hasVersion && version != SchemaVersion {
			return fmt.Errorf("%w: %s", ErrSchema, version)
		}
		match, err := shapeMatches(ctx, tx)
		if err != nil {
			return err
		}
		if hasVersion && !match {
			return ErrSchema
		}
		if !hasVersion && !match {
			exists, err := tableExists(ctx, tx, "messages")
			if err != nil {
				return err
			}
			// A file that already has messages and lacks this shape is not a place to add tables.
			if exists {
				return ErrSchema
			}
		}
		for _, stmt := range schemaStmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("create schema: %w", err)
			}
		}
		if hasVersion {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('schema', ?)`, SchemaVersion)
		if err != nil {
			return fmt.Errorf("record schema version: %w", err)
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

func normalizeBinding(b Binding) Binding {
	b.Prompts = copyTo(b.Prompts)
	b.Roster = copyTo(b.Roster)
	return b
}

type rowScanner interface {
	Scan(dest ...any) error
}

func conversationByName(ctx context.Context, tx *sql.Tx, name string) (Conversation, error) {
	conv, err := scanConversation(tx.QueryRowContext(ctx,
		`SELECT `+conversationColumns+` FROM conversations WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return conv, err
}

func scanConversation(row rowScanner) (Conversation, error) {
	var c Conversation
	var prompts, roster string
	if err := row.Scan(
		&c.Name, &c.Status, &c.Task, &c.Ticket, &c.Seat, &c.Directory, &prompts, &c.Custom, &roster,
	); err != nil {
		return Conversation{}, err
	}
	return finishConversation(c, prompts, roster)
}

func finishConversation(c Conversation, prompts, roster string) (Conversation, error) {
	var err error
	c.Prompts, err = unmarshalTo(prompts)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation %s prompts: %w", c.Name, err)
	}
	c.Roster, err = unmarshalTo(roster)
	if err != nil {
		return Conversation{}, fmt.Errorf("conversation %s roster: %w", c.Name, err)
	}
	return c, nil
}

func recordedSchema(ctx context.Context, tx *sql.Tx) (string, bool, error) {
	ok, err := tableExists(ctx, tx, "meta")
	if err != nil || !ok {
		return "", false, err
	}
	var version string
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return version, true, nil
}

func shapeMatches(ctx context.Context, tx *sql.Tx) (bool, error) {
	for _, col := range []string{"kind", "re"} {
		ok, err := columnExists(ctx, tx, "messages", col)
		if err != nil || !ok {
			return false, err
		}
	}
	for _, col := range []string{"task", "ticket", "seat", "directory", "prompts", "custom", "roster"} {
		ok, err := columnExists(ctx, tx, "conversations", col)
		if err != nil || !ok {
			return false, err
		}
	}
	seqKey, err := seqIsPrimaryKey(ctx, tx)
	if err != nil || !seqKey {
		return false, err
	}
	keys, err := uniqueKeys(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, want := range [][]string{{"conversation", "seq"}, {"conversation", "seq", "kind"}} {
		if !hasKey(keys, want) {
			return false, nil
		}
	}
	for _, table := range append([]string{"members"}, recordTables...) {
		ok, err := tableExists(ctx, tx, table)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// uniqueKeys returns the ordered columns of each unique index on messages that covers every row.
// An unnamed UNIQUE gets a generated sqlite_autoindex name, so keys are matched by columns.
// A partial index cannot be a foreign key parent, so it is not a key.
func uniqueKeys(ctx context.Context, tx *sql.Tx) ([][]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT il.name, ii.name
		FROM pragma_index_list('messages') AS il, pragma_index_info(il.name) AS ii
		WHERE il."unique" = 1 AND il.partial = 0
		ORDER BY il.name, ii.seqno`)
	if err != nil {
		return nil, err
	}
	var keys [][]string
	last := ""
	for rows.Next() {
		var index string
		var col sql.NullString
		if err := rows.Scan(&index, &col); err != nil {
			return nil, joinClose(err, rows.Close())
		}
		if len(keys) == 0 || index != last {
			keys = append(keys, nil)
			last = index
		}
		keys[len(keys)-1] = append(keys[len(keys)-1], col.String)
	}
	if err := rows.Err(); err != nil {
		return nil, joinClose(err, rows.Close())
	}
	return keys, rows.Close()
}

func hasKey(keys [][]string, want []string) bool {
	for _, k := range keys {
		if slices.Equal(k, want) {
			return true
		}
	}
	return false
}

// seqIsPrimaryKey reports whether seq alone is the messages primary key.
// The per-conversation shape also has a seq column, so presence cannot tell them apart.
func seqIsPrimaryKey(ctx context.Context, tx *sql.Tx) (bool, error) {
	var n int
	var name string
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(name), '') FROM pragma_table_info('messages') WHERE pk > 0`).Scan(&n, &name)
	return n == 1 && name == "seq", err
}

func tableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

func columnExists(ctx context.Context, tx *sql.Tx, table, col string) (bool, error) {
	var q string
	switch table {
	case "messages":
		q = `SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = ?`
	case "conversations":
		q = `SELECT COUNT(*) FROM pragma_table_info('conversations') WHERE name = ?`
	default:
		return false, fmt.Errorf("unknown table %s", table)
	}
	var n int
	err := tx.QueryRowContext(ctx, q, col).Scan(&n)
	return n > 0, err
}

// scanPlaced reads one message row that leads with the conversation column.
func scanPlaced(row rowScanner) (Message, error) {
	return scanMessage(row, true)
}

// scanMessage reads messageColumns, led by conversation when placed is true.
func scanMessage(row rowScanner, placed bool) (Message, error) {
	var m Message
	var rec string
	var re sql.NullInt64
	dest := []any{&m.Seq, &m.Time, &m.From, &rec, &m.Body, &m.TxID, &m.Kind, &re}
	if placed {
		dest = append([]any{&m.Conversation}, dest...)
	}
	if err := row.Scan(dest...); err != nil {
		return Message{}, err
	}
	to, err := unmarshalTo(rec)
	if err != nil {
		if placed {
			return Message{}, fmt.Errorf("message %s/%d recipients: %w", m.Conversation, m.Seq, err)
		}
		return Message{}, fmt.Errorf("message %d recipients: %w", m.Seq, err)
	}
	m.To = to
	if re.Valid {
		m.Re = &re.Int64
	}
	return m, nil
}

// queryRows scans every row of query with scan, then closes the rows. The result is empty rather than nil.
func queryRows[T any](ctx context.Context, tx *sql.Tx, scan func(rowScanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, joinClose(err, rows.Close())
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, joinClose(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
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
	m, err := scanMessage(tx.QueryRowContext(ctx, `
		SELECT `+messageColumns+`
		FROM messages
		WHERE conversation = ? AND sender = ? AND txid = ?`,
		conversation, sender, txid), false)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
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

// SQLite result codes this store branches on (https://sqlite.org/rescode.html).
const (
	sqliteConstraint           = 19
	sqliteConstraintCheck      = 275
	sqliteConstraintForeignKey = 787
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// sqliteCode is the extended result code of a driver error, or 0.
func sqliteCode(err error) int {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return 0
	}
	return se.Code()
}

func sqlitePrimary(err error) int {
	return sqliteCode(err) & 0xff
}

// isBadReply reports a message insert refused by the re foreign key or the re < seq check.
// The conversation is read in the same transaction first, so its foreign key cannot be the cause.
// The result code does not name the constraint, so re < seq must stay the only CHECK on messages.
func isBadReply(err error) bool {
	switch sqliteCode(err) {
	case sqliteConstraintCheck, sqliteConstraintForeignKey:
		return true
	default:
		return false
	}
}

func isConstraint(err error) bool {
	return sqlitePrimary(err) == sqliteConstraint
}

func isBusy(err error) bool {
	switch sqlitePrimary(err) {
	case 5, 6:
		return true
	default:
		return false
	}
}
