package shortcode_test

import (
	"testing"

	"github.com/vpramatarov/micro-blog/internal/shortcode"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	enc, err := shortcode.New()
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for _, id := range []int64{0, 1, 42, 1000, 9999999} {
		code, err := enc.Encode(id)
		if err != nil {
			t.Fatalf("encode %d: %v", id, err)
		}

		if len(code) < shortcode.DefaultMinLength {
			t.Errorf("encode %d: got %q, want length >= %d", id, code, shortcode.DefaultMinLength)
		}

		back, err := enc.Decode(code)
		if err != nil {
			t.Fatalf("decode %q: %v", code, err)
		}

		if back != id {
			t.Errorf("roundtrip %d: got %d", id, back)
		}
	}
}

func TestEncodeNegativeID(t *testing.T) {
	enc, _ := shortcode.New()
	if _, err := enc.Encode(-1); err == nil {
		t.Error("expected error for negative id")
	}
}

func TestDecodeGarbage(t *testing.T) {
	enc, _ := shortcode.New()
	if _, err := enc.Decode(""); err == nil {
		t.Error("expected error for empty code")
	}

	if _, err := enc.Decode("!!!"); err == nil {
		t.Error("expected error for non-alphabet code")
	}
}
