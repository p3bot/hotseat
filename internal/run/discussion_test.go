// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"slices"
	"testing"
)

// say spends one launch of name and applies its stance.
func say(t *testing.T, d *discussion, name, kind string, seq int64) bool {
	t.Helper()
	d.spend(name)
	return d.stance(name, kind, seq)
}

func wantNext(t *testing.T, d *discussion, want string) {
	t.Helper()
	got, ok := d.next()
	if want == "" {
		if ok {
			t.Fatalf("next %s, want a stall", got)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("next %q %v, want %s", got, ok, want)
	}
}

func TestDisputedFindingStallsAndSettlesAfterExtend(t *testing.T) {
	d := newDiscussion([]string{"a1", "a2", "a3"}, 4, 10, "a1")
	if slices.Compare(d.required(), []string{"a2", "a3"}) != 0 {
		t.Fatalf("required %v", d.required())
	}
	wantNext(t, d, "a2")
	if !say(t, d, "a2", kindPropose, 20) || d.proposer != "a2" || d.proposal != 20 {
		t.Fatalf("propose not taken: %+v", d)
	}
	if slices.Compare(d.required(), []string{"a1", "a3"}) != 0 {
		t.Fatalf("required %v", d.required())
	}
	if !d.passable("a2", "a1") {
		t.Fatal("a1 is not passable")
	}
	if !say(t, d, "a1", kindObject, 21) {
		t.Fatal("object refused")
	}
	wantNext(t, d, "a3")
	if !say(t, d, "a3", kindAgree, 22) {
		t.Fatal("agree refused")
	}
	seq := int64(23)
	for round := 0; round < 3; round++ {
		wantNext(t, d, "a2")
		if !say(t, d, "a2", kindStand, seq) {
			t.Fatalf("stand %d refused", round)
		}
		wantNext(t, d, "a1")
		if !say(t, d, "a1", kindObject, seq+1) {
			t.Fatalf("object %d refused", round)
		}
		seq += 2
	}
	if d.budget["a1"] != 0 || d.budget["a2"] != 0 || !d.objectionOpen() {
		t.Fatalf("budgets %v open %v", d.budget, d.objectionOpen())
	}
	wantNext(t, d, "")
	if slices.Compare(d.outOfBudget(), []string{"a1", "a2"}) != 0 {
		t.Fatalf("out of budget %v", d.outOfBudget())
	}
	agreed, not := d.split()
	if slices.Compare(agreed, []string{"a3"}) != 0 || slices.Compare(not, []string{"a1"}) != 0 {
		t.Fatalf("agreed %v not %v", agreed, not)
	}
	if refreshed, rejected := d.extend([]string{"a1", "a2"}); len(refreshed) != 2 || len(rejected) != 0 {
		t.Fatalf("refreshed %v rejected %v", refreshed, rejected)
	}
	wantNext(t, d, "a2")
	if !say(t, d, "a2", kindStand, seq) {
		t.Fatal("stand after extend refused")
	}
	wantNext(t, d, "a1")
	if !say(t, d, "a1", kindAgree, seq+1) {
		t.Fatal("agree refused")
	}
	if !d.settled() || d.proposal != 20 || !d.change() || d.finder != "a1" {
		t.Fatalf("settled %v proposal %d change %v finder %s", d.settled(), d.proposal, d.change(), d.finder)
	}
}

func TestProposeClearsAgreementsAndStandKeepsThem(t *testing.T) {
	d := newDiscussion([]string{"a", "b", "c", "d"}, 4, 1, "a")
	say(t, d, "b", kindAgree, 2)
	say(t, d, "c", kindObject, 3)
	say(t, d, "a", kindStand, 4)
	if !d.agreed["b"] || d.objectionOpen() {
		t.Fatalf("stand dropped an agreement or kept the objection: %v %v", d.agreed, d.objecting)
	}
	wantNext(t, d, "c")
	say(t, d, "d", kindPropose, 5)
	if len(d.agreed) != 0 || d.proposer != "d" || d.kind != kindPropose {
		t.Fatalf("propose kept agreements: %v", d.agreed)
	}
	say(t, d, "a", kindDismiss, 6)
	if d.proposer != "a" || d.change() {
		t.Fatalf("dismiss: proposer %s change %v", d.proposer, d.change())
	}
}

func TestStanceThatBreaksTheProposerRuleChangesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*discussion)
		from  string
		kind  string
	}{
		{name: "proposer agree", from: "a", kind: kindAgree},
		{name: "proposer object", from: "a", kind: kindObject},
		{name: "stand with no open objection", from: "a", kind: kindStand},
		{name: "stand from a member who is not the proposer", setup: func(d *discussion) { d.stance("b", kindObject, 2) }, from: "c", kind: kindStand},
		{name: "unknown kind", from: "b", kind: "say"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDiscussion([]string{"a", "b", "c"}, 4, 1, "a")
			if tc.setup != nil {
				tc.setup(d)
			}
			before := snapshot(d)
			if d.stance(tc.from, tc.kind, 9) {
				t.Fatal("stance accepted")
			}
			if after := snapshot(d); after != before {
				t.Fatalf("state changed\n%s\n%s", before, after)
			}
		})
	}
}

