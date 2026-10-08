// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func TestPublishScriptMintsOnlyOnEmptyPublish(t *testing.T) {
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, conversationPage("job")); err != nil {
		t.Fatal(err)
	}
	script, err := pageScript(buf.String())
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(publishScriptHarness); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}

	got := firePublishScript(t, vm, "publish-button", "")
	want := scriptFixedHex()
	if got != want || scriptCalls(t, vm) != 1 {
		t.Fatalf("empty publish field %q calls %d, want %q and 1 call", got, scriptCalls(t, vm), want)
	}

	got = firePublishScript(t, vm, "publish-button", "keep-me")
	if got != "keep-me" || scriptCalls(t, vm) != 0 {
		t.Fatalf("filled publish field %q calls %d", got, scriptCalls(t, vm))
	}

	got = firePublishScript(t, vm, "read-button", "")
	if got != "" || scriptCalls(t, vm) != 0 {
		t.Fatalf("read field %q calls %d", got, scriptCalls(t, vm))
	}
}

func firePublishScript(t *testing.T, vm *goja.Runtime, button, value string) string {
	t.Helper()
	if _, err := vm.RunString(`__calls = 0; __setTxID(` + quoteJS(value) + `); __fire(` + quoteJS(button) + `)`); err != nil {
		t.Fatal(err)
	}
	return scriptString(t, vm, "__txid()")
}

func scriptCalls(t *testing.T, vm *goja.Runtime) int {
	t.Helper()
	v := scriptString(t, vm, "String(__calls)")
	var n int
	if _, err := fmt.Sscan(v, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func scriptString(t *testing.T, vm *goja.Runtime, expr string) string {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

func pageScript(html string) (string, error) {
	const open, close = "<script>", "</script>"
	i := strings.Index(html, open)
	if i < 0 {
		return "", errors.New("page has no script")
	}
	rest := html[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", errors.New("page script is not closed")
	}
	if strings.Contains(rest[j+len(close):], open) {
		return "", errors.New("page has more than one script")
	}
	return rest[:j], nil
}

func scriptFixedHex() string {
	var b strings.Builder
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&b, "%02x", (i*17+3)&255)
	}
	return b.String()
}

func quoteJS(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

const publishScriptHarness = `
var __calls = 0;
var __elements = {
  "read-form": {},
  "publish-button": {},
  "read-button": {},
  "txid": { value: "" }
};
__elements["read-form"].addEventListener = function (type, fn) {
  __elements["read-form"].onsubmit = fn;
};
function Uint8Array(n) {
  var a = new Array(n);
  for (var i = 0; i < n; i++) a[i] = 0;
  return a;
}
var crypto = {
  getRandomValues: function (buf) {
    __calls++;
    for (var i = 0; i < buf.length; i++) buf[i] = (i * 17 + 3) & 255;
    return buf;
  }
};
var document = {
  getElementById: function (id) {
    return __elements[id] || null;
  }
};
function __setTxID(v) { __elements.txid.value = v; }
function __fire(which) {
  __elements["read-form"].onsubmit({ submitter: __elements[which] });
}
function __txid() { return __elements.txid.value; }
`
