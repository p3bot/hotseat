// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func seqPtr(v int64) *int64 { return &v }

func reText(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

func publishRe(t *testing.T, st *Store, conv, txid string, re *int64) Message {
	t.Helper()
	m, _, already, err := st.Publish(context.Background(), Publish{
		Conversation: conv, From: "alice", To: []string{"bob"},
		Body: "b " + txid, TxID: txid, Kind: "say", Re: re, Time: "t",
	})
	if err != nil || already {
		t.Fatalf("publish %s: %+v already=%v err=%v", txid, m, already, err)
	}
	return m
}

func TestPublishReplyNamesAnEarlierMessageOfTheConversation(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for _, name := range []string{"job", "other"} {
		if _, _, err := st.Create(ctx, name, Binding{}); err != nil {
			t.Fatal(err)
		}
	}
	first := publishRe(t, st, "job", "1", nil)
	publishRe(t, st, "other", "2", nil)
	reply := publishRe(t, st, "job", "3", seqPtr(first.Seq))
	if first.Re != nil || reply.Re == nil || *reply.Re != first.Seq {
		t.Fatalf("publish re: first=%s reply=%s", reText(first.Re), reText(reply.Re))
	}

	_, msgs, err := st.Transcript(ctx, "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	newest, err := st.Newest(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := st.After(ctx, "", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]*int64{first.Seq: nil, reply.Seq: seqPtr(first.Seq), 2: nil}
	for _, read := range [][]Message{msgs, newest, after} {
		for _, m := range read {
			if !sameRe(m.Re, want[m.Seq]) {
				t.Fatalf("seq %d re = %s, want %s", m.Seq, reText(m.Re), reText(want[m.Seq]))
			}
		}
	}
}

func TestPublishRefusesReplyThatIsNotAnEarlierMessageOfTheConversation(t *testing.T) {
	cases := []struct {
		name string
		re   func(job, other Message) int64
	}{
		{"message of another conversation", func(_, other Message) int64 { return other.Seq }},
		{"seq that does not exist", func(_, _ Message) int64 { return 99 }},
		{"zero", func(_, _ Message) int64 { return 0 }},
		// The foreign key alone accepts this: SQLite checks it after the row exists.
		{"own seq", func(_, other Message) int64 { return other.Seq + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			ctx := context.Background()
			for _, name := range []string{"job", "other"} {
				if _, _, err := st.Create(ctx, name, Binding{}); err != nil {
					t.Fatal(err)
				}
			}
			job := publishRe(t, st, "job", "1", nil)
			other := publishRe(t, st, "other", "2", nil)
			re := tc.re(job, other)
			_, _, _, err := st.Publish(ctx, Publish{
				Conversation: "job", From: "alice", To: []string{"bob"},
				Body: "reply", TxID: "3", Kind: "say", Re: &re, Time: "t",
			})
			if !errors.Is(err, ErrReplyTarget) {
				t.Fatalf("re %d: err = %v", re, err)
			}
			_, msgs, err := st.Transcript(ctx, "job", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 || msgs[0].Seq != job.Seq {
				t.Fatalf("transcript = %+v", msgs)
			}
		})
	}
}

func TestPublishReplyIsAttemptContent(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	first := publishRe(t, st, "job", "1", nil)
	second := publishRe(t, st, "job", "2", nil)
	reply := publishRe(t, st, "job", "3", seqPtr(first.Seq))

	attempt := func(txid string, re *int64) (Message, bool, error) {
		m, _, already, err := st.Publish(ctx, Publish{
			Conversation: "job", From: "alice", To: []string{"bob"},
			Body: "b " + txid, TxID: txid, Kind: "say", Re: re, Time: "later",
		})
		return m, already, err
	}
	m, already, err := attempt("3", seqPtr(first.Seq))
	if err != nil || !already || m.Seq != reply.Seq || !sameRe(m.Re, reply.Re) {
		t.Fatalf("same re: %+v already=%v err=%v", m, already, err)
	}
	m, already, err = attempt("1", nil)
	if err != nil || !already || m.Seq != first.Seq || m.Re != nil {
		t.Fatalf("same absent re: %+v already=%v err=%v", m, already, err)
	}
	for _, tc := range []struct {
		name string
		txid string
		re   *int64
	}{
		{"different re", "3", seqPtr(second.Seq)},
		{"absent against present", "3", nil},
		{"present against absent", "2", seqPtr(first.Seq)},
	} {
		if _, _, err := attempt(tc.txid, tc.re); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
	}
}

// previousMessages is the messages table of the binary before re.
const previousMessages = `CREATE TABLE messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation TEXT NOT NULL REFERENCES conversations(name),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  kind TEXT NOT NULL,
  UNIQUE (conversation, sender, txid)
) STRICT`

const replyMessagesHead = `CREATE TABLE messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation TEXT NOT NULL REFERENCES conversations(name),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  kind TEXT NOT NULL,
  re INTEGER,
  UNIQUE (conversation, sender, txid)`

// writeVersion1 writes a version-1 file whose messages table is built by
// messages, with one conversation and one message, the members table, and
// the record tables, in WAL mode.
func writeVersion1(t *testing.T, messages ...string) string {
	t.Helper()
	dir := t.TempDir()
	db := openRaw(t, dir)
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`,
		`INSERT INTO meta (key, value) VALUES ('schema', '1')`,
		`CREATE TABLE conversations (
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
		`INSERT INTO conversations (name, status, task, ticket, seat, directory, prompts, custom, roster)
			VALUES ('job', 'open', '', '', '', '', '[]', '', '[]')`,
	}
	stmts = append(stmts, messages...)
	stmts = append(stmts,
		`INSERT INTO messages (conversation, time, sender, recipients, body, txid, kind)
			VALUES ('job', 't', 'alice', '[]', 'keep', 'k', 'say')`,
		`CREATE TABLE members (
			conversation TEXT NOT NULL REFERENCES conversations(name),
			name TEXT NOT NULL,
			launched TEXT NOT NULL,
			registered TEXT NOT NULL,
			status TEXT NOT NULL,
			session TEXT NOT NULL,
			pid INTEGER,
			PRIMARY KEY (conversation, name)
		) STRICT`,
	)
	stmts = append(stmts, recordStmts(t)...)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recordStmts returns the record table statements this binary creates.
func recordStmts(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, table := range recordTables {
		prefix := "CREATE TABLE IF NOT EXISTS " + table + " ("
		for _, stmt := range schemaStmts {
			if strings.HasPrefix(stmt, prefix) {
				out = append(out, stmt)
			}
		}
	}
	if len(out) != len(recordTables) {
		t.Fatalf("found %d record statements for %d tables", len(out), len(recordTables))
	}
	return out
}

func TestOpenRejectsVersion1WithoutReOrBothMessageKeys(t *testing.T) {
	cases := []struct {
		name     string
		messages []string
	}{
		{"previous binary", []string{
			previousMessages,
			`CREATE INDEX messages_conversation_seq ON messages (conversation, seq)`,
		}},
		{"no conversation seq key", []string{
			replyMessagesHead + `, UNIQUE (conversation, seq, kind)) STRICT`,
		}},
		{"no conversation seq kind key", []string{
			replyMessagesHead + `, UNIQUE (conversation, seq)) STRICT`,
		}},
		{"keys in another column order", []string{
			replyMessagesHead + `, UNIQUE (seq, conversation), UNIQUE (conversation, kind, seq)) STRICT`,
		}},
		{"unique indexes that are partial", []string{
			replyMessagesHead + `) STRICT`,
			`CREATE UNIQUE INDEX messages_cs ON messages (conversation, seq) WHERE kind = 'say'`,
			`CREATE UNIQUE INDEX messages_csk ON messages (conversation, seq, kind) WHERE kind = 'say'`,
		}},
		{"indexes that are not unique", []string{
			replyMessagesHead + `) STRICT`,
			`CREATE INDEX messages_cs ON messages (conversation, seq)`,
			`CREATE INDEX messages_csk ON messages (conversation, seq, kind)`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeVersion1(t, tc.messages...)
			path := filepath.Join(dir, FileName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrSchema) {
				t.Fatalf("open = %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("database file changed")
			}
		})
	}
}

func TestOpenMatchesMessageKeysByColumnsNotName(t *testing.T) {
	dir := writeVersion1(t,
		replyMessagesHead+`) STRICT`,
		`CREATE UNIQUE INDEX messages_cs ON messages (conversation, seq)`,
		`CREATE UNIQUE INDEX messages_csk ON messages (conversation, seq, kind)`,
	)
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("open = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewDatabaseHasReplyColumnAndMessageKeys(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, cols, tableSQL := schemaOf(t, dir)
	if !containsCol(cols, "re") {
		t.Fatalf("cols = %v", cols)
	}
	for _, want := range []string{
		"re INTEGER CHECK (re < seq)",
		"UNIQUE (conversation, seq)",
		"UNIQUE (conversation, seq, kind)",
		"FOREIGN KEY (conversation, re) REFERENCES messages (conversation, seq)",
	} {
		if !strings.Contains(tableSQL, want) {
			t.Fatalf("messages sql lacks %q: %s", want, tableSQL)
		}
	}
	db := openRaw(t, dir)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'messages_conversation_seq'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("messages_conversation_seq still exists")
	}
}

// isBadReply maps every CHECK failure on a message insert to ErrReplyTarget.
func TestReplyCheckIsTheOnlyCheckOnMessages(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, tableSQL := schemaOf(t, dir)
	if n := strings.Count(strings.ToUpper(tableSQL), "CHECK"); n != 1 || !strings.Contains(tableSQL, "CHECK (re < seq)") {
		t.Fatalf("messages has %d CHECK constraints; isBadReply would misreport all but re < seq:\n%s", n, tableSQL)
	}
}
