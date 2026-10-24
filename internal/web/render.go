// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package web

import (
	"bytes"
	"html"
	"html/template"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// renderBody renders a message body as Markdown.
// Raw HTML stays text. A link is kept only for http, https, and mailto.
func renderBody(src string) template.HTML {
	source := []byte(src)
	doc := bodyMarkdown.Parser().Parse(text.NewReader(source))
	scrubLinks(doc, source)
	var buf bytes.Buffer
	if err := bodyMarkdown.Renderer().Render(&buf, source, doc); err != nil {
		return template.HTML("<p>" + html.EscapeString(src) + "</p>")
	}
	out := bytes.ReplaceAll(buf.Bytes(), []byte("<table"), []byte(`<div class="table-scroll"><table`))
	out = bytes.ReplaceAll(out, []byte("</table>"), []byte("</table></div>"))
	return template.HTML(out)
}

var bodyMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(htmlPolicy{}, 100)),
	),
)

type htmlPolicy struct{}

func (htmlPolicy) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, renderEscapedHTMLBlock)
	reg.Register(ast.KindRawHTML, renderEscapedRawHTML)
	reg.Register(ast.KindLink, renderLink)
	reg.Register(ast.KindAutoLink, renderAutoLink)
	reg.Register(ast.KindImage, renderImageAlt)
}

func renderEscapedHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.HTMLBlock)
	if entering {
		for i := 0; i < n.Lines().Len(); i++ {
			line := n.Lines().At(i)
			_, _ = w.Write(util.EscapeHTML(line.Value(source)))
		}
		return ast.WalkContinue, nil
	}
	if n.HasClosure() {
		_, _ = w.Write(util.EscapeHTML(n.ClosureLine.Value(source)))
	}
	return ast.WalkContinue, nil
}

func renderEscapedRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
	}
	return ast.WalkSkipChildren, nil
}

func renderLink(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if entering {
		_, _ = w.WriteString(`<a href="`)
		_, _ = w.Write(util.EscapeHTML(linkHref(n.Destination)))
		_, _ = w.WriteString(`" rel="noopener noreferrer"`)
		if len(n.Title) > 0 {
			_, _ = w.WriteString(` title="`)
			_, _ = w.Write(util.EscapeHTML(n.Title))
			_ = w.WriteByte('"')
		}
		_ = w.WriteByte('>')
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("</a>")
	return ast.WalkContinue, nil
}

func renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	_, _ = w.WriteString(`<a href="`)
	_, _ = w.Write(util.EscapeHTML(autoHref(n, source)))
	_, _ = w.WriteString(`" rel="noopener noreferrer">`)
	_, _ = w.Write(util.EscapeHTML(n.Label(source)))
	_, _ = w.WriteString("</a>")
	return ast.WalkSkipChildren, nil
}

func renderImageAlt(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	writeInlineText(w, source, node)
	return ast.WalkSkipChildren, nil
}

func writeInlineText(w util.BufWriter, source []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			seg := t.Segment
			_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
		case *ast.String:
			_, _ = w.Write(util.EscapeHTML(t.Value))
		case *ast.RawHTML:
			for i := 0; i < t.Segments.Len(); i++ {
				seg := t.Segments.At(i)
				_, _ = w.Write(util.EscapeHTML(seg.Value(source)))
			}
		case *ast.AutoLink:
			_, _ = w.Write(util.EscapeHTML(t.Label(source)))
		default:
			writeInlineText(w, source, c)
		}
	}
}

func scrubLinks(doc ast.Node, source []byte) {
	var links []*ast.Link
	var autos []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			if !allowedDestination(linkHref(n.Destination)) {
				links = append(links, n)
			}
		case *ast.AutoLink:
			if !allowedDestination(autoHref(n, source)) {
				autos = append(autos, n)
			}
		}
		return ast.WalkContinue, nil
	})
	for _, n := range links {
		unwrap(n)
	}
	for _, n := range autos {
		parent := n.Parent()
		if parent == nil {
			continue
		}
		parent.InsertBefore(parent, n, ast.NewString(append([]byte(nil), n.Label(source)...)))
		parent.RemoveChild(parent, n)
	}
}

func unwrap(n ast.Node) {
	parent := n.Parent()
	if parent == nil {
		return
	}
	for c := n.FirstChild(); c != nil; {
		next := c.NextSibling()
		parent.InsertBefore(parent, n, c)
		c = next
	}
	parent.RemoveChild(parent, n)
}

func linkHref(dest []byte) []byte {
	return util.URLEscape(dest, true)
}

func autoHref(n *ast.AutoLink, source []byte) []byte {
	dest := n.URL(source)
	escaped := util.URLEscape(dest, false)
	if n.AutoLinkType == ast.AutoLinkEmail && !bytes.HasPrefix(bytes.ToLower(dest), []byte("mailto:")) {
		return append([]byte("mailto:"), escaped...)
	}
	return escaped
}

func allowedDestination(dest []byte) bool {
	if len(dest) == 0 || bytes.ContainsAny(dest, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse(string(dest))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto":
		return parsed.Opaque != "" || parsed.User != nil
	default:
		return false
	}
}
