package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSegmentsRoundTripToTheMillisecond(t *testing.T) {
	in := []TranscriptSegment{
		{Start: 0, End: 1500 * time.Millisecond, Text: "Call to order.", Speaker: "c0/speaker_0"},
		{Start: 1500*time.Millisecond + 400*time.Microsecond, End: 4*time.Second + 600*time.Microsecond, Text: "Roll call."},
	}
	raw, err := encodeSegments(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"start_ms":0,"end_ms":1500,"text":"Call to order.","speaker":"c0/speaker_0"},{"start_ms":1500,"end_ms":4001,"text":"Roll call."}]`
	if raw != want {
		t.Errorf("encoded = %s, want %s", raw, want)
	}

	out, err := decodeSegments([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	wantOut := []TranscriptSegment{
		{Start: 0, End: 1500 * time.Millisecond, Text: "Call to order.", Speaker: "c0/speaker_0"},
		{Start: 1500 * time.Millisecond, End: 4001 * time.Millisecond, Text: "Roll call."},
	}
	if !reflect.DeepEqual(out, wantOut) {
		t.Errorf("decoded = %+v, want %+v", out, wantOut)
	}
}

func TestEncodeSegmentsOfNoneIsAnEmptyArray(t *testing.T) {
	for _, segments := range [][]TranscriptSegment{nil, {}} {
		raw, err := encodeSegments(segments)
		if err != nil || raw != "[]" {
			t.Errorf("encodeSegments(%v) = (%q, %v), want []", segments, raw, err)
		}
	}
	out, err := decodeSegments([]byte("[]"))
	if err != nil || out == nil || len(out) != 0 {
		t.Errorf("decodeSegments([]) = (%#v, %v), want an empty non-nil slice", out, err)
	}
}

func TestEncodeSegmentsRejectsBadTimes(t *testing.T) {
	for name, s := range map[string]TranscriptSegment{
		"negative start":   {Start: -time.Second, End: time.Second},
		"end before start": {Start: 2 * time.Second, End: time.Second},
		"negative both":    {Start: -2 * time.Second, End: -time.Second},
	} {
		if _, err := encodeSegments([]TranscriptSegment{{End: time.Second}, s}); !errors.Is(err, ErrInvalidSegment) {
			t.Errorf("%s: err = %v, want ErrInvalidSegment", name, err)
		}
	}
	if _, err := encodeSegments([]TranscriptSegment{{Start: time.Second, End: time.Second}}); err != nil {
		t.Errorf("zero-length segment rejected: %v", err)
	}
}

func TestDecodeSegmentsRejectsMalformedJSON(t *testing.T) {
	for _, raw := range []string{`{}`, `[1]`, `not json`} {
		if _, err := decodeSegments([]byte(raw)); err == nil {
			t.Errorf("decodeSegments(%s) succeeded", raw)
		}
	}
}

func TestSaveTranscriptRejectsBadSegmentsBeforeTouchingTheDatabase(t *testing.T) {
	in := TranscriptInput{RecordingID: uuid.New(), Text: "t", Segments: []TranscriptSegment{{Start: time.Second}}}
	if _, _, err := New(nil).SaveTranscript(t.Context(), in); !errors.Is(err, ErrInvalidSegment) {
		t.Errorf("SaveTranscript err = %v, want ErrInvalidSegment", err)
	}
	if _, _, err := New(nil).SaveTranscriptTx(t.Context(), nil, in); !errors.Is(err, ErrInvalidSegment) {
		t.Errorf("SaveTranscriptTx err = %v, want ErrInvalidSegment", err)
	}
}

func TestSetTranscriptSummaryRejectsZeroHashBeforeTouchingTheDatabase(t *testing.T) {
	if _, err := New(nil).SetTranscriptSummary(t.Context(), uuid.New(), ContentHash{}, "s"); !errors.Is(err, ErrZeroContentHash) {
		t.Errorf("SetTranscriptSummary err = %v, want ErrZeroContentHash", err)
	}
	if _, err := New(nil).SetTranscriptSummaryTx(t.Context(), nil, uuid.New(), ContentHash{}, "s"); !errors.Is(err, ErrZeroContentHash) {
		t.Errorf("SetTranscriptSummaryTx err = %v, want ErrZeroContentHash", err)
	}
}
