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

func sendRe(t *testing.T, st *Store, chat, txid string, re *int64) Message {
	t.Helper()
	m, _, already, err := st.Send(context.Background(), Send{
		Chat: chat, From: "alice", To: []string{"bob"},
		Body: "b " + txid, TxID: txid, Kind: "say", Re: re, Time: "t",
	})
	if err != nil || already {
		t.Fatalf("send %s: %+v already=%v err=%v", txid, m, already, err)
	}
	return m
}

func TestSendReplyNamesAnEarlierMessageOfTheChat(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	for _, name := range []string{"job", "other"} {
		if _, _, err := st.Create(ctx, name, Binding{}); err != nil {
			t.Fatal(err)
		}
	}
	first := sendRe(t, st, "job", "1", nil)
	sendRe(t, st, "other", "2", nil)
	reply := sendRe(t, st, "job", "3", seqPtr(first.Seq))
	if first.Re != nil || reply.Re == nil || *reply.Re != first.Seq {
		t.Fatalf("send re: first=%s reply=%s", reText(first.Re), reText(reply.Re))
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

func TestSendRefusesReplyThatIsNotAnEarlierMessageOfTheChat(t *testing.T) {
	cases := []struct {
		name string
		re   func(job, other Message) int64
	}{
		{"message of another chat", func(_, other Message) int64 { return other.Seq }},
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
			job := sendRe(t, st, "job", "1", nil)
			other := sendRe(t, st, "other", "2", nil)
			re := tc.re(job, other)
			_, _, _, err := st.Send(ctx, Send{
				Chat: "job", From: "alice", To: []string{"bob"},
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

func TestSendReplyIsAttemptContent(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	first := sendRe(t, st, "job", "1", nil)
	second := sendRe(t, st, "job", "2", nil)
	reply := sendRe(t, st, "job", "3", seqPtr(first.Seq))

	attempt := func(txid string, re *int64) (Message, bool, error) {
		m, _, already, err := st.Send(ctx, Send{
			Chat: "job", From: "alice", To: []string{"bob"},
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
  chat TEXT NOT NULL REFERENCES chats(name),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  kind TEXT NOT NULL,
  UNIQUE (chat, sender, txid)
) STRICT`

const replyMessagesHead = `CREATE TABLE messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  chat TEXT NOT NULL REFERENCES chats(name),
  time TEXT NOT NULL,
  sender TEXT NOT NULL,
  recipients TEXT NOT NULL,
  body TEXT NOT NULL,
  txid TEXT NOT NULL,
  kind TEXT NOT NULL,
  re INTEGER,
  UNIQUE (chat, sender, txid)`

// writeVersion1 writes a version-1 file whose messages table is built by
// messages, with one chat and one message, the members table, and
// the record tables, in WAL mode.
func writeVersion1(t *testing.T, messages ...string) string {
	t.Helper()
	dir := t.TempDir()
	db := openRaw(t, dir)
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`,
		`INSERT INTO meta (key, value) VALUES ('schema', '1')`,
		`CREATE TABLE chats (
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
		`INSERT INTO chats (name, status, task, ticket, seat, directory, prompts, custom, roster)
			VALUES ('job', 'open', '', '', '', '', '[]', '', '[]')`,
	}
	stmts = append(stmts, messages...)
	stmts = append(stmts,
		`INSERT INTO messages (chat, time, sender, recipients, body, txid, kind)
			VALUES ('job', 't', 'alice', '[]', 'keep', 'k', 'say')`,
		schemaStmt(t, "members"),
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
		out = append(out, schemaStmt(t, table))
	}
	return out
}

// schemaStmt returns the statement this binary creates table with.
func schemaStmt(t *testing.T, table string) string {
	t.Helper()
	prefix := "CREATE TABLE IF NOT EXISTS " + table + " ("
	for _, stmt := range schemaStmts {
		if strings.HasPrefix(stmt, prefix) {
			return stmt
		}
	}
	t.Fatalf("no statement creates %s", table)
	return ""
}

func TestOpenRejectsVersion1WithoutReOrBothMessageKeys(t *testing.T) {
	cases := []struct {
		name     string
		messages []string
	}{
		{"previous binary", []string{
			previousMessages,
			`CREATE INDEX messages_chat_seq ON messages (chat, seq)`,
		}},
		{"no chat seq key", []string{
			replyMessagesHead + `, UNIQUE (chat, seq, kind)) STRICT`,
		}},
		{"no chat seq kind key", []string{
			replyMessagesHead + `, UNIQUE (chat, seq)) STRICT`,
		}},
		{"keys in another column order", []string{
			replyMessagesHead + `, UNIQUE (seq, chat), UNIQUE (chat, kind, seq)) STRICT`,
		}},
		{"unique indexes that are partial", []string{
			replyMessagesHead + `) STRICT`,
			`CREATE UNIQUE INDEX messages_cs ON messages (chat, seq) WHERE kind = 'say'`,
			`CREATE UNIQUE INDEX messages_csk ON messages (chat, seq, kind) WHERE kind = 'say'`,
		}},
		{"indexes that are not unique", []string{
			replyMessagesHead + `) STRICT`,
			`CREATE INDEX messages_cs ON messages (chat, seq)`,
			`CREATE INDEX messages_csk ON messages (chat, seq, kind)`,
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
		`CREATE UNIQUE INDEX messages_cs ON messages (chat, seq)`,
		`CREATE UNIQUE INDEX messages_csk ON messages (chat, seq, kind)`,
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
		"UNIQUE (chat, seq)",
		"UNIQUE (chat, seq, kind)",
		"FOREIGN KEY (chat, re) REFERENCES messages (chat, seq)",
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'messages_chat_seq'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("messages_chat_seq still exists")
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
