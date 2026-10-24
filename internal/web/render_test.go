// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"strings"
	"testing"
)

func TestRenderBodyKeepsMarkdownTextAndDropsUnsafeLinks(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		skip string
	}{
		{name: "emphasis", body: "**x**", want: "<strong>x</strong>"},
		{name: "raw tag", body: "<b>&", want: "&lt;b&gt;&amp;", skip: "<b>"},
		{name: "script", body: "<script>alert(1)</script>", want: "&lt;script&gt;", skip: "<script"},
		{name: "javascript link", body: "[x](javascript:alert(1))", want: "x", skip: "href="},
		{name: "entity javascript", body: "[x](&#106;avascript:alert(1))", want: "x", skip: "href="},
		{name: "vertical tab link", body: "[x](\vhttps://example.com)", want: "x", skip: "href="},
		{name: "trailing non-breaking space", body: "[x](https://example.com\u00a0)", want: `href="https://example.com%C2%A0"`},
		{name: "relative link", body: "[x](/etc/passwd)", want: "x", skip: "href="},
		{name: "https link", body: "[x](https://example.com/a)", want: `<a href="https://example.com/a" rel="noopener noreferrer">x</a>`},
		{name: "mailto", body: "[x](mailto:a@b.c)", want: `href="mailto:a@b.c"`},
		{name: "image", body: "![alt](https://example.com/a.png)", want: "alt", skip: "<img"},
		{name: "image raw html", body: "![<script>alert(1)</script>](https://example.com/a.png)", want: "&lt;script&gt;alert(1)&lt;/script&gt;", skip: "<script"},
		{name: "image autolink", body: "![see <http://example.com>](https://example.com/a.png)", want: "see http://example.com", skip: "href="},
		{name: "fence", body: "```\n<b>\n```", want: "<code>", skip: "<b>"},
		{name: "table", body: "| a |\n| --- |\n| 1 |", want: `<div class="table-scroll"><table>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(renderBody(tc.body))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("body %q rendered %q, missing %q", tc.body, got, tc.want)
			}
			if tc.skip != "" && strings.Contains(got, tc.skip) {
				t.Fatalf("body %q rendered %q, contains %q", tc.body, got, tc.skip)
			}
			if strings.Contains(got, "raw HTML omitted") {
				t.Fatalf("omitted raw HTML\n%s", got)
			}
		})
	}
}
