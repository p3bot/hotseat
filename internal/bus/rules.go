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

// waitLimit is the caller's deadline.
// set is false when the field was omitted or empty.
// abs is an RFC3339 end time in at. Otherwise rel is a duration from arrival.
type waitLimit struct {
	set bool
	abs bool
	at  time.Time
	rel time.Duration
}

// remaining is how long this call may still block, measured on the bus clock.
// An absolute end time already past is zero.
func (w waitLimit) remaining(now time.Time) time.Duration {
	if w.abs {
		d := w.at.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	return w.rel
}

func parseDeadline(raw json.RawMessage) (waitLimit, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return waitLimit{}, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return waitLimit{}, rule(ReasonDeadline)
	}
	if text == "" {
		return waitLimit{}, nil
	}
	d, err := time.ParseDuration(text)
	if err == nil {
		if d < 0 {
			return waitLimit{}, rule(ReasonDeadline)
		}
		return waitLimit{set: true, rel: d}, nil
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return waitLimit{}, rule(ReasonDeadline)
	}
	return waitLimit{set: true, abs: true, at: at}, nil
}

// decideWait reports the wait result. pending is true when the caller must block.
// msgs are the messages with seq greater than the cursor, oldest first.
// Conversation status is not an end. A wait ends on a match.
func decideWait(msgs []store.Message, name string, hasName bool, kinds []string, hasKinds bool) (Result, bool) {
	if !hasName {
		for _, m := range msgs {
			if !kindListed(m.Kind, kinds, hasKinds) {
				continue
			}
			return Result{
				Outcome:  OutcomeOK,
				Messages: []store.Message{m},
				MatchSeq: m.Seq,
			}, false
		}
		return Result{}, true
	}
	var span []store.Message
	for _, m := range msgs {
		span = append(span, m)
		if addressed(m, name) && kindListed(m.Kind, kinds, hasKinds) {
			return Result{Outcome: OutcomeOK, Messages: span, MatchSeq: m.Seq}, false
		}
	}
	return Result{}, true
}

// nextRead reports whether m belongs in a read result and whether a later message is required.
// kept is how many messages are already chosen. A named read skips a message that is not addressed to name.
// A kinds list skips a message whose kind is not listed. Skipped messages do not count toward limit.
func nextRead(m store.Message, name string, hasName bool, kinds []string, hasKinds bool, kept, limit int) (keep, more bool) {
	if hasName && !addressed(m, name) {
		return false, true
	}
	if !kindListed(m.Kind, kinds, hasKinds) {
		return false, true
	}
	return true, kept+1 < limit
}

func selectRead(msgs []store.Message, name string, hasName bool, kinds []string, hasKinds bool, limit int) []store.Message {
	out := make([]store.Message, 0)
	for _, m := range msgs {
		keep, more := nextRead(m, name, hasName, kinds, hasKinds, len(out), limit)
		if keep {
			out = append(out, m)
		}
		if !more {
			break
		}
	}
	return out
}

func kindListed(kind string, kinds []string, hasKinds bool) bool {
	if !hasKinds {
		return true
	}
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
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
	if in.TxID == "" {
		return rule(ReasonKeyRequired)
	}
	if !utf8.ValidString(in.TxID) {
		return rule(ReasonKeyUTF8)
	}
	if in.Kind == "" {
		return rule(ReasonKindRequired)
	}
	if !validName(in.Kind) {
		return rule(ReasonKindBad)
	}
	return nil
}

func validateBinding(b store.Binding) error {
	seen := make(map[string]struct{}, len(b.Roster))
	for _, n := range b.Roster {
		if !validName(n) {
			return rule(ReasonRosterBadName)
		}
		if _, ok := seen[n]; ok {
			return rule(ReasonRosterDuplicate)
		}
		seen[n] = struct{}{}
	}
	for _, p := range b.Prompts {
		if p == "" {
			return rule(ReasonPromptEmpty)
		}
	}
	if len(b.Roster) == 0 {
		if b.Seat != "" {
			return rule(ReasonSeatNeedsRoster)
		}
		return nil
	}
	if _, ok := seen[b.Seat]; !ok {
		return rule(ReasonSeatNotInRoster)
	}
	return nil
}

func validateKinds(kinds []string, has bool) error {
	if !has {
		return nil
	}
	if len(kinds) == 0 {
		return rule(ReasonKindsEmpty)
	}
	for _, k := range kinds {
		if !validName(k) {
			return rule(ReasonKindsBadName)
		}
	}
	return nil
}

// parseKinds reports the kinds filter. An absent field is not a filter.
// null, a non-array, an empty array, and a name outside the grammar are refused.
func parseKinds(raw json.RawMessage) (kinds []string, has bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, true, rule(ReasonKindsShape)
	}
	names, err := decodeStringList(trimmed, ReasonKindsShape)
	if err != nil {
		return nil, true, err
	}
	if err := validateKinds(names, true); err != nil {
		return nil, true, err
	}
	return names, true, nil
}

func parseRoster(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		if bytes.Equal(trimmed, []byte("null")) {
			return nil, rule(ReasonRosterShape)
		}
		return []string{}, nil
	}
	return decodeStringList(trimmed, ReasonRosterShape)
}

func parsePrompts(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []string{}, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, rule(ReasonPromptsShape)
	}
	return decodeStringList(trimmed, ReasonPromptsShape)
}

func decodeStringList(raw []byte, shape string) ([]string, error) {
	var names []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&names); err != nil || dec.More() {
		return nil, rule(shape)
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

// parsePID reports an optional pid. Absent and null are unset.
// Any other value that is not an integer greater than zero is refused.
func parsePID(raw json.RawMessage) (pid int64, has bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false, nil
	}
	if trimmed[0] == '"' { // json.Number accepts a JSON string.
		return 0, true, rule(ReasonPID)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err != nil || dec.More() {
		return 0, true, rule(ReasonPID)
	}
	i, err := n.Int64()
	if err != nil || i <= 0 {
		return 0, true, rule(ReasonPID)
	}
	return i, true, nil
}
