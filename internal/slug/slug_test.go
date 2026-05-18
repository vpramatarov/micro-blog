package slug_test

import (
	"strings"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/slug"
)

func TestGenerate(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		// ASCII basics.
		{"plain ascii", "Hello World", "hello-world"},
		{"already kebab", "already-kebab", "already-kebab"},
		{"mixed case", "GoLang Rocks", "golang-rocks"},
		{"digits stay", "Go 1.26 is out", "go-1-26-is-out"},
		{"trailing punct", "Hello!!!", "hello"},
		{"leading punct", "!!!Hello", "hello"},
		{"underscores split", "snake_case_word", "snake-case-word"},
		{"emoji stripped", "I 🎉 Go", "i-go"},
		{"all punct", "!!!", ""},
		{"empty", "", ""},
		{"whitespace only", "   \t\n  ", ""},

		// Bulgarian Cyrillic, per the 2009 transliteration law.
		{"bg hello world", "Здравей свят", "zdravey-svyat"},
		{"bg ия end of word", "Мария Иванова", "maria-ivanova"},
		{"bg single ия", "ия", "ia"},
		{"bg ия mid word", "иян", "iyan"}, // not end of word → standard iya pattern (i + ya = iya)
		{"bg sht щ", "още един път", "oshte-edin-pat"},
		{"bg yu/ya/yo", "юли яни", "yuli-yani"},
		{"bg ъ", "ъглов", "aglov"},
		{"bg mixed with latin", "Go и Cyrillic", "go-i-cyrillic"},
		{"bg with digits", "Версия 1.26", "versia-1-26"},

		// Edge cases.
		{"multiple spaces", "hello   world", "hello-world"},
		{"leading and trailing space", "  hello world  ", "hello-world"},
		{"chinese stripped", "你好 world", "world"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slug.Generate(tc.title)
			if got != tc.want {
				t.Errorf("Generate(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestGenerateMaxLength(t *testing.T) {
	// 250 letters → result must be capped at MaxLength with no trailing hyphen.
	long := strings.Repeat("a", 250)
	got := slug.Generate(long)
	if len(got) > slug.MaxLength {
		t.Errorf("len(%q) = %d, want <= %d", got, len(got), slug.MaxLength)
	}

	if strings.HasSuffix(got, "-") {
		t.Errorf("trailing hyphen on %q", got)
	}
}

func TestGenerateCollapsesRepeatedHyphens(t *testing.T) {
	// Punctuation between words and hyphens in source should not produce runs of multiple hyphens.
	got := slug.Generate("hello---world!!---foo")
	want := "hello-world-foo"
	if got != want {
		t.Errorf("Generate(%q) = %q, want %q", "hello---world!!---foo", got, want)
	}
}
