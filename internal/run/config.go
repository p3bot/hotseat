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
	// It is not a limit on the chat. A zero Config.Timeout disables it.
	DefaultProcessLimit = 8 * time.Hour
	// DefaultPermission is the permission New stores when Setup gives none. Every member launch takes the stored one.
	DefaultPermission = "auto"
	// DoneGrace is how long the sender of a stopping done may run before it is killed.
	// It is not a setting. Other running members are stopped at once.
	DoneGrace = 30 * time.Second
)

// Config is the settings of one run process, for New and for Continue. None of them is stored with the chat, and a
// continuation takes its own. A Set field is false when the caller omitted that setting, which then takes its default.
//
// Timeout limits one member process. Zero with TimeoutSet means no limit, and a negative one is refused.
//
// MaxSays is each member's discussion launches per finding. When the speaker
// order finds nobody and the finding has not settled, the acting seat decides.
//
// MaxLaunches counts every member process this process starts, of any kind.
// At the cap, and when a member failure leaves fewer live members than the
// task's min_members, run starts no further launch, lets running members
// finish, and returns an error with the chat open. A stopping done
// still closes it.
//
// Address and TokenFile name the bus. Each member process gets HOTSEAT_ADDRESS and, when TokenFile is set,
// HOTSEAT_TOKEN_FILE as its absolute path against Dir.
type Config struct {
	Timeout    time.Duration
	TimeoutSet bool

	MaxTurns    int
	MaxTurnsSet bool

	MaxLaunches    int
	MaxLaunchesSet bool

	MaxSays    int
	MaxSaysSet bool

	// Verbose writes one event line per engine event to stderr, and forwards a
	// successful start or tk call's stderr with each line prefixed by the tool name.
	Verbose bool

	Address      string
	TokenFile    string
	TokenFileSet bool

	// Dir is the directory run was started in.
	Dir string
	// StartBin and TkBin are the programs to exec. Empty means start and tk.
	StartBin string
	TkBin    string
	// Random mints chat codes. Nil uses crypto/rand.
	Random io.Reader
	// Clock reads the time elapsed is measured on. Nil uses time.Now.
	Clock func() time.Time
}

// Setup is what New stores with the chat it creates, and what Continue launches from.
// Agents is a space-separated roster of name=module:model and is required, as is Task.
// An empty Seat is the first roster name in ASCII byte order, and an empty Permission is DefaultPermission.
// Ticket, Prompts, and Instructions may be empty.
type Setup struct {
	Task         string
	Agents       string
	Seat         string
	Ticket       string
	Prompts      string
	Instructions string
	Permission   string
}

func (c Config) timeout() time.Duration {
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
