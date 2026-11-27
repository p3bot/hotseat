// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bus

import (
	"errors"

	"github.com/p3bot/hotseat/internal/store"
)

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

// ChatClosed is the status a binding carries once the chat is closed.
const ChatClosed = store.StatusClosed

// Reasons are the exact text of the reason field on a refused call.
const (
	ReasonBadName              = "name must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonNameRequired         = "name is required"
	ReasonChatRequired         = "chat is required"
	ReasonBadChat              = "chat must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonNotFound             = "chat not found"
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
	ReasonKindRequired         = "kind is required"
	ReasonKindBad              = "kind must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonReRange              = "re must be an integer greater than zero"
	ReasonReTarget             = "re must be the seq of an earlier message of the same chat"
	ReasonKindsShape           = "kinds must be an array of names"
	ReasonKindsEmpty           = "kinds must not be empty"
	ReasonKindsBadName         = "kinds contains a name that is not 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonRosterShape          = "roster must be an array of objects, each with a string name and optional string module and model"
	ReasonRosterBadName        = "roster contains a name that is not 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonRosterDuplicate      = "roster contains a duplicate name"
	ReasonSeatNotInRoster      = "seat must be one of the roster names"
	ReasonSeatNeedsRoster      = "a seat requires a roster"
	ReasonPromptsShape         = "prompts must be an array of strings"
	ReasonPromptEmpty          = "prompts contains an empty string"
	ReasonNotInRoster          = "name is not in the roster"
	ReasonMemberMissing        = "member has not been launched"
	ReasonStatusBad            = "status must be launched, running, exited, or timed-out"
	ReasonAlreadyLaunched      = "member is already launched"
	ReasonPID                  = "pid must be an integer greater than zero"
	ReasonPIDNeedsStatus       = "a pid requires a status"
	ReasonStart                = "start must be 1 to 128 printable ASCII characters with no spaces"
	ReasonStartNeedsPID        = "a start requires a pid"
	ReasonSessionEmpty         = "session must not be empty"
	ReasonRecordRequired       = "record is required"
	ReasonRecordBad            = "record must be set, grouping, settle, fix, halt, or stop"
	ReasonTurnRequired         = "turn is required"
	ReasonTurnRange            = "turn must be an integer greater than zero"
	ReasonFindingsRequired     = "findings is required"
	ReasonFindingsShape        = "findings must be an array of integers greater than zero"
	ReasonFindingsRepeat       = "findings contains a repeated seq"
	ReasonDropsRequired        = "drops is required"
	ReasonDropsShape           = "drops must be an array of objects whose finding and kept are integers greater than zero"
	ReasonDropsRepeat          = "drops contains a repeated finding"
	ReasonFindingRequired      = "finding is required"
	ReasonFindingRange         = "finding must be an integer greater than zero"
	ReasonProposalRequired     = "proposal is required"
	ReasonProposalRange        = "proposal must be an integer greater than zero"
	ReasonProposalKindRequired = "proposal_kind is required"
	ReasonProposalKindBad      = "proposal_kind must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonFixedRequired        = "fixed is required"
	ReasonFixedRange           = "fixed must be an integer greater than zero"
	ReasonDoneRequired         = "done is required"
	ReasonDoneRange            = "done must be an integer greater than zero"
	ReasonRecordReference      = "record breaks a reference, kind, or uniqueness rule"
	ReasonKeptDropped          = "a kept finding must not itself be dropped"
	ReasonNotKept              = "finding must be a kept finding of a set that has a grouping"
	ReasonFixDismissed         = "a fix must belong to a settle whose proposal is not a dismiss"
	ReasonLeaseRequired        = "lease is required"
	ReasonLeaseBad             = "lease must be acquire, renew, release, or get"
	ReasonHolderRequired       = "holder is required"
	ReasonHolderBad            = "holder must be 1 to 64 characters from ASCII letters, digits, '.', '_', and '-'"
	ReasonConductorHeld        = "chat has a conductor"
	ReasonLeaseNotHeld         = "lease is not held"
	ReasonLeaseWrite           = "write requires the chat lease"
	ReasonCursorRequired       = "cursor is required"
	ReasonCursorRange          = "cursor must be an integer greater than or equal to zero"
	ReasonLimitRequired        = "limit is required"
	ReasonLimitRange           = "limit must be a positive integer"
	ReasonLimitCap             = "limit must be at most 1000"
	ReasonBeforeRange          = "before must be an integer greater than zero"
	ReasonDeadline             = "deadline must be a non-negative duration such as 30s or 500ms, or an RFC3339 time"
	ReasonBadJSON              = "request is not valid JSON"
	ReasonUnknownOp            = "unknown operation"
	ReasonTokenRequired        = "token is required"
	ReasonTokenRejected        = "token does not match"
	ReasonUnavailable          = "could not make the change durable"
)

// MaxReadLimit is the largest read limit. A body can be 512 KiB, so even a full page can be large.
const MaxReadLimit = 1000

// MaxNameLen is the length limit for a chat, a participant, a kind, a kinds entry, and a roster name.
const MaxNameLen = 64

const valueAll = "all"

// PathCreate and the routes beside it are the listener operations.
// PathHealth is the unauthenticated pass or fail check. It is not an operation.
const (
	PathCreate   = "/create"
	PathSend     = "/send"
	PathRead     = "/read"
	PathWait     = "/wait"
	PathWatch    = "/watch"
	PathClose    = "/close"
	PathList     = "/list"
	PathMessages = "/messages"
	PathJoin     = "/join"
	PathMember   = "/member"
	PathSession  = "/session"
	PathMembers  = "/members"
	PathRecord   = "/record"
	PathRecords  = "/records"
	PathLease    = "/lease"
	PathHealth   = "/health"
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
