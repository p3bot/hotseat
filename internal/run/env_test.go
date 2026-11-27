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

func TestMemberEnvReplacesInheritedBusVariablesAndRemovesATokenFileRunDoesNotHave(t *testing.T) {
	inherited := []string{"PATH=/bin", envAddress + "=old:1", envTokenFile + "=/old/token", envAddress + "_OTHER=kept"}
	bare := []string{"PATH=/bin"}
	for _, tc := range []struct {
		name      string
		base      []string
		tokenFile string
		want      []string
	}{
		{"inherited, token file", inherited, "/new/token",
			[]string{"PATH=/bin", envAddress + "_OTHER=kept", envAddress + "=new:2", envTokenFile + "=/new/token"}},
		{"inherited, no token file", inherited, "",
			[]string{"PATH=/bin", envAddress + "_OTHER=kept", envAddress + "=new:2"}},
		{"not inherited, token file", bare, "/new/token",
			[]string{"PATH=/bin", envAddress + "=new:2", envTokenFile + "=/new/token"}},
		{"not inherited, no token file", bare, "",
			[]string{"PATH=/bin", envAddress + "=new:2"}},
	} {
		if got := memberEnv(tc.base, "new:2", tc.tokenFile); !slices.Equal(got, tc.want) {
			t.Errorf("%s\ngot:  %q\nwant: %q", tc.name, got, tc.want)
		}
	}
}
