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
	// DefaultMaxLaunches is the number of member processes one run process starts.
	DefaultMaxLaunches = 300
	// DefaultMaxSays is the number of discussion launches each member has per finding.
	DefaultMaxSays = 4
	// DefaultProcessLimit is the limit for one member process.
	// It is not a limit on the conversation. Interactive mode disables it.
	DefaultProcessLimit = 8 * time.Hour
	// DefaultPermission is passed to every member launch.
	DefaultPermission = "auto"
	// DoneGrace is how long the sender of a stopping done may run before it is killed.
	// It is not a setting. Other running members are stopped at once.
	DoneGrace = 30 * time.Second
)

// Config is one run invocation.
// A Set field is false when the caller omitted that setting.
// Omitted settings are not compared on --conversation.
// An omitted roster continues, and a launch needs the roster passed again.
// Timeout, MaxTurns, MaxLaunches, MaxSays, Interactive, Verbose, Permission,
// Address, and TokenFile are settings of this process. They are not stored and
// they are not compared.
//
// MaxSays is each member's discussion launches per finding. When the speaker
// order finds nobody and the finding has not settled, the acting seat decides.
//
// MaxLaunches counts every member process this process starts, of any kind.
// At the cap, and when a member failure leaves fewer live members than the
// task's min_members, run starts no further launch, lets running members
// finish, and returns an error with the conversation open. A stopping done
// still closes it.
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

	MaxLaunches    int
	MaxLaunchesSet bool

	MaxSays    int
	MaxSaysSet bool

	Interactive    bool
	InteractiveSet bool

	// Verbose writes one event line per engine event to stderr, and forwards a
	// successful start or tk call's stderr with each line prefixed by the tool name.
	Verbose bool

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
	// Clock reads the time elapsed is measured on. Nil uses time.Now.
	Clock func() time.Time
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

func (c Config) maxLaunches() int {
	if c.MaxLaunchesSet {
		return c.MaxLaunches
	}
	return DefaultMaxLaunches
}

func (c Config) maxSays() int {
	if c.MaxSaysSet {
		return c.MaxSays
	}
	return DefaultMaxSays
}

func (c Config) permission() string {
	if c.PermissionSet || c.Permission != "" {
		return c.Permission
	}
	return DefaultPermission
}