func snapshot(d *discussion) string {
	var s string
	s += d.proposer + "/" + d.kind
	for _, name := range d.roster {
		s += " " + name
		if d.agreed[name] {
			s += "+agreed"
		}
		if d.objecting[name] {
			s += "+objecting"
		}
		if d.spoke[name] {
			s += "+spoke"
		}
	}
	return s
}

func TestProposerTimeoutClosesObjectionsAndKeepsAgreements(t *testing.T) {
	d := newDiscussion([]string{"a", "b", "c"}, 4, 1, "a")
	say(t, d, "b", kindAgree, 2)
	say(t, d, "c", kindObject, 3)
	wantNext(t, d, "a")
	d.markDead("a")
	if d.objectionOpen() || !d.agreed["b"] {
		t.Fatalf("objection %v agreed %v", d.objectionOpen(), d.agreed)
	}
	// The objector speaks again and can change the proposal.
	wantNext(t, d, "c")
	say(t, d, "c", kindObject, 4)
	// Nobody answers an objection against a timed-out proposer, so it stalls.
	wantNext(t, d, "")
}

func TestSoleSurvivorSettlesAtOnce(t *testing.T) {
	d := newDiscussion([]string{"a", "b"}, 4, 1, "a")
	d.markDead("b")
	if !d.settled() || len(d.required()) != 0 {
		t.Fatalf("settled %v required %v", d.settled(), d.required())
	}
}

func TestFirstSpeakerIsTheNextLiveMemberAfterTheFinder(t *testing.T) {
	cases := []struct {
		finder string
		dead   []string
		want   string
	}{
		{finder: "a", want: "b"},
		{finder: "c", want: "a"},
		{finder: "c", dead: []string{"a"}, want: "b"},
		{finder: "b", dead: []string{"b"}, want: "c"},
	}
	for _, tc := range cases {
		d := newDiscussion([]string{"a", "b", "c"}, 4, 1, tc.finder)
		for _, name := range tc.dead {
			d.markDead(name)
		}
		wantNext(t, d, tc.want)
	}
}

func TestPassTargetMustBeEligible(t *testing.T) {
	d := newDiscussion([]string{"a", "b", "c", "d"}, 2, 1, "a")
	say(t, d, "b", kindAgree, 2)
	d.markDead("d")
	d.budget["c"] = 0
	cases := []struct {
		from, to string
		want     bool
	}{
		{"b", "b", false},
		{"b", "z", false},
		{"c", "b", false},
		{"b", "c", false},
		{"b", "d", false},
		{"b", "a", false},
	}
	for _, tc := range cases {
		if got := d.passable(tc.from, tc.to); got != tc.want {
			t.Fatalf("pass %s to %s: %v", tc.from, tc.to, got)
		}
	}
	d.budget["c"] = 2
	if !d.passable("b", "c") {
		t.Fatal("c with budget is not passable")
	}
	say(t, d, "c", kindObject, 3)
	if !d.passable("c", "a") {
		t.Fatal("proposer facing an objection is not passable")
	}
}

func TestExtendRefreshesOnlyLiveRosterMembers(t *testing.T) {
	d := newDiscussion([]string{"a", "b", "c"}, 3, 1, "a")
	d.budget["b"] = 0
	d.budget["c"] = 0
	d.markDead("c")
	refreshed, rejected := d.extend([]string{"owner", "b", "c", "b", "owner"})
	if slices.Compare(refreshed, []string{"b"}) != 0 || slices.Compare(rejected, []string{"owner", "c"}) != 0 {
		t.Fatalf("refreshed %v rejected %v", refreshed, rejected)
	}
	if d.budget["b"] != 3 || d.budget["c"] != 0 {
		t.Fatalf("budgets %v", d.budget)
	}
}
