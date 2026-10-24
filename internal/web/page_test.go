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
	"time"

	"github.com/dop251/goja"

	"github.com/p3bot/hotseat/internal/client"
)

func TestViewTime(t *testing.T) {
	raw := "2026-10-17T12:30:27.069994715Z"
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.In(time.Local).Format("2006-01-02 15:04:05.000")
	if got := viewTime(raw); got != want {
		t.Fatalf("viewTime %q, want %q", got, want)
	}
	if got := viewTime("not-a-time"); got != "not-a-time" {
		t.Fatalf("unparsed %q", got)
	}
}

func TestRenderedMessageShowsLocalTimeKindAndSequence(t *testing.T) {
	raw := "2026-10-17T12:30:27.069994715Z"
	msg := client.Message{
		Seq: 4, Time: raw, From: "alice", To: []string{"bob"},
		Body: "hello", TxID: "idem", Kind: "say",
	}
	p := conversationPage("job")
	p.Read = &readView{Outcome: "ok", Messages: []client.Message{msg}}
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, p); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.In(time.Local).Format("2006-01-02 15:04:05.000")
	if strings.Count(html, want) != 1 || strings.Count(html, `datetime="`+raw+`"`) != 1 {
		t.Fatalf("time\n%s", html)
	}
	if strings.Count(html, `data-kind="say">say<`) != 1 || strings.Count(html, `class="seq-n">4<`) != 1 {
		t.Fatalf("kind or seq\n%s", html)
	}
	if strings.Contains(html, `id="read-outcome"`) {
		t.Fatalf("ok outcome shown\n%s", html)
	}
}

func TestMessageFooterKeepsSourceBesideRenderedMarkdown(t *testing.T) {
	p := conversationPage("job")
	p.Read = &readView{Outcome: "ok", Messages: []client.Message{{
		Seq: 4, From: "alice", Body: "**hello**", Kind: "instruction",
	}}}
	html := renderPage(t, p)
	rendered := strings.Index(html, "<strong>hello</strong>")
	source := strings.Index(html, `class="body source" hidden>**hello**</pre>`)
	actions := strings.Index(html, `<button type="button" class="markdown" aria-pressed="false" aria-label="Markdown" title="Markdown"><svg`)
	copyTitle := strings.Index(html, `class="copy" aria-label="Copy markdown" title="Copy markdown"`)
	if copyTitle < 0 {
		t.Fatalf("copy tooltip\n%s", html)
	}
	when := strings.Index(html, `class="when"`)
	kind := strings.Index(html, `class="seq-n">4</span><span class="kind" data-kind="instruction">instruction</span>`)
	if rendered < 0 || source < rendered || actions < 0 || when < 0 || actions < when || kind < source {
		t.Fatalf("message\n%s", html)
	}
}

func TestMessageControlsCopySourceAndToggleView(t *testing.T) {
	script, err := pageScript(renderPage(t, conversationPage("job")))
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(messageControlHarness); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(`__click("svg")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__copiedText()") != "line\n**two**" || scriptString(t, vm, "__label()") != "Copied" || scriptString(t, vm, "__title()") != "Copied" || scriptString(t, vm, "__copyClass()") != "copied" {
		t.Fatalf("copy text %q label %q class %q", scriptString(t, vm, "__copiedText()"), scriptString(t, vm, "__label()"), scriptString(t, vm, "__copyClass()"))
	}
	if _, err := vm.RunString(`__flush()`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__label()") != "Copy markdown" || scriptString(t, vm, "__title()") != "Copy markdown" || scriptString(t, vm, "__copyClass()") != "" {
		t.Fatalf("restore label %q class %q", scriptString(t, vm, "__label()"), scriptString(t, vm, "__copyClass()"))
	}
	if _, err := vm.RunString(`__failCopy(); __click("svg")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__label()") != "Copy failed" || scriptString(t, vm, "__title()") != "Copy failed" || scriptString(t, vm, "__copyClass()") != "" {
		t.Fatalf("fail label %q class %q", scriptString(t, vm, "__label()"), scriptString(t, vm, "__copyClass()"))
	}
	if _, err := vm.RunString(`__click("markdown")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__pressed()") != "true" || scriptString(t, vm, "__shown()") != "source" || scriptString(t, vm, "__markdownName()") != "Markdown" {
		t.Fatalf("on pressed %q shown %q name %q", scriptString(t, vm, "__pressed()"), scriptString(t, vm, "__shown()"), scriptString(t, vm, "__markdownName()"))
	}
	if _, err := vm.RunString(`__click("markdown")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__pressed()") != "false" || scriptString(t, vm, "__shown()") != "rendered" || scriptString(t, vm, "__markdownName()") != "Markdown" {
		t.Fatalf("off pressed %q shown %q name %q", scriptString(t, vm, "__pressed()"), scriptString(t, vm, "__shown()"), scriptString(t, vm, "__markdownName()"))
	}
}

