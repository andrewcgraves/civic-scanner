package transcribe

import (
	"strings"
	"testing"
	"time"
)

func sec(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func seg(start, end float64, text string) Segment {
	return Segment{Start: sec(start), End: sec(end), Text: text}
}

func word(start, end float64, text string) Word {
	return Word{Start: sec(start), End: sec(end), Text: text}
}

func relative(chunkStart float64, segments ...Segment) []Segment {
	out := make([]Segment, len(segments))
	for i, s := range segments {
		s.Start -= sec(chunkStart)
		s.End -= sec(chunkStart)
		out[i] = s
	}
	return out
}

func relativeWords(chunkStart float64, words ...Word) []Word {
	out := make([]Word, len(words))
	for i, w := range words {
		w.Start -= sec(chunkStart)
		w.End -= sec(chunkStart)
		out[i] = w
	}
	return out
}

func twoChunks(length, secondStart float64) []Chunk {
	return []Chunk{{Index: 0, Start: 0, Length: sec(length)}, {Index: 1, Start: sec(secondStart), Length: sec(length)}}
}

func texts(segments []Segment) string {
	parts := make([]string, len(segments))
	for i, s := range segments {
		parts[i] = s.Text
	}
	return strings.Join(parts, "|")
}

func assertMonotonic(t *testing.T, segments []Segment) {
	t.Helper()
	for i := 1; i < len(segments); i++ {
		if segments[i].Start < segments[i-1].End {
			t.Errorf("segment %d (%s-%s) overlaps the previous one ending %s", i, segments[i].Start, segments[i].End, segments[i-1].End)
		}
	}
}

func TestStitchCutsInASilenceBothChunksShare(t *testing.T) {
	chunks := twoChunks(60, 30)
	results := []Transcript{
		{Segments: relative(0, seg(20, 32, "A"), seg(33.5, 45.5, "B"), seg(47, 59, "C"), seg(59.5, 60, "D-cut"))},
		{Segments: relative(30, seg(30, 32, "A-cut"), seg(33.6, 45.4, "B"), seg(47.1, 59, "C"), seg(60.5, 72, "D"))},
	}

	got := stitchSegments(chunks, results)

	if texts(got) != "A|B|C|D" {
		t.Errorf("segments = %s, want each sentence once and no cut half", texts(got))
	}
	assertMonotonic(t, got)
}

func TestStitchKeepsTheCompleteViewWhenNoSilenceIsShared(t *testing.T) {
	chunks := twoChunks(10, 8)
	results := []Transcript{
		{Segments: relative(0, seg(0, 4, "one"), seg(4, 8.8, "two"), seg(8.8, 10, "thre"))},
		{Segments: relative(8, seg(8, 8.8, "wo"), seg(8.8, 12, "three"), seg(12, 16, "four"))},
	}

	got := stitchSegments(chunks, results)

	if texts(got) != "one|two|three|four" {
		t.Errorf("segments = %s", texts(got))
	}
	assertMonotonic(t, got)
}

func TestStitchPrefersTheLeftViewWhenItIsTheCompleteOne(t *testing.T) {
	chunks := twoChunks(10, 6)
	results := []Transcript{
		{Segments: relative(0, seg(0, 6.2, "one"), seg(6.2, 9, "two"))},
		{Segments: relative(6, seg(6, 6.5, "ne"), seg(6.5, 9.2, "two again"), seg(9.2, 14, "three"))},
	}

	got := stitchSegments(chunks, results)

	if texts(got) != "one|two|three" {
		t.Errorf("segments = %s", texts(got))
	}
	assertMonotonic(t, got)
}

func TestStitchWithTwoTruncatedViewsKeepsOneWithoutOverlap(t *testing.T) {
	chunks := twoChunks(600, 595)
	results := []Transcript{
		{Segments: relative(0, seg(580, 593.5, "before"), seg(594, 600, "first half"))},
		{Segments: relative(595, seg(595, 605.8, "second part"), seg(607, 619, "after"))},
	}

	got := stitchSegments(chunks, results)

	if texts(got) != "before|second part|after" {
		t.Errorf("segments = %s, want the longer of two truncated views", texts(got))
	}
	assertMonotonic(t, got)
}

func TestStitchBothCompleteStraddlersAreDecidedByMidpoint(t *testing.T) {
	chunks := twoChunks(20, 10)
	results := []Transcript{
		{Segments: relative(0, seg(9, 16, "long"), seg(16, 18, "x"))},
		{Segments: relative(10, seg(11, 16.2, "long"), seg(16.2, 18, "x"))},
	}

	got := stitchSegments(chunks, results)

	if texts(got) != "long|x" {
		t.Errorf("segments = %s", texts(got))
	}
	assertMonotonic(t, got)
}

func TestStitchWithWordsSplitsASegmentCrossingTheCut(t *testing.T) {
	chunks := twoChunks(10, 6)
	aWords := []Word{word(5, 5.5, "we"), word(5.6, 6.4, "now"), word(6.6, 7.4, "vote"), word(7.6, 8.2, "on"), word(8.4, 9.2, "item"), word(9.4, 10, "fi")}
	bWords := []Word{word(6, 6.4, "ow"), word(6.62, 7.41, "vote"), word(7.61, 8.21, "on"), word(8.41, 9.21, "item"), word(9.41, 10.2, "five."), word(10.5, 11, "Next")}
	results := []Transcript{
		{Segments: relative(0, seg(5, 10, "we now vote on item fi")), Words: relativeWords(0, aWords...)},
		{Segments: relative(6, seg(6, 10.2, "ow vote on item five."), seg(10.5, 11, "Next")), Words: relativeWords(6, bWords...)},
	}

	got := stitchSegments(chunks, results)

	joined := joinText(got, "")
	if joined != "we now vote on item five. Next" {
		t.Errorf("text = %q, want every word once", joined)
	}
	assertMonotonic(t, got)
}

func TestStitchWithContiguousWordsCutsByWordMidpoint(t *testing.T) {
	chunks := twoChunks(4, 2)
	results := []Transcript{
		{Segments: relative(0, seg(0, 4, "a b c d")), Words: relativeWords(0, word(0, 1, "a"), word(1, 2, "b"), word(2, 3, "c"), word(3, 4, "d"))},
		{Segments: relative(2, seg(2, 6, "c d e f")), Words: relativeWords(2, word(2, 3, "c"), word(3, 4, "d"), word(4, 5, "e"), word(5, 6, "f"))},
	}

	got := stitchSegments(chunks, results)

	if joined := joinText(got, ""); joined != "a b c d e f" {
		t.Errorf("text = %q", joined)
	}
}

func TestTrimToWordsWithoutWordsFallsBackToTheMidpoint(t *testing.T) {
	s := seg(1, 3, "x")
	if _, ok := trimToWords(nil, s, func(mid time.Duration) bool { return mid < sec(2.5) }); !ok {
		t.Error("segment with its midpoint on the kept side was dropped")
	}
	if _, ok := trimToWords([]Word{word(1, 1.5, "x")}, s, func(time.Duration) bool { return false }); ok {
		t.Error("segment with no kept words was kept")
	}
}

func TestAppendMonotonicDropsCoveredAndClipsOverlappingSegments(t *testing.T) {
	merged := appendMonotonic(nil, seg(0, 10, "a"))
	merged = appendMonotonic(merged, seg(2, 9, "inside"))
	merged = appendMonotonic(merged, seg(8, 14, "late"))

	if texts(merged) != "a|late" || merged[1].Start != sec(10) {
		t.Errorf("merged = %+v", merged)
	}
}

func TestFreePointPrefersTheTargetAndStaysOffWords(t *testing.T) {
	covered := []interval{{sec(1), sec(4)}, {sec(6), sec(9)}}
	if at, ok := freePoint(covered, sec(0), sec(10), sec(5)); !ok || at != sec(5) {
		t.Errorf("free point = %s, %v", at, ok)
	}
	if at, ok := freePoint(covered, sec(2), sec(8), sec(3)); !ok || at <= sec(4) || at >= sec(6) {
		t.Errorf("free point = %s, %v, want inside the gap", at, ok)
	}
	if _, ok := freePoint([]interval{{sec(0), sec(10)}}, sec(1), sec(9), sec(5)); ok {
		t.Error("found a free point inside speech")
	}
	if _, ok := freePoint(nil, sec(5), sec(5), sec(5)); ok {
		t.Error("found a free point in an empty window")
	}
}

func TestStitchTextRemovesTheRepeatedOverlap(t *testing.T) {
	got := stitchText([]string{"The board will now vote on item five.", "vote on item five. All in favour?", "", "Motion carries."})
	if got != "The board will now vote on item five. All in favour? Motion carries." {
		t.Errorf("text = %q", got)
	}
	if got := stitchText([]string{"one two", "two three"}); got != "one two two three" {
		t.Errorf("short overlap = %q, want both kept below the minimum match", got)
	}
}
