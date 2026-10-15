// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"io"
	"time"
)

const (
	// DefaultMaxTurns is the number of turn messages one process acts on.
	DefaultMaxTurns = 500
	// DefaultProcessLimit is the limit for one member process.
	// It is not a limit on the conversation. Interactive mode disables it.
	DefaultProcessLimit = 8 * time.Hour
	// DefaultPermission is passed to every member launch.
	DefaultPermission = "auto"
)

// Config is one run invocation.
// A Set field is false when the caller omitted that setting.
// Omitted settings are not compared on --conversation.
// An omitted roster continues, and a launch needs the roster passed again.
// Timeout, MaxTurns, Interactive, Permission, Address, and TokenFile are
// settings of this process. They are not stored and they are not compared.
type Config struct {
	Task    string
	TaskSet bool

	Agents    string
	AgentsSet bool

	Seat    string
	SeatSet bool

	Prompts    string
	PromptsSet bool

	Instructions    string
	InstructionsSet bool

	Timeout    time.Duration
	TimeoutSet bool

	MaxTurns    int
	MaxTurnsSet bool

	Interactive    bool
	InteractiveSet bool

	Ticket    string
	TicketSet bool

	Conversation    string
	ConversationSet bool

	Address       string
	TokenFile     string
	TokenFileSet  bool
	Permission    string
	PermissionSet bool

	// Dir is the directory run was started in.
	Dir string
	// StartBin and TkBin are the programs to exec. Empty means start and tk.
	StartBin string
	TkBin    string
	// Random mints conversation codes. Nil uses crypto/rand.
	Random io.Reader
}

func (c Config) timeout() time.Duration {
	if c.Interactive {
		return 0
	}
	if c.TimeoutSet {
		return c.Timeout
	}
	return DefaultProcessLimit
}

func (c Config) maxTurns() int {
	if c.MaxTurnsSet {
		return c.MaxTurns
	}
	return DefaultMaxTurns
}

func (c Config) permission() string {
	if c.PermissionSet || c.Permission != "" {
		return c.Permission
	}
	return DefaultPermission
}
