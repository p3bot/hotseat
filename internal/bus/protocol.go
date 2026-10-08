// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import "errors"

const (
	// OutcomeOK carries the result of the call.
	OutcomeOK = "ok"
	// OutcomeTimeout is a wait whose deadline passed with no match.
	OutcomeTimeout = "timeout"
	// OutcomeRefused means the call broke a rule and wrote nothing.
	OutcomeRefused = "refused"
	// OutcomeUnavailable means a change could not be made durable and wrote nothing.
	OutcomeUnavailable = "unavailable"
)

// Reasons are the exact text of the reason field on a refused call.
const (
	ReasonBadName              = "name must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonNameRequired         = "name is required"
	ReasonConversationRequired = "conversation is required"
	ReasonBadConversation      = "conversation must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonNotFound             = "conversation not found"
	ReasonFromRequired         = "from is required"
	ReasonFromAll              = "from must not be all"
	ReasonFromBad              = "from must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonToRequired           = "to is required"
	ReasonToShape              = "to must be an array of names"
	ReasonToAllMixed           = "all must not be combined with other names"
	ReasonToBadName            = "to contains a name that is not 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonBodyRequired         = "body is required"
	ReasonBodyUTF8             = "body is not valid UTF-8"
	ReasonBodySize             = "body exceeds the configured maximum"
	ReasonRequestUTF8          = "request is not valid UTF-8"
	ReasonRequestSize          = "request exceeds the maximum size"
	ReasonKeyRequired          = "transaction id is required"
	ReasonKeyUTF8              = "transaction id is not valid UTF-8"
	ReasonKeyConflict          = "transaction id reused with different content"
	ReasonCursorRequired       = "cursor is required"
	ReasonCursorRange          = "cursor must be an integer greater than or equal to zero"
	ReasonLimitRequired        = "limit is required"
	ReasonLimitRange           = "limit must be a positive integer"
	ReasonDeadline             = "deadline must be a non-negative duration such as 30s or 500ms, or an RFC3339 time"
	ReasonBadJSON              = "request is not valid JSON"
	ReasonUnknownOp            = "unknown operation"
	ReasonTokenRequired        = "token is required"
	ReasonTokenRejected        = "token does not match"
	ReasonUnavailable          = "could not make the change durable"
)

// MaxNameLen is the conversation, from, and to name limit.
const MaxNameLen = 64

const valueAll = "all"

// PathCreate and the routes beside it are the listener operations.
const (
	PathCreate  = "/create"
	PathPublish = "/publish"
	PathRead    = "/read"
	PathWait    = "/wait"
	PathClose   = "/close"
	PathList    = "/list"
)

type ruleError struct {
	reason string
}

func (e *ruleError) Error() string { return e.reason }

func rule(reason string) error { return &ruleError{reason: reason} }

func reasonOf(err error) (string, bool) {
	var re *ruleError
	if errors.As(err, &re) {
		return re.reason, true
	}
	return "", false
}
