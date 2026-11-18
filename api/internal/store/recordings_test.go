package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSetRecordingAudioRejectsInvalidAudioBeforeTouchingTheDatabase(t *testing.T) {
	expires := time.Date(2027, 5, 1, 0, 0, 0, 0, time.UTC)
	for name, audio := range map[string]RecordingAudio{
		"empty key":   {ExpiresAt: expires},
		"zero expiry": {Key: "audio/m.mp3"},
		"both empty":  {},
	} {
		if _, err := New(nil).SetRecordingAudio(t.Context(), uuid.New(), audio); !errors.Is(err, ErrInvalidAudio) {
			t.Errorf("%s: SetRecordingAudio err = %v, want ErrInvalidAudio", name, err)
		}
		if _, err := New(nil).SetRecordingAudioTx(t.Context(), nil, uuid.New(), audio); !errors.Is(err, ErrInvalidAudio) {
			t.Errorf("%s: SetRecordingAudioTx err = %v, want ErrInvalidAudio", name, err)
		}
	}
}

func TestIsForeignKeyViolation(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "23503"}, true},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23503"}), true},
		{&pgconn.PgError{Code: "23505"}, false},
		{errors.New("other"), false},
		{nil, false},
	} {
		if got := isForeignKeyViolation(tc.err); got != tc.want {
			t.Errorf("isForeignKeyViolation(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