const messageControlHarness = `
function __cls(names) {
  var list = names ? names.split(" ") : [];
  return {
    contains: function (n) { return list.indexOf(n) >= 0; },
    add: function (n) { if (list.indexOf(n) < 0) list.push(n); },
    remove: function (n) { var i = list.indexOf(n); if (i >= 0) list.splice(i, 1); }
  };
}
function __el(tag, className) {
  var node = {
    tag: tag,
    classList: __cls(className),
    attrs: {},
    children: [],
    textContent: "",
    hidden: false,
    parentNode: null,
    getAttribute: function (k) {
      if (!Object.prototype.hasOwnProperty.call(this.attrs, k)) return null;
      return this.attrs[k];
    },
    setAttribute: function (k, v) { this.attrs[k] = String(v); },
    removeAttribute: function (k) { delete this.attrs[k]; },
    getElementsByTagName: function (want) {
      var out = [];
      function walk(n) {
        for (var i = 0; i < n.children.length; i++) {
          if (n.children[i].tag === want) out.push(n.children[i]);
          walk(n.children[i]);
        }
      }
      walk(this);
      return out;
    }
  };
  return node;
}
function __add(parent, child) {
  child.parentNode = parent;
  parent.children.push(child);
  return child;
}
var __source = __el("pre", "body source");
__source.textContent = "line\n**two**";
__source.hidden = true;
var __rendered = __el("div", "body rendered");
var __copy = __el("button", "copy");
__copy.attrs["aria-label"] = "Copy markdown";
var __svg = __el("svg", "icon-copy");
__add(__copy, __svg);
var __markdown = __el("button", "markdown");
__markdown.attrs["aria-pressed"] = "false";
__markdown.attrs["aria-label"] = "Markdown";
var __msg = __el("div", "message");
__add(__msg, __rendered);
__add(__msg, __source);
var __foot = __el("p", "foot");
__add(__foot, __markdown);
__add(__foot, __copy);
__add(__msg, __foot);
var __main = __el("main", "");
__add(__main, __msg);
__main.addEventListener = function (type, fn) { this.onclick = fn; };
var __timers = [];
function setTimeout(fn) { __timers.push(fn); return __timers.length; }
function clearTimeout() {}
var __lastCopy = "";
var __writeFails = false;
var navigator = { clipboard: { writeText: function (text) {
  if (__writeFails) return { then: function (ok, err) { err(); } };
  __lastCopy = text;
  return { then: function (ok) { ok(); } };
}}};
var document = { getElementById: function (id) { return id === "main" ? __main : null; } };
function __click(which) {
  var target = which === "svg" ? __svg : __markdown;
  __main.onclick({ target: target });
}
function __copiedText() { return __lastCopy; }
function __label() { return __copy.getAttribute("aria-label"); }
function __title() { return __copy.getAttribute("title"); }
function __copyClass() { return __copy.classList.contains("copied") ? "copied" : ""; }
function __pressed() { return __markdown.getAttribute("aria-pressed"); }
function __markdownName() { return __markdown.getAttribute("aria-label"); }
function __shown() {
  if (!__rendered.hidden && __source.hidden) return "rendered";
  if (__rendered.hidden && !__source.hidden) return "source";
  return "both";
}
function __flush() { var fn = __timers.shift(); if (fn) fn(); }
function __failCopy() { __writeFails = true; }
`

