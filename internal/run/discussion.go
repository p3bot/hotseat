// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import "slices"

// discussion is the live state of one finding. It holds no bus or process, so a launch window feeds it one event at a time.
// A continuation never rebuilds it from stances. It starts over from the finding.
type discussion struct {
	roster  []string
	maxSays int

	finding int64
	finder  string
	// start is the seq this discussion began after. Stances at or before it belong to an earlier discussion or a run before a restart.
	start int64

	// proposal is the seq of the current proposal and kind is its message kind: finding, propose, or dismiss.
	proposal int64
	kind     string
	proposer string

	agreed    map[string]bool
	objecting map[string]bool
	// spoke is a valid stance since the latest of the proposal being set, the proposer's last stand, and the proposer's timeout.
	spoke  map[string]bool
	budget map[string]int
	dead   map[string]bool
	last   string
}

func newDiscussion(roster []string, maxSays int, finding int64, finder string) *discussion {
	d := &discussion{
		roster:    roster,
		maxSays:   maxSays,
		finding:   finding,
		finder:    finder,
		proposal:  finding,
		kind:      kindFinding,
		proposer:  finder,
		agreed:    make(map[string]bool),
		objecting: make(map[string]bool),
		spoke:     make(map[string]bool),
		budget:    make(map[string]int),
		dead:      make(map[string]bool),
		last:      finder,
	}
	for _, name := range roster {
		d.budget[name] = maxSays
	}
	return d
}

// markDead records a timed-out member. A proposer's timeout counts as a stand, so objections close and agreements stay.
func (d *discussion) markDead(name string) {
	if d.dead[name] {
		return
	}
	d.dead[name] = true
	if name == d.proposer {
		clear(d.objecting)
		clear(d.spoke)
	}
}

// required is every live roster member except the proposer, in roster order.
func (d *discussion) required() []string {
	var out []string
	for _, name := range d.roster {
		if name != d.proposer && !d.dead[name] {
			out = append(out, name)
		}
	}
	return out
}

func (d *discussion) settled() bool {
	for _, name := range d.required() {
		if !d.agreed[name] {
			return false
		}
	}
	return true
}

func (d *discussion) objectionOpen() bool {
	for _, open := range d.objecting {
		if open {
			return true
		}
	}
	return false
}

// spend counts one discussion launch of name, whatever it published.
func (d *discussion) spend(name string) {
	d.budget[name]--
	d.last = name
}

// stance applies one stance from name and reports whether it is valid. An invalid stance changes nothing.
func (d *discussion) stance(name, kind string, seq int64) bool {
	if name == d.proposer {
		switch kind {
		case kindPropose, kindDismiss:
		case kindStand:
			if !d.objectionOpen() {
				return false
			}
			clear(d.objecting)
			clear(d.spoke)
			return true
		default:
			return false
		}
	}
	switch kind {
	case kindPropose, kindDismiss:
		d.proposal = seq
		d.kind = kind
		d.proposer = name
		clear(d.agreed)
		clear(d.objecting)
		clear(d.spoke)
	case kindAgree:
		d.agreed[name] = true
		d.objecting[name] = false
		d.spoke[name] = true
	case kindObject:
		d.agreed[name] = false
		d.objecting[name] = true
		d.spoke[name] = true
	default:
		return false
	}
	return true
}

func (d *discussion) eligible(name string) bool {
	if d.budget[name] <= 0 || d.dead[name] {
		return false
	}
	if name == d.proposer {
		return d.objectionOpen()
	}
	return slices.Contains(d.roster, name) && !d.agreed[name]
}

// passable reports whether from may pass the floor to to.
func (d *discussion) passable(from, to string) bool {
	return to != from && slices.Contains(d.roster, to) && d.eligible(to)
}

// next is the speaker after the last one, or false when the discussion has stalled.
// The caller checks settled first and applies an accepted pass before this rule.
func (d *discussion) next() (string, bool) {
	start := slices.Index(d.roster, d.last)
	for i := 1; i <= len(d.roster); i++ {
		name := d.roster[(start+i+len(d.roster))%len(d.roster)]
		if name == d.proposer || d.dead[name] || d.agreed[name] || d.spoke[name] || d.budget[name] <= 0 {
			continue
		}
		return name, true
	}
	if d.objectionOpen() && !d.dead[d.proposer] && d.budget[d.proposer] > 0 {
		return d.proposer, true
	}
	return "", false
}

// extend gives each live roster member in names a fresh budget. Both results keep the order of names, each name once.
func (d *discussion) extend(names []string) (refreshed, rejected []string) {
	for _, name := range names {
		if slices.Contains(refreshed, name) || slices.Contains(rejected, name) {
			continue
		}
		if !slices.Contains(d.roster, name) || d.dead[name] {
			rejected = append(rejected, name)
			continue
		}
		d.budget[name] = d.maxSays
		refreshed = append(refreshed, name)
	}
	return refreshed, rejected
}

// outOfBudget lists roster members with no launches left, in roster order.
func (d *discussion) outOfBudget() []string {
	var out []string
	for _, name := range d.roster {
		if d.budget[name] <= 0 {
			out = append(out, name)
		}
	}
	return out
}

// objectors lists members whose objection is open, in roster order.
func (d *discussion) objectors() []string {
	var out []string
	for _, name := range d.roster {
		if d.objecting[name] {
			out = append(out, name)
		}
	}
	return out
}

// split lists the required members who have agreed and who have not, in roster order.
func (d *discussion) split() (agreed, not []string) {
	for _, name := range d.required() {
		if d.agreed[name] {
			agreed = append(agreed, name)
		} else {
			not = append(not, name)
		}
	}
	return agreed, not
}

// change reports whether the current proposal asks for a fix. A settled dismiss does not.
func (d *discussion) change() bool {
	return d.kind != kindDismiss
}
