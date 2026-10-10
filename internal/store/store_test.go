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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenRelativeDirectory(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	st, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	want := filepath.Join(root, "data", FileName)
	if st.Path() != want {
		t.Fatalf("path = %s, want %s", st.Path(), want)
	}
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob"},
		Body: "hello", TxID: "k", Time: "t",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	status, msgs, err := reopened.Transcript(ctx, "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusOpen || len(msgs) != 1 || msgs[0].Body != "hello" {
		t.Fatalf("reopen status=%s msgs=%+v", status, msgs)
	}
}

func TestOpenPermissionsAndPragmas(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if st.Path() != filepath.Join(dir, FileName) {
		t.Fatalf("path = %s", st.Path())
	}
	info, err := os.Stat(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	lockInfo, err := os.Lstat(filepath.Join(dir, LockName))
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode()&os.ModeSymlink != 0 || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o", lockInfo.Mode())
	}
	var mode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %s", mode)
	}
	var syncMode, fk int
	if err := st.db.QueryRow(`PRAGMA synchronous`).Scan(&syncMode); err != nil {
		t.Fatal(err)
	}
	if syncMode != 2 {
		t.Fatalf("synchronous = %d, want FULL (2)", syncMode)
	}
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d", fk)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database appeared in the working directory: %v", err)
	}
}

func TestOpenTightensDirectoryAndRefusesSymlinks(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("KEEP ME"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("existing directory", func(t *testing.T) {
		dir := filepath.Join(root, "plain")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := st.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %o", dirInfo.Mode().Perm())
		}
		for _, suffix := range []string{"", "-wal"} {
			p := st.Path() + suffix
			info, err := os.Lstat(p)
			if err != nil {
				t.Fatalf("%s: %v", suffix, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("%s is a symlink", p)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %o", p, info.Mode().Perm())
			}
		}
		if info, err := os.Lstat(st.Path() + "-shm"); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
				t.Fatalf("shm mode = %o", info.Mode().Perm())
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	})

	cases := []struct {
		name     string
		suffix   string
		dangling bool
	}{
		{name: "database", suffix: ""},
		{name: "database dangling", suffix: "", dangling: true},
		{name: "wal", suffix: "-wal"},
		{name: "shm", suffix: "-shm"},
		{name: "lock", suffix: ".lock"},
		{name: "lock dangling", suffix: ".lock", dangling: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-"))
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := secret
			if tt.dangling {
				target = filepath.Join(root, tt.name+"-missing")
			}
			link := filepath.Join(dir, FileName+tt.suffix)
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("open = %v", err)
			}
			linkInfo, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if linkInfo.Mode()&os.ModeSymlink == 0 {
				t.Fatal("symlink was replaced")
			}
			if tt.suffix != "" {
				if _, err := os.Lstat(filepath.Join(dir, FileName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("database created despite symlink: %v", err)
				}
			}
			if tt.suffix != ".lock" {
				if _, err := os.Lstat(filepath.Join(dir, LockName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("lock created despite symlink: %v", err)
				}
			}
			if tt.dangling {
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dangling target: %v", err)
				}
				return
			}
			got, err := os.ReadFile(secret)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "KEEP ME" {
				t.Fatalf("secret = %q", got)
			}
			info, err := os.Stat(secret)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("secret mode = %o", info.Mode().Perm())
			}
		})
	}
}