func TestTabTitleNamesTheProduct(t *testing.T) {
	if got := (page{}).TabTitle(); got != "Hotseat" {
		t.Fatalf("recent %q", got)
	}
	if got := (page{Kind: kindList}).TabTitle(); got != "Hotseat - Conversations" {
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
  "send-form": {},
  "publish-button": {},
  "read-button": {},
  "txid": { value: "" }
};
__elements["send-form"].addEventListener = function (type, fn) {
  __elements["send-form"].onsubmit = fn;
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
  __elements["send-form"].onsubmit({ submitter: __elements[which] });
}
function __txid() { return __elements.txid.value; }
`

func TestRefusedSendOpensTheSameDialog(t *testing.T) {
	refused := conversationPage("job")
	refused.SendOpen = true
	refusedHTML := renderPage(t, refused)
	if strings.Contains(refusedHTML, `id="send-dialog" open`) || !sendMarked(refusedHTML) {
		t.Fatalf("marker\n%s", refusedHTML)
	}
	vm := sendDialogVM(t, refusedHTML)
	if scriptString(t, vm, "__shown()") != "1" {
		t.Fatal("refused send did not open the dialog")
	}
	if _, err := vm.RunString(`__clickPen()`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__shown()") != "1" {
		t.Fatal("pen opened a dialog that was already open")
	}

	quietHTML := renderPage(t, conversationPage("job"))
	if sendMarked(quietHTML) {
		t.Fatalf("marker\n%s", quietHTML)
	}
	vm = sendDialogVM(t, quietHTML)
	if scriptString(t, vm, "__shown()") != "0" {
		t.Fatal("a closed send opened the dialog")
	}
	if _, err := vm.RunString(`__clickPen()`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__shown()") != "1" {
		t.Fatal("pen did not open the dialog")
	}
}

func sendMarked(html string) bool {
	return strings.Contains(html, `id="send-dialog" data-send-open`)
}

func renderPage(t *testing.T, p page) string {
	t.Helper()
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, p); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func sendDialogVM(t *testing.T, html string) *goja.Runtime {
	t.Helper()
	script, err := pageScript(html)
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(sendDialogHarness); err != nil {
		t.Fatal(err)
	}
	marked := "false"
	if sendMarked(html) {
		marked = "true"
	}
	if _, err := vm.RunString("__marked = " + marked); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	return vm
}

const sendDialogHarness = `
var __shownCount = 0;
var __marked = false;
var __dialog = {
  open: false,
  hasAttribute: function (name) { return name === "data-send-open" && __marked; },
  showModal: function () {
    if (this.open) throw new Error("already open");
    this.open = true;
    __shownCount++;
  }
};
var __pen = {
  addEventListener: function (type, fn) { this.onclick = fn; }
};
var document = {
  getElementById: function (id) {
    if (id === "send-dialog") return __dialog;
    if (id === "send-pen") return __pen;
    return null;
  }
};
function __clickPen() { __pen.onclick(); }
function __shown() { return String(__shownCount); }
`

func TestFailedOlderGroupKeepsTheMarker(t *testing.T) {
	open := bytes.Buffer{}
	page := conversationPage("job")
	page.Read = &readView{
		Outcome:  "ok",
		Messages: []client.Message{{Body: "newest"}},
		Full:     true,
		NextPage: "/conversations/job?before=3",
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

	const marker = "/conversations/job?before=3"
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
		{name: "empty", job: `{ok:true, outcome:"ok", empty:true, notice:"", reason:"", next:"", bodies:0}`, message: "", added: "0"},
		{name: "next", job: `{ok:true, outcome:"ok", notice:"", reason:"", next:"/conversations/job?before=1", bodies:1}`, marker: "/conversations/job?before=1", added: "1"},
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
      if (id === "read-outcome") {
        if (!page.outcome || page.outcome === "ok") return null;
        return textNode(page.outcome);
      }
      if (id === "read-reason") return textNode(page.reason);
      if (id === "transcript") {
        if (page.outcome !== "ok" || page.empty) return null;
        return { querySelectorAll: function () { return items; } };
      }
      if (id === "transcript-empty") {
        if (page.outcome === "ok" && page.empty) return {};
        return null;
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
__node("more", { "data-next": "/conversations/job?before=3" });
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

func TestSearchKeepsActivityOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, page{Kind: kindList}); err != nil {
		t.Fatal(err)
	}
	script, err := pageScript(buf.String())
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if _, err := vm.RunString(searchHarness); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(script); err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(`__query("A")`); err != nil {
		t.Fatal(err)
	}
	if got := scriptString(t, vm, "__names()"); got != "zeta,alpha" || scriptString(t, vm, "__hidden()") != "0" {
		t.Fatalf("names %q hidden %q", got, scriptString(t, vm, "__hidden()"))
	}
	if _, err := vm.RunString(`__query("PEAR")`); err != nil {
		t.Fatal(err)
	}
	if got := scriptString(t, vm, "__names()"); got != "alpha" {
		t.Fatalf("ticket %q", got)
	}
	if _, err := vm.RunString(`__query("  ")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__hidden()") != "1" || scriptString(t, vm, "__names()") != "" {
		t.Fatalf("blank hidden %s names %s", scriptString(t, vm, "__hidden()"), scriptString(t, vm, "__names()"))
	}
	if _, err := vm.RunString(`__query("missing")`); err != nil {
		t.Fatal(err)
	}
	if scriptString(t, vm, "__names()") != "" || scriptString(t, vm, "__empty()") != "No matches." {
		t.Fatalf("empty %q names %q", scriptString(t, vm, "__empty()"), scriptString(t, vm, "__names()"))
	}
}

