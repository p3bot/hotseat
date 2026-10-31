// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEventDataSplitsEveryLineEndingIntoDataFields(t *testing.T) {
	rec := httptest.NewRecorder()
	out := eventWriter{w: rec, rc: http.NewResponseController(rec)}
	if err := out.event(eventMessage, "7", "a\r\nb\rc\nd"); err != nil {
		t.Fatal(err)
	}
	want := "event: message\nid: 7\ndata: a\ndata: b\ndata: c\ndata: d\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !rec.Flushed {
		t.Fatal("event was not flushed")
	}
}

func TestEventWithNoDataStillDispatches(t *testing.T) {
	rec := httptest.NewRecorder()
	out := eventWriter{w: rec, rc: http.NewResponseController(rec)}
	if err := out.event(eventReset, "", ""); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.Body.String(), "event: reset\ndata: \n\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