func TestPublishRestartAndRetry(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Create(ctx, "other", Binding{}); err != nil {
		t.Fatal(err)
	}
	againConv, already, err := st.Create(ctx, "job", Binding{})
	if err != nil || !already || againConv.Name != "job" || againConv.Status != StatusOpen {
		t.Fatalf("duplicate create = %+v already=%v err=%v", againConv, already, err)
	}
	first, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob", "carol"},
		Body: "one", TxID: "k", Time: "2026-10-03T07:00:00Z",
	})
	if err != nil || already || first.Seq != 1 {
		t.Fatalf("first = %+v already=%v err=%v", first, already, err)
	}
	other, _, _, err := st.Publish(ctx, Publish{
		Conversation: "other", From: "alice", To: []string{"bob"},
		Body: "o", TxID: "k", Time: "2026-10-03T07:00:01Z",
	})
	if err != nil || other.Seq != 1 {
		t.Fatalf("other conversation seq = %+v err=%v", other, err)
	}
	again, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob", "carol"},
		Body: "one", TxID: "k", Time: "later",
	})
	if err != nil || !already || again.Seq != 1 || again.Time != first.Time {
		t.Fatalf("retry = %+v already=%v err=%v", again, already, err)
	}
	if _, _, _, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"carol", "bob"},
		Body: "one", TxID: "k", Time: "later",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("reordered to: %v", err)
	}
	if _, err := st.CloseConversation(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	closedRetry, closedConv, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob", "carol"},
		Body: "one", TxID: "k", Time: "later",
	})
	if err != nil || !already || closedRetry.Seq != 1 || closedConv.Status != StatusClosed {
		t.Fatalf("closed retry = %+v status=%s already=%v err=%v", closedRetry, closedConv.Status, already, err)
	}
	late, lateConv, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob"},
		Body: "new", TxID: "new", Time: "later",
	})
	if err != nil || already || late.Seq != 2 || lateConv.Status != StatusClosed || late.Body != "new" {
		t.Fatalf("publish after close = %+v status=%s already=%v err=%v", late, lateConv.Status, already, err)
	}
	closedConv, already, err = st.Create(ctx, "job", Binding{})
	if err != nil || !already || closedConv.Status != StatusClosed {
		t.Fatalf("create after close = %+v already=%v err=%v", closedConv, already, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	status, msgs, err := reopened.Transcript(ctx, "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusClosed || len(msgs) != 2 || msgs[0].Body != "one" || msgs[0].Time != first.Time || msgs[1].Body != "new" {
		t.Fatalf("restart status=%s msgs=%+v", status, msgs)
	}
	if len(msgs[0].To) != 2 || msgs[0].To[0] != "bob" || msgs[0].To[1] != "carol" {
		t.Fatalf("to order = %v", msgs[0].To)
	}
}

func TestScanStopsBeforeLaterMessages(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, _, _, err := st.Publish(ctx, Publish{
			Conversation: "job", From: "alice", To: []string{"bob"},
			Body: fmt.Sprintf("m%d", i), TxID: fmt.Sprintf("k%d", i), Time: "t",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var got []int64
	status, err := st.Scan(ctx, "job", 0, func(m Message) bool {
		got = append(got, m.Seq)
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusOpen || len(got) != 1 || got[0] != 1 {
		t.Fatalf("status=%s seqs=%v", status, got)
	}
	var all []int64
	if _, err := st.Scan(ctx, "job", 1, func(m Message) bool {
		all = append(all, m.Seq)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0] != 2 || all[1] != 3 {
		t.Fatalf("rest = %v", all)
	}
	if _, err := st.Scan(ctx, "missing", 0, func(Message) bool {
		t.Fatal("scanned a missing conversation")
		return false
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestConcurrentPublish(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _, err := st.Publish(ctx, Publish{
				Conversation: "job", From: "alice", To: []string{"bob"},
				Body: fmt.Sprintf("m%d", i), TxID: fmt.Sprintf("k%d", i), Time: "t",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	_, msgs, err := st.Transcript(ctx, "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n {
		t.Fatalf("len = %d", len(msgs))
	}
	seen := map[int64]bool{}
	for _, m := range msgs {
		if seen[m.Seq] {
			t.Fatalf("duplicate seq %d", m.Seq)
		}
		seen[m.Seq] = true
	}
	for seq := int64(1); seq <= n; seq++ {
		if !seen[seq] {
			t.Fatalf("missing seq %d", seq)
		}
	}
}

func TestNewDatabaseSchema(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	version, cols, tableSQL := schemaOf(t, dir)
	if version != SchemaVersion || !containsCol(cols, "txid") || !containsCol(cols, "kind") {
		t.Fatalf("version=%s cols=%v", version, cols)
	}
	if !strings.Contains(tableSQL, "UNIQUE (conversation, sender, txid)") {
		t.Fatalf("sql=%s", tableSQL)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var members int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'members'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
		t.Fatalf("members tables = %d", members)
	}
}

func TestPublishAttemptIsPerSender(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	alice, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob"},
		Body: "one", TxID: "1", Time: "t",
	})
	if err != nil || already || alice.Seq != 1 {
		t.Fatalf("alice = %+v already=%v err=%v", alice, already, err)
	}
	bob, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "bob", To: []string{"bob"},
		Body: "one", TxID: "1", Time: "t2",
	})
	if err != nil || already || bob.Seq != 2 || bob.From != "bob" {
		t.Fatalf("bob = %+v already=%v err=%v", bob, already, err)
	}
	again, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob"},
		Body: "one", TxID: "1", Time: "later",
	})
	if err != nil || !already || again.Seq != 1 || again.Time != alice.Time {
		t.Fatalf("retry = %+v already=%v err=%v", again, already, err)
	}
	if _, _, _, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"carol"},
		Body: "one", TxID: "1", Time: "later",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different to: %v", err)
	}
	fresh, _, already, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{"bob"},
		Body: "one", TxID: "2", Time: "t3",
	})
	if err != nil || already || fresh.Seq != 3 {
		t.Fatalf("new id = %+v already=%v err=%v", fresh, already, err)
	}
	_, msgs, err := st.Transcript(ctx, "job", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func schemaOf(t *testing.T, dir string) (string, []string, string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var version, tableSQL string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'messages'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close rows: %v", err)
		}
	}()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return version, cols, tableSQL
}

func TestOpenRejectsVersion1WithoutKindOrMembers(t *testing.T) {
	t.Run("no kind column", func(t *testing.T) {
		dir := writeOldDB(t, false)
		if _, err := Open(dir); !errors.Is(err, ErrSchema) {
			t.Fatalf("open = %v", err)
		}
		assertOldShapeUntouched(t, dir)
	})
	t.Run("no member table", func(t *testing.T) {
		dir := writeOldDB(t, true)
		if _, err := Open(dir); !errors.Is(err, ErrSchema) {
			t.Fatalf("open = %v", err)
		}
		db := openRaw(t, dir)
		defer func() {
			if err := db.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'members'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("members table was created")
		}
	})
}

func writeOldDB(t *testing.T, withKind bool) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	kindCol := ""
	if withKind {
		kindCol = ", kind TEXT NOT NULL"
	}
	stmts := []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT`,
		`INSERT INTO meta (key, value) VALUES ('schema', '1')`,
		`CREATE TABLE conversations (
			name TEXT PRIMARY KEY,
			status TEXT NOT NULL,
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
		`CREATE TABLE messages (
			conversation TEXT NOT NULL,
			seq INTEGER NOT NULL,
			time TEXT NOT NULL,
			sender TEXT NOT NULL,
			recipients TEXT NOT NULL,
			body TEXT NOT NULL,
			txid TEXT NOT NULL` + kindCol + `,
			PRIMARY KEY (conversation, seq)
		) STRICT`,
	}
	insert := `INSERT INTO messages (conversation, seq, time, sender, recipients, body, txid) VALUES ('job', 1, 't', 'alice', '[]', 'keep', 'k')`
	if withKind {
		insert = `INSERT INTO messages (conversation, seq, time, sender, recipients, body, txid, kind) VALUES ('job', 1, 't', 'alice', '[]', 'keep', 'k', 'say')`
	}
	stmts = append(stmts, insert)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertOldShapeUntouched(t *testing.T, dir string) {
	t.Helper()
	db := openRaw(t, dir)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var body string
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'members'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("members table was created")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'kind'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("kind column was added")
	}
	if err := db.QueryRow(`SELECT body FROM messages`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "keep" {
		t.Fatalf("body = %s", body)
	}
}

func openRaw(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func containsCol(cols []string, name string) bool {
	for _, col := range cols {
		if col == name {
			return true
		}
	}
	return false
}

func TestSchemaRead(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	version, err := st.Schema(ctx)
	if err != nil || version != SchemaVersion {
		t.Fatalf("schema %q %v", version, err)
	}
	if _, err := st.db.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema'`); err != nil {
		t.Fatal(err)
	}
	version, err = st.Schema(ctx)
	if err != nil || version != "99" {
		t.Fatalf("schema %q %v", version, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Schema(ctx); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed %v", err)
	}
}

func TestSchemaMismatchKeepsMessages(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Create(ctx, "job", Binding{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.Publish(ctx, Publish{
		Conversation: "job", From: "alice", To: []string{}, Body: "keep", TxID: "k", Time: "t",
	}); err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrSchema) {
		t.Fatalf("reopen = %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages = %d", n)
	}
}

func TestSecondProcessRefused(t *testing.T) {
	if os.Getenv("HOTSEAT_STORE_CHILD") == "1" {
		_, err := Open(os.Getenv("HOTSEAT_STORE_DIR"))
		if errors.Is(err, ErrHeld) {
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecondProcessRefused$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HOTSEAT_STORE_CHILD=1", "HOTSEAT_STORE_DIR="+dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return
	}
	t.Fatalf("child err=%v output=%s", err, out)
}

func TestOtherSQLiteClientCannotWriteWhileOpen(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	cmd := exec.Command("sqlite3", st.Path(), `CREATE TABLE hack(x INTEGER); INSERT INTO hack VALUES (1);`)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("sqlite3 wrote the open database: %s", out)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'hack'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("hack table exists, sqlite3 said: %s", out)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return st
}
