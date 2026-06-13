// Package markdown converts user-supplied markdown source into HTML for storage in posts.html_content.
// Backed by goldmark with GFM extensions (tables, strikethrough, autolinks, task lists) enabled.
//
// XSS posture: raw HTML in the markdown source is escaped (goldmark's default — WithUnsafe is intentionally not set).
// That makes the output safe to render directly in a browser.
// If a richer HTML allow-list is needed later, layer a sanitizer (e.g. bluemonday) on top — don't relax this.
package markdown

import (
	"bytes"

	stripMarkdown "github.com/writeas/go-strip-markdown"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// defaultExcerptLimit is the limit of characters for the excerpt. Content will be truncated to this amount of characters.
const defaultExcerptLimit int = 100

// defaultRenderer is configured once at package init. goldmark.Markdown is safe for concurrent use after configuration, so the whole server shares this instance.
var defaultRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// Render converts markdown source to HTML.
func Render(md string) (string, error) {
	var buf bytes.Buffer
	if err := defaultRenderer.Convert([]byte(md), &buf); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func ToText(md string) string {
	return truncateText(stripMarkdown.Strip(md), defaultExcerptLimit)
}

// truncateText safely truncates a string to a specific number of characters (runes)
func truncateText(text string, limit int) string {
	runes := []rune(text) // Convert to runes for safe UTF-8 slicing
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}

	return text
}
