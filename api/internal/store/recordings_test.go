package store

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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

func TestListRecordingsWithExpiredAudioRejectsNonPositiveLimitBeforeTouchingTheDatabase(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := New(nil).ListRecordingsWithExpiredAudio(t.Context(), time.Now(), limit); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("limit %d: err = %v, want ErrInvalidLimit", limit, err)
		}
	}
}
