// Package shortcode wraps github.com/sqids/sqids-go to encode int64 IDs into
// short opaque strings (e.g. "X7bL9q") and decode them back.
//
// Used for public post URLs (so non-admin and unauthenticated callers don't hit raw numeric IDs) and slated for the URL-shortener feature.
package shortcode

import (
	"errors"
	"fmt"

	"github.com/sqids/sqids-go"
)

// DefaultMinLength keeps codes at six characters minimum.
const DefaultMinLength = 6

type Encoder struct {
	s *sqids.Sqids
}

func New() (*Encoder, error) {
	s, err := sqids.New(sqids.Options{MinLength: DefaultMinLength})
	if err != nil {
		return nil, fmt.Errorf("new sqids: %w", err)
	}

	return &Encoder{s: s}, nil
}

func (e *Encoder) Encode(id int64) (string, error) {
	if id < 0 {
		return "", errors.New("shortcode: negative id")
	}

	return e.s.Encode([]uint64{uint64(id)})
}

func (e *Encoder) Decode(code string) (int64, error) {
	nums := e.s.Decode(code)
	if len(nums) != 1 {
		return 0, errors.New("shortcode: invalid code")
	}

	return int64(nums[0]), nil
}
