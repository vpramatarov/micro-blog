package validation_test

import (
	"strings"
	"testing"

	"github.com/vpramatarov/micro-blog/internal/validation"
)

func TestUsername(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"   ", "is required"},
		{"ab", "must be at least 3 characters"},
		{strings.Repeat("a", 51), "must be at most 50 characters"},
		{"alice bob", "may only contain letters, digits, '-' and '_'"},
		{"alice.bob", "may only contain letters, digits, '-' and '_'"},
		{"中文用户", "may only contain letters, digits, '-' and '_'"},
		{"alice", ""},
		{"Alice_Bob-99", ""},
		{strings.Repeat("a", 50), ""}, // exactly the upper bound
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.ValidateUsername(c.in)
			if got != c.want {
				t.Errorf("Username(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestEmail(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"   ", "is required"},
		{"not-an-email", "is not a valid email"},
		{"@no-local-part.com", "is not a valid email"},
		{"a@b", ""}, // bare hostname is RFC-valid
		{"alice@example.com", ""},
		{"Alice@Example.COM", ""},
		{strings.Repeat("a", 250) + "@b.com", "must be at most 254 characters"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.ValidateEmail(c.in)
			if got != c.want {
				t.Errorf("Email(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPassword(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"short", "must be at least 8 characters"},
		{"        ", ""}, // intentional whitespace is allowed
		{"hunter2!", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.ValidatePassword(c.in)
			if got != c.want {
				t.Errorf("Password(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestPasswordOptional(t *testing.T) {
	if got := validation.PasswordOptional(""); got == "" {
		t.Error("PasswordOptional(\"\") should fail (length 0)")
	}

	if got := validation.PasswordOptional("short"); got != "must be at least 8 characters" {
		t.Errorf("PasswordOptional short: got %q", got)
	}

	if got := validation.PasswordOptional("hunter2!"); got != "" {
		t.Errorf("PasswordOptional ok: got %q", got)
	}
}

func TestTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"  ", "is required"},
		{"ab", "must be at least 3 characters"},
		{strings.Repeat("a", 201), "must be at most 200 characters"},
		{"Hello", ""},
		{strings.Repeat("a", 200), ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.Title(c.in)
			if got != c.want {
				t.Errorf("Title(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMarkdownContent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"     ", "is required"},
		{"short", "must be at least 10 characters"},
		{"# Hello\n\nworld", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.MarkdownContent(c.in)
			if got != c.want {
				t.Errorf("MarkdownContent(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"   ", "is required"},
		{"AI", ""},
		{"Front-end", ""},
		{"общи теми", ""},
		{strings.Repeat("a", 50), ""},
		{strings.Repeat("a", 51), "must be at most 50 characters"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.Name(c.in)
			if got != c.want {
				t.Errorf("Name(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "is required"},
		{"  ", "is required"},
		{"example.com", "must use http or https"},
		{"ftp://example.com/x", "must use http or https"},
		{"javascript:alert(1)", "must use http or https"},
		{"http://", "must include a host"},
		{"https://example.com/path", ""},
		{"http://example.com", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := validation.URL(c.in)
			if got != c.want {
				t.Errorf("URL(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestErrorsAccumulator(t *testing.T) {
	errs := validation.New()
	if !errs.IsEmpty() {
		t.Fatal("fresh Errors not empty")
	}

	errs.Add("a", "msg-a")
	errs.Add("b", "msg-b")
	errs.Add("a", "another-a") // first write wins
	errs.Add("c", "")          // empty messages dropped

	if errs.IsEmpty() {
		t.Fatal("Errors should be non-empty")
	}

	if errs["a"] != "msg-a" {
		t.Errorf("a: got %q, want msg-a (first-write-wins violated)", errs["a"])
	}

	if errs["b"] != "msg-b" {
		t.Errorf("b: got %q", errs["b"])
	}

	if _, present := errs["c"]; present {
		t.Error("c: empty message should not be recorded")
	}
}
