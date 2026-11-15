// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package store

import "encoding/json"

func marshalTo(to []string) (string, error) {
	if to == nil {
		to = []string{}
	}
	b, err := json.Marshal(to)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalTo(raw string) ([]string, error) {
	var to []string
	if err := json.Unmarshal([]byte(raw), &to); err != nil {
		return nil, err
	}
	if to == nil {
		to = []string{}
	}
	return to, nil
}

func sameTo(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func copyTo(to []string) []string {
	out := make([]string, len(to))
	copy(out, to)
	return out
}

func sameRe(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func copySeq(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// nullSeq is the SQL value of an optional seq. nil is NULL.
func nullSeq(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
