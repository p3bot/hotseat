// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

// LetterAlphabet and ShortIDAlphabet are the tk ticket-id alphabets.
// The first character of a conversation code is a letter. The other three
// are drawn from the full alphabet. Copied so run does not import tk.
const (
	LetterAlphabet   = "abcdefghjkmnpqrstuvwxyz"
	ShortIDAlphabet  = "abcdefghjkmnpqrstuvwxyz23456789"
	conversationCode = 4
)
