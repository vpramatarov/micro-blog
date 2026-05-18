// Package slug turns human-readable titles into URL-safe lowercase strings
// using the 2009 Bulgarian transliteration law for Cyrillic input (including the end-of-word "ия → ia" carve-out).
// Non-Latin/non-Cyrillic input is stripped; the result contains only [a-z0-9-].
//
// The output is the BASE slug. Collision resolution (appending -2, -3, ...)
package slug

import "strings"

// MaxLength caps the generated slug at the same length as the post title
// (validation.TitleMaxLen). The handler should treat an empty result as a validation error on the title field.
const MaxLength = 200

// bgTranslit maps each lowercase Bulgarian Cyrillic letter to its official
// Latin transliteration per the 2009 Cyrillic-to-Latin transliteration law.
// "ия → ia" at word ends is handled separately in transliterateToken; the
// per-rune mapping here emits "ya" for я and is overridden only when я is
// the final rune of a token preceded by и.
var bgTranslit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d",
	'е': "e", 'ж': "zh", 'з': "z", 'и': "i", 'й': "y",
	'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh",
	'щ': "sht", 'ъ': "a", 'ь': "y", 'ю': "yu", 'я': "ya",
}

// Generate produces a slug from `title`. Returns "" when no characters survive
// (e.g. title made entirely of emoji or punctuation); callers should treat
// "" as a validation failure on the title field.
func Generate(title string) string {
	tokens := tokenize(strings.ToLower(title))
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if out := transliterateToken(t); out != "" {
			parts = append(parts, out)
		}
	}

	joined := strings.Join(parts, "-")
	for strings.Contains(joined, "--") {
		joined = strings.ReplaceAll(joined, "--", "-")
	}

	joined = strings.Trim(joined, "-")
	if len(joined) > MaxLength {
		joined = strings.TrimRight(joined[:MaxLength], "-")
	}

	return joined
}

// tokenize splits the input into runs of slug-word characters. Punctuation,
// whitespace, emoji, and any other rune that isn't ASCII alphanumeric or a known Bulgarian Cyrillic letter ends the current token.
// This keeps "1.26" from collapsing into "126" — the dot acts as a separator.
func tokenize(s string) []string {
	var (
		tokens []string
		cur    strings.Builder
	)
	for _, r := range s {
		if isSlugWordChar(r) {
			cur.WriteRune(r)
			continue
		}

		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}

	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}

	return tokens
}

func isSlugWordChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	}

	_, ok := bgTranslit[r]
	return ok
}

// transliterateToken applies the per-rune Bulgarian map to one whitespace-
// separated token. The end-of-token "ия" sequence is treated specially:
// strict letter-by-letter would emit "iya", but the official transliteration
// law specifies "ia" at word ends (the "Maria not Mariya" rule).
func transliterateToken(tok string) string {
	runes := []rune(tok)
	endsInIya := len(runes) >= 2 && runes[len(runes)-2] == 'и' && runes[len(runes)-1] == 'я'
	if endsInIya {
		runes = runes[:len(runes)-2]
	}

	var b strings.Builder
	for _, r := range runes {
		if t, ok := bgTranslit[r]; ok {
			b.WriteString(t)
			continue
		}

		if isSlugWordChar(r) {
			b.WriteRune(r)
		}
		// else: rune is not in the slug alphabet — drop it. tokenize already
		// filters most non-slug runes, but a tokenizer/translit mismatch
		// (e.g. a Cyrillic letter outside bgTranslit) would land here.
	}

	if endsInIya {
		b.WriteString("ia")
	}

	return b.String()
}
