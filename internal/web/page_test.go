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

	"github.com/p3bot/hotseat/internal/client"
)

func TestTabTitleNamesTheProduct(t *testing.T) {
	if got := (page{}).TabTitle(); got != "Hotseat" {
		t.Fatalf("list %q", got)
	}
	if got := (page{Name: "job"}).TabTitle(); got != "Hotseat - job" {
		t.Fatalf("conversation %q", got)
	}
}

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
	for i := 0; i < 5; i++ {
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

func TestFailedOlderGroupKeepsTheMarker(t *testing.T) {
	open := bytes.Buffer{}
	page := conversationPage("job")
	page.Read = &readView{
		Outcome:  "ok",
		Messages: []client.Message{{Body: "newest"}},
		Full:     true,
		NextPage: "/c?conversation=job&before=3",
	}
	if err := pageTmpl.Execute(&open, page); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(open.String(), `id="older-error"`) {
		t.Fatal("full group has no place to show a failed load")
	}
	script, err := pageScript(open.String())
	if err != nil {
		t.Fatal(err)
	}

	const marker = "/c?conversation=job&before=3"
	cases := []struct {
		name    string
		job     string
		marker  string
		message string
		added   string
	}{
		{name: "notice", job: `{ok:true, outcome:"", notice:"bus down", reason:"", next:"", bodies:1}`, marker: marker, message: "bus down", added: "0"},
		{name: "refused", job: `{ok:true, outcome:"refused", notice:"", reason:"closed", next:"", bodies:1}`, marker: marker, message: "refused closed", added: "0"},
		{name: "status", job: `{ok:false, outcome:"", notice:"", reason:"", next:"", bodies:0}`, marker: marker, message: "older messages did not load", added: "0"},
		{name: "network", job: `{network:true}`, marker: marker, message: "older messages did not load", added: "0"},
		{name: "end", job: `{ok:true, outcome:"ok", notice:"", reason:"", next:"", bodies:1}`, message: "", added: "1"},
		{name: "next", job: `{ok:true, outcome:"ok", notice:"", reason:"", next:"/c?conversation=job&before=1", bodies:1}`, marker: "/c?conversation=job&before=1", added: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := olderLoadVM(t, script)
			if _, err := vm.RunString("__queue = [" + tc.job + "]; __loadOlder()"); err != nil {
				t.Fatal(err)
			}
			if got := scriptString(t, vm, "__marker()"); got != tc.marker {
				t.Fatalf("marker %q", got)
			}
			if got := scriptString(t, vm, "__olderError()"); got != tc.message {
				t.Fatalf("message %q", got)
			}
			if got := scriptString(t, vm, "__added()"); got != tc.added {
				t.Fatalf("added %q", got)
			}
		})
	}

	vm := olderLoadVM(t, script)
	if _, err := vm.RunString(`__queue = [
		{ok:true, outcome:"", notice:"bus down", reason:"", next:"", bodies:0},
		{ok:true, outcome:"ok", notice:"", reason:"", next:"", bodies:1}
	]; __loadOlder(); __loadOlder()`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__marker()") != "" || scriptString(t, vm, "__olderError()") != "" || scriptString(t, vm, "__added()") != "1" {
		t.Fatalf("marker %q message %q added %q", scriptString(t, vm, "__marker()"), scriptString(t, vm, "__olderError()"), scriptString(t, vm, "__added()"))
	}
}

func olderLoadVM(t *testing.T, script string) *goja.Runtime {
	t.Helper()
	vm := goja.New()
	if _, err := vm.RunString(olderLoadHarness); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	return vm
}

const olderLoadHarness = `
function Promise(executor) {
  if (!(this instanceof Promise)) return new Promise(executor);
  this.state = "pending";
  this.value = undefined;
  this.done = [];
  var self = this;
  function settle(state, value) {
    if (self.state !== "pending") return;
    self.state = state;
    self.value = value;
    var wait = self.done.slice();
    self.done = [];
    for (var i = 0; i < wait.length; i++) wait[i]();
  }
  this.then = function (onOk, onErr) {
    return new Promise(function (resolve, reject) {
      function run() {
        try {
          var fn = self.state === "fulfilled" ? onOk : onErr;
          if (typeof fn !== "function") {
            if (self.state === "fulfilled") resolve(self.value);
            else reject(self.value);
            return;
          }
          var out = fn(self.value);
          if (out && typeof out.then === "function") out.then(resolve, reject);
          else resolve(out);
        } catch (err) {
          reject(err);
        }
      }
      if (self.state === "pending") self.done.push(run);
      else run();
    });
  };
  this.catch = function (onErr) { return this.then(null, onErr); };
  try {
    executor(function (v) { settle("fulfilled", v); }, function (e) { settle("rejected", e); });
  } catch (err) {
    settle("rejected", err);
  }
}
var __queue = [];
var __current = null;
function fetch() {
  var job = __queue.shift();
  __current = job;
  return new Promise(function (resolve, reject) {
    if (!job || job.network) {
      reject(new Error("network"));
      return;
    }
    resolve({
      ok: job.ok !== false,
      text: function () { return new Promise(function (resolveText) { resolveText("page"); }); }
    });
  });
}
function DOMParser() {}
DOMParser.prototype.parseFromString = function () {
  var page = __current || {};
  var items = [];
  for (var i = 0; i < (page.bodies || 0); i++) items.push({});
  function textNode(text) {
    if (!text) return null;
    return { textContent: text };
  }
  return {
    getElementById: function (id) {
      if (id === "notice") return textNode(page.notice);
      if (id === "read-outcome") return textNode(page.outcome);
      if (id === "read-reason") return textNode(page.reason);
      if (id === "transcript") {
        if (page.outcome !== "ok") return null;
        return { querySelectorAll: function () { return items; } };
      }
      if (id === "more") {
        if (!page.next) return null;
        return { getAttribute: function (k) { return k === "data-next" ? page.next : null; } };
      }
      return null;
    }
  };
};
var __onIntersect = null;
function IntersectionObserver(cb) { __onIntersect = cb; }
IntersectionObserver.prototype.observe = function () {};
function __loadOlder() { __onIntersect([{ isIntersecting: true }]); }
var __elements = {};
function __node(id, attrs) {
  var node = {
    id: id,
    attrs: attrs,
    textContent: "",
    children: [],
    parentNode: null,
    getAttribute: function (k) {
      if (!Object.prototype.hasOwnProperty.call(this.attrs, k)) return null;
      return this.attrs[k];
    },
    setAttribute: function (k, v) { this.attrs[k] = v; },
    removeAttribute: function (k) { delete this.attrs[k]; },
    getBoundingClientRect: function () { return { top: 200, bottom: 201 }; },
    appendChild: function (child) { this.children.push(child); }
  };
  __elements[id] = node;
  return node;
}
__node("read-form", {});
__elements["read-form"].addEventListener = function (type, fn) { this.onsubmit = fn; };
__node("publish-button", {});
__node("txid", {});
__elements.txid.value = "kept";
__node("transcript", {});
__node("older-error", {});
__node("more", { "data-next": "/c?conversation=job&before=3" });
var document = {
  getElementById: function (id) { return __elements[id] || null; },
  importNode: function (node) { return node; },
  createElement: function () { return { textContent: "" }; }
};
var window = { innerHeight: 100 };
function __marker() {
  var v = __elements.more.getAttribute("data-next");
  return v === null || v === undefined ? "" : v;
}
function __olderError() { return __elements["older-error"].textContent; }
function __added() { return String(__elements.transcript.children.length); }
`
