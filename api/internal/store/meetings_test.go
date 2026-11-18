package store

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMeetingCursorRoundTrips(t *testing.T) {
	m := Meeting{ID: uuid.New(), AgencyID: uuid.New(), StartsAt: time.Date(2026, 3, 5, 18, 0, 0, 123456000, time.FixedZone("PST", -8*3600))}

	got, err := decodeMeetingCursor(encodeMeetingCursor(m), m.AgencyID)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != m.ID || !got.StartsAt.Equal(m.StartsAt) {
		t.Errorf("cursor = %+v, want id %v at %v", got, m.ID, m.StartsAt)
	}
}

func TestDecodeMeetingCursorRejectsMalformedValues(t *testing.T) {
	encode := func(s string) string {
		return base64.RawURLEncoding.EncodeToString(append([]byte(s), meetingCursorChecksum([]byte(s))...))
	}
	for name, cursor := range map[string]string{
		"not base64":      "!!!",
		"empty":           "",
		"checksum only":   base64.RawURLEncoding.EncodeToString(meetingCursorChecksum(nil)),
		"not json":        encode("hello"),
		"empty object":    encode("{}"),
		"missing id":      encode(`{"s":"2026-03-05T18:00:00Z"}`),
		"missing instant": encode(`{"i":"` + uuid.NewString() + `"}`),
	} {
		if _, err := decodeMeetingCursor(cursor, uuid.New()); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: err = %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestDecodeMeetingCursorRejectsTamperedCursors(t *testing.T) {
	m := Meeting{ID: uuid.New(), AgencyID: uuid.New(), StartsAt: time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)}
	valid := encodeMeetingCursor(m)
	raw, err := base64.RawURLEncoding.DecodeString(valid)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(raw, []byte("2026-03-05"), []byte("2026-03-04"), 1)
	if bytes.Equal(edited, raw) {
		t.Fatalf("cursor %q does not contain the start date", raw)
	}

	for name, cursor := range map[string]string{
		"edited start time": base64.RawURLEncoding.EncodeToString(edited),
		"truncated":         valid[:len(valid)-4],
		"extended":          valid + "AAAA",
		"padded":            valid + "=",
	} {
		if _, err := decodeMeetingCursor(cursor, m.AgencyID); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: err = %v, want ErrInvalidCursor", name, err)
		}
	}
}

func TestDecodeMeetingCursorRejectsAnotherAgencysCursor(t *testing.T) {
	m := Meeting{ID: uuid.New(), AgencyID: uuid.New(), StartsAt: time.Date(2026, 3, 5, 18, 0, 0, 0, time.UTC)}
	if _, err := decodeMeetingCursor(encodeMeetingCursor(m), uuid.New()); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("err = %v, want ErrInvalidCursor", err)
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
