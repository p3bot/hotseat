// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"bytes"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/p3bot/hotseat/internal/store"
)

func validName(s string) bool {
	if len(s) < 1 || len(s) > MaxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

func requireConversation(name string) error {
	if name == "" {
		return rule(ReasonConversationRequired)
	}
	if !validName(name) {
		return rule(ReasonBadConversation)
	}
	return nil
}

func requireParticipant(name string) error {
	if !validName(name) {
		return rule(ReasonBadName)
	}
	return nil
}

func parseTo(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, rule(ReasonToRequired)
	}
	var names []string
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&names); err != nil {
		return nil, rule(ReasonToShape)
	}
	if dec.More() {
		return nil, rule(ReasonToShape)
	}
	if err := validateTo(names); err != nil {
		return nil, err
	}
	return names, nil
}

func validateTo(names []string) error {
	if len(names) == 1 && names[0] == valueAll {
		return nil
	}
	for _, n := range names {
		if n == valueAll {
			return rule(ReasonToAllMixed)
		}
		if !validName(n) {
			return rule(ReasonToBadName)
		}
	}
	return nil
}

func parseDeadline(raw json.RawMessage) (time.Duration, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return 0, false, rule(ReasonDeadline)
	}
	if text == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil || d < 0 {
		return 0, false, rule(ReasonDeadline)
	}
	return d, true, nil
}

// decideWait reports the wait result. pending is true when the caller must block.
// msgs are the messages with seq greater than the cursor, oldest first.
func decideWait(status string, msgs []store.Message, name string, hasName bool) (Result, bool) {
	if !hasName {
		if len(msgs) > 0 {
			m := msgs[0]
			return Result{
				Outcome:  OutcomeOK,
				Messages: []store.Message{m},
				MatchSeq: m.Seq,
			}, false
		}
		if status == store.StatusClosed {
			return Result{Outcome: OutcomeClosed, Messages: []store.Message{}}, false
		}
		return Result{}, true
	}
	var span []store.Message
	for _, m := range msgs {
		span = append(span, m)
		if addressed(m, name) {
			return Result{Outcome: OutcomeOK, Messages: span, MatchSeq: m.Seq}, false
		}
	}
	if status == store.StatusClosed {
		if span == nil {
			span = []store.Message{}
		}
		return Result{Outcome: OutcomeClosed, Messages: span}, false
	}
	return Result{}, true
}

// nextRead reports whether m belongs in a read result and whether a later message is required.
// kept is how many messages are already chosen. A named read skips a message that is not addressed to name.
func nextRead(m store.Message, name string, hasName bool, kept, limit int) (keep, more bool) {
	if hasName && !addressed(m, name) {
		return false, true
	}
	return true, kept+1 < limit
}

func selectRead(msgs []store.Message, name string, hasName bool, limit int) []store.Message {
	out := make([]store.Message, 0)
	for _, m := range msgs {
		keep, more := nextRead(m, name, hasName, len(out), limit)
		if keep {
			out = append(out, m)
		}
		if !more {
			break
		}
	}
	return out
}

// addressed reports whether a named waiter or a named read includes m.
// The sender is excluded. Empty to is not a match. all matches every other name.
func addressed(m store.Message, name string) bool {
	if m.From == name {
		return false
	}
	if len(m.To) == 1 && m.To[0] == valueAll {
		return true
	}
	for _, n := range m.To {
		if n == name {
			return true
		}
	}
	return false
}

func validatePublish(in PublishInput, maxBody int) error {
	if err := requireConversation(in.Conversation); err != nil {
		return err
	}
	if in.From == "" {
		return rule(ReasonFromRequired)
	}
	if in.From == valueAll {
		return rule(ReasonFromAll)
	}
	if !validName(in.From) {
		return rule(ReasonFromBad)
	}
	if err := validateTo(in.To); err != nil {
		return err
	}
	if in.BodyMissing {
		return rule(ReasonBodyRequired)
	}
	if !utf8.ValidString(in.Body) {
		return rule(ReasonBodyUTF8)
	}
	if len(in.Body) > maxBody {
		return rule(ReasonBodySize)
	}
	if in.Key == "" {
		return rule(ReasonKeyRequired)
	}
	if !utf8.ValidString(in.Key) {
		return rule(ReasonKeyUTF8)
	}
	return nil
}
