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
	"testing"
)

func TestStartTokenIsStoredBesideItsPidAndReplacedWithIt(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{Roster: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	launched, err := st.LaunchMember(ctx, "job", "alpha", "t", 41, true, "boot/100")
	if err != nil || launched.PID != 41 || launched.Start != "boot/100" {
		t.Fatalf("launch = %+v err=%v", launched, err)
	}
	if got, err := st.Member(ctx, "job", "alpha"); err != nil || got.Start != "boot/100" {
		t.Fatalf("member = %+v err=%v", got, err)
	}
	kept, err := st.SetMemberStatus(ctx, "job", "alpha", MemberRunning, 0, false, "")
	if err != nil || kept.PID != 41 || kept.Start != "boot/100" {
		t.Fatalf("status without pid = %+v err=%v", kept, err)
	}
	moved, err := st.SetMemberStatus(ctx, "job", "alpha", MemberRunning, 42, true, "boot/200")
	if err != nil || moved.PID != 42 || moved.Start != "boot/200" {
		t.Fatalf("relaunch = %+v err=%v", moved, err)
	}
	cleared, err := st.SetMemberStatus(ctx, "job", "alpha", MemberRunning, 43, true, "")
	if err != nil || cleared.PID != 43 || cleared.Start != "" {
		t.Fatalf("relaunch without token = %+v err=%v", cleared, err)
	}
	if got, err := st.Member(ctx, "job", "alpha"); err != nil || got.PID != 43 || got.Start != "" {
		t.Fatalf("member after clear = %+v err=%v", got, err)
	}
}

func TestStartTokenWithoutAPidIsNotStored(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, _, err := st.Create(ctx, "job", Binding{Roster: []string{"alpha"}}); err != nil {
		t.Fatal(err)
	}
	launched, err := st.LaunchMember(ctx, "job", "alpha", "t", 0, false, "boot/100")
	if err != nil || launched.HasPID || launched.Start != "" {
		t.Fatalf("launch = %+v err=%v", launched, err)
	}
	if got, err := st.Member(ctx, "job", "alpha"); err != nil || got.HasPID || got.Start != "" {
		t.Fatalf("member = %+v err=%v", got, err)
	}
}

func TestOpenRejectsVersion1WhoseMembersLackStart(t *testing.T) {
	dir := writeVersion1(t, replyMessagesHead+`, UNIQUE (conversation, seq), UNIQUE (conversation, seq, kind)) STRICT`)
	db := openRaw(t, dir)
	for _, stmt := range []string{
		`DROP TABLE members`,
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
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
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
}
