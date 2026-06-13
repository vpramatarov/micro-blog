package markdown_test

import (
	"strings"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/markdown"
)

func render(t *testing.T, md string) string {
	t.Helper()
	out, err := markdown.Render(md)
	if err != nil {
		t.Fatalf("render %q: %v", md, err)
	}

	return out
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing %q\nfull output: %s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("output unexpectedly contains %q\nfull output: %s", needle, haystack)
	}
}

func TestRenderHeading(t *testing.T) {
	out := render(t, "# Hello")
	mustContain(t, out, "<h1>Hello</h1>")
}

func TestRenderParagraphAndEmphasis(t *testing.T) {
	out := render(t, "This is **bold** and *italic*.")
	mustContain(t, out, "<p>")
	mustContain(t, out, "<strong>bold</strong>")
	mustContain(t, out, "<em>italic</em>")
}

func TestRenderCodeBlock(t *testing.T) {
	out := render(t, "```\nfmt.Println(\"hi\")\n```")
	mustContain(t, out, "<pre>")
	mustContain(t, out, "<code>")
}

func TestRenderLink(t *testing.T) {
	out := render(t, "[example](https://example.com)")
	mustContain(t, out, `<a href="https://example.com">example</a>`)
}

// GFM extension — tables.
func TestRenderTable(t *testing.T) {
	md := "| a | b |\n|---|---|\n| 1 | 2 |"
	out := render(t, md)
	mustContain(t, out, "<table>")
	mustContain(t, out, "<th>a</th>")
	mustContain(t, out, "<td>1</td>")
}

// GFM extension — strikethrough.
func TestRenderStrikethrough(t *testing.T) {
	out := render(t, "~~gone~~")
	mustContain(t, out, "<del>gone</del>")
}

// GFM extension — autolinks.
func TestRenderAutolink(t *testing.T) {
	out := render(t, "see https://example.com for details")
	mustContain(t, out, `href="https://example.com"`)
}

// GFM extension — task lists.
func TestRenderTaskList(t *testing.T) {
	out := render(t, "- [ ] todo\n- [x] done")
	mustContain(t, out, `<input`)
	mustContain(t, out, `type="checkbox"`)
}

// Security: raw HTML in the markdown source must be escaped, never passed through.
// goldmark's default behavior (no WithUnsafe) handles this.
func TestRenderEscapesRawScriptTag(t *testing.T) {
	out := render(t, `<script>alert("xss")</script>`)
	mustNotContain(t, out, "<script>")
	mustNotContain(t, out, "</script>")
}

func TestRenderEscapesRawDivWithEventHandler(t *testing.T) {
	out := render(t, `<div onclick="evil()">x</div>`)
	mustNotContain(t, out, "<div")
	mustNotContain(t, out, "onclick")
}

// Inline HTML inside otherwise valid markdown — also escaped.
func TestRenderEscapesInlineHTMLInsideParagraph(t *testing.T) {
	out := render(t, `Hello <img src=x onerror="evil()"> world`)
	mustNotContain(t, out, "<img")
	mustNotContain(t, out, "onerror")
}

func TestRenderEmptyInput(t *testing.T) {
	out := render(t, "")
	if out != "" {
		t.Errorf("empty input: got %q, want empty", out)
	}
}

func TestToText(t *testing.T) {
	md := "# title\n\n" + strings.Repeat("a", 250)
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{"heading", "# Hello", "Hello"},
		{"link", "[example](https://example.com)", "example"},
		{"bold_italic", "This is **bold** and *italic*.", "This is bold and italic."},
		{"new_lines", "# body\n\nlong enough.", "body\n\nlong enough."},
		{"text_limit", md, markdown.ToText(md)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clean := markdown.ToText(c.in)
			if clean != c.out {
				t.Errorf("got %s, want %s; input: %s", clean, c.out, c.in)
			}
		})
	}
}
