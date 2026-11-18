package store

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMeetingCursorRoundTrips(t *testing.T) {
	m := Meeting{ID: uuid.New(), StartsAt: time.Date(2026, 3, 5, 18, 0, 0, 123456000, time.FixedZone("PST", -8*3600))}

	got, err := decodeMeetingCursor(encodeMeetingCursor(m))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != m.ID || !got.StartsAt.Equal(m.StartsAt) {
		t.Errorf("cursor = %+v, want id %v at %v", got, m.ID, m.StartsAt)
	}
}

func TestDecodeMeetingCursorRejectsMalformedValues(t *testing.T) {
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, cursor := range map[string]string{
		"not base64":      "!!!",
		"not json":        encode("hello"),
		"empty object":    encode("{}"),
		"missing id":      encode(`{"s":"2026-03-05T18:00:00Z"}`),
		"missing instant": encode(`{"i":"` + uuid.NewString() + `"}`),
	} {
		if _, err := decodeMeetingCursor(cursor); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: err = %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestClampMeetingsLimit(t *testing.T) {
	for limit, want := range map[int]int{
		-1:                   DefaultMeetingsLimit,
		0:                    DefaultMeetingsLimit,
		1:                    1,
		MaxMeetingsLimit:     MaxMeetingsLimit,
		MaxMeetingsLimit + 1: MaxMeetingsLimit,
	} {
		if got := clampMeetingsLimit(limit); got != want {
			t.Errorf("clampMeetingsLimit(%d) = %d, want %d", limit, got, want)
		}
	}
}