const searchHarness = `
function Node(tag) {
  this.tag = tag;
  this.attrs = {};
  this.children = [];
  this.hidden = false;
  this.textContent = "";
  this.className = "";
  this.href = "";
  this.id = "";
  this.value = "";
}
Node.prototype.getAttribute = function (k) {
  if (!Object.prototype.hasOwnProperty.call(this.attrs, k)) return null;
  return this.attrs[k];
};
Node.prototype.setAttribute = function (k, v) { this.attrs[k] = v; };
Node.prototype.appendChild = function (c) { this.children.push(c); return c; };
Node.prototype.removeChild = function (c) {
  var i = this.children.indexOf(c);
  if (i >= 0) this.children.splice(i, 1);
  return c;
};
Object.defineProperty(Node.prototype, "firstChild", {
  get: function () { return this.children.length ? this.children[0] : null; }
});
Node.prototype.querySelector = function (sel) {
  if (sel !== "a") return null;
  for (var i = 0; i < this.children.length; i++) {
    var ch = this.children[i];
    if (ch.tag === "a") return ch;
    if (ch.querySelector) {
      var found = ch.querySelector(sel);
      if (found) return found;
    }
  }
  return null;
};
Node.prototype.addEventListener = function (type, fn) { this["on" + type] = fn; };
function item(name, status, task, ticket, href) {
  var n = new Node("li");
  n.attrs = {"data-name": name, "data-status": status, "data-task": task, "data-ticket": ticket, "data-href": href};
  return n;
}
var __index = new Node("ul");
__index.children = [
  item("zeta", "open", "apple", "", "/conversations/zeta"),
  item("alpha", "closed", "", "pear", "/conversations/alpha"),
  item("quiet", "open", "", "", "/conversations/quiet")
];
var __box = new Node("input");
var __results = new Node("ul");
__results.hidden = true;
var document = {
  getElementById: function (id) {
    if (id === "search") return __box;
    if (id === "search-results") return __results;
    if (id === "conversation-index") return __index;
    return null;
  },
  createElement: function (tag) { return new Node(tag); }
};
var window = {};
function __query(v) { __box.value = v; __box.oninput(); }
function __names() {
  var out = [];
  for (var i = 0; i < __results.children.length; i++) {
    var link = __results.children[i].querySelector("a");
    if (link) out.push(link.textContent);
  }
  return out.join(",");
}
function __hidden() { return __results.hidden ? "1" : "0"; }
function __empty() {
  for (var i = 0; i < __results.children.length; i++) {
    if (__results.children[i].id === "search-empty") return __results.children[i].textContent;
  }
  return "";
}
`
