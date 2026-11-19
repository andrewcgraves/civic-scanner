package transcribe

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	maxCutMargin      = 500 * time.Millisecond
	maxGapInset       = 250 * time.Millisecond
	textOverlapTokens = 400
	minTextOverlap    = 3
)

type view struct {
	chunk    Chunk
	segments []Segment
	words    []Word
}

func (v view) end() time.Duration { return v.chunk.Start + v.chunk.Length }

type interval struct{ start, end time.Duration }

type cut struct {
	at        time.Duration
	byWords   bool
	leftKeep  time.Duration
	rightKeep time.Duration
	leftOwn   int
	rightOwn  int
	leftDrop  int
	rightDrop int
}

func newViews(chunks []Chunk, results []Transcript) []view {
	views := make([]view, len(chunks))
	for i, chunk := range chunks {
		v := view{chunk: chunk}
		for _, s := range results[i].Segments {
			s.Start += chunk.Start
			s.End += chunk.Start
			v.segments = append(v.segments, s)
		}
		for _, w := range results[i].Words {
			w.Start += chunk.Start
			w.End += chunk.Start
			v.words = append(v.words, w)
		}
		slices.SortStableFunc(v.segments, func(a, b Segment) int { return compareDurations(a.Start, b.Start) })
		slices.SortStableFunc(v.words, func(a, b Word) int { return compareDurations(a.Start, b.Start) })
		views[i] = v
	}
	return views
}

func compareDurations(a, b time.Duration) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func stitchSegments(chunks []Chunk, results []Transcript) []Segment {
	views := newViews(chunks, results)
	cuts := make([]cut, len(views)-1)
	for i := range cuts {
		cuts[i] = chooseCut(views[i], views[i+1])
	}
	var merged []Segment
	for i, v := range views {
		var left, right *cut
		if i > 0 {
			left = &cuts[i-1]
		}
		if i < len(cuts) {
			right = &cuts[i]
		}
		for j, s := range v.segments {
			kept, ok := keepSegment(v, j, s, left, right)
			if ok {
				merged = appendMonotonic(merged, kept)
			}
		}
	}
	return merged
}

func keepSegment(v view, index int, s Segment, left, right *cut) (Segment, bool) {
	if right != nil {
		var ok bool
		if s, ok = keepBefore(v, index, s, right); !ok {
			return Segment{}, false
		}
	}
	if left != nil {
		var ok bool
		if s, ok = keepAfter(v, index, s, left); !ok {
			return Segment{}, false
		}
	}
	return s, strings.TrimSpace(s.Text) != ""
}

func keepBefore(v view, index int, s Segment, c *cut) (Segment, bool) {
	if c.byWords {
		switch {
		case s.End <= c.at:
			return s, true
		case s.Start >= c.at:
			return Segment{}, false
		}
		return trimToWords(v.words, s, func(mid time.Duration) bool { return mid < c.at })
	}
	if index == c.leftOwn {
		return s, true
	}
	return s, index != c.leftDrop && midpoint(s.Start, s.End) < c.leftKeep
}

func keepAfter(v view, index int, s Segment, c *cut) (Segment, bool) {
	if c.byWords {
		switch {
		case s.Start >= c.at:
			return s, true
		case s.End <= c.at:
			return Segment{}, false
		}
		return trimToWords(v.words, s, func(mid time.Duration) bool { return mid >= c.at })
	}
	if index == c.rightOwn {
		return s, true
	}
	return s, index != c.rightDrop && midpoint(s.Start, s.End) >= c.rightKeep
}

func trimToWords(words []Word, s Segment, keep func(time.Duration) bool) (Segment, bool) {
	var parts []string
	trimmed := Segment{Speaker: s.Speaker}
	inside := false
	for _, w := range words {
		mid := midpoint(w.Start, w.End)
		if mid < s.Start || mid > s.End {
			continue
		}
		inside = true
		if !keep(mid) {
			continue
		}
		if len(parts) == 0 {
			trimmed.Start = w.Start
		}
		trimmed.End = w.End
		parts = append(parts, strings.TrimSpace(w.Text))
	}
	if !inside {
		return s, keep(midpoint(s.Start, s.End))
	}
	if len(parts) == 0 {
		return Segment{}, false
	}
	trimmed.Text = strings.Join(parts, " ")
	return trimmed, true
}

func appendMonotonic(merged []Segment, s Segment) []Segment {
	if len(merged) == 0 {
		return append(merged, s)
	}
	frontier := merged[len(merged)-1].End
	if s.Start < frontier {
		if midpoint(s.Start, s.End) < frontier {
			return merged
		}
		s.Start = frontier
	}
	return append(merged, s)
}

func midpoint(a, b time.Duration) time.Duration { return a + (b-a)/2 }

func chooseCut(a, b view) cut {
	windowStart, windowEnd := b.chunk.Start, a.end()
	margin := min(maxCutMargin, (windowEnd-windowStart)/4)
	lo, hi := windowStart+margin, windowEnd-margin
	target := midpoint(windowStart, windowEnd)
	byWords := len(a.words) > 0 && len(b.words) > 0
	var covered []interval
	if byWords {
		covered = append(wordIntervals(a.words), wordIntervals(b.words)...)
	} else {
		covered = append(segmentIntervals(a.segments), segmentIntervals(b.segments)...)
	}
	if at, ok := freePoint(covered, lo, hi, target); ok {
		return cut{at: at, byWords: byWords, leftKeep: at, rightKeep: at, leftOwn: -1, rightOwn: -1, leftDrop: -1, rightDrop: -1}
	}
	if byWords {
		return cut{at: target, byWords: true}
	}
	return straddlingCut(a, b, target, margin)
}

func straddlingCut(a, b view, at, margin time.Duration) cut {
	c := cut{at: at, leftKeep: at, rightKeep: at, leftOwn: -1, rightOwn: -1, leftDrop: -1, rightDrop: -1}
	ai, bi := straddler(a.segments, at), straddler(b.segments, at)
	ownA := false
	switch {
	case ai < 0 && bi < 0:
		return c
	case bi < 0:
		ownA = true
	case ai < 0:
		ownA = false
	default:
		x, y := a.segments[ai], b.segments[bi]
		xComplete := x.End < a.end()-margin
		yComplete := y.Start > b.chunk.Start+margin
		switch {
		case xComplete != yComplete:
			ownA = xComplete
		case xComplete:
			ownA = midpoint(x.Start, x.End) < at
		default:
			ownA = x.End-x.Start > y.End-y.Start
		}
	}
	if ownA {
		c.leftOwn, c.rightDrop = ai, bi
		c.rightKeep = a.segments[ai].End
		return c
	}
	c.rightOwn, c.leftDrop = bi, ai
	c.leftKeep = b.segments[bi].Start
	return c
}

func straddler(segments []Segment, at time.Duration) int {
	for i, s := range segments {
		if s.Start < at && at < s.End {
			return i
		}
	}
	return -1
}

func wordIntervals(words []Word) []interval {
	out := make([]interval, 0, len(words))
	for _, w := range words {
		out = append(out, interval{w.Start, w.End})
	}
	return out
}

func segmentIntervals(segments []Segment) []interval {
	out := make([]interval, 0, len(segments))
	for _, s := range segments {
		out = append(out, interval{s.Start, s.End})
	}
	return out
}

func freePoint(covered []interval, lo, hi, target time.Duration) (time.Duration, bool) {
	if hi <= lo {
		return 0, false
	}
	slices.SortFunc(covered, func(x, y interval) int { return compareDurations(x.start, y.start) })
	best, found := time.Duration(0), false
	consider := func(gapStart, gapEnd time.Duration) {
		gapStart, gapEnd = max(gapStart, lo), min(gapEnd, hi)
		if gapEnd <= gapStart {
			return
		}
		inset := min(maxGapInset, (gapEnd-gapStart)/2)
		point := min(max(target, gapStart+inset), gapEnd-inset)
		if !found || absDuration(point-target) < absDuration(best-target) {
			best, found = point, true
		}
	}
	cursor := lo
	for _, iv := range covered {
		if iv.end <= cursor {
			continue
		}
		if iv.start > cursor {
			consider(cursor, iv.start)
		}
		cursor = max(cursor, iv.end)
		if cursor >= hi {
			break
		}
	}
	consider(cursor, hi)
	return best, found
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func stitchText(texts []string) string {
	var merged []string
	for _, text := range texts {
		tokens := strings.Fields(text)
		if len(tokens) == 0 {
			continue
		}
		merged = append(merged, tokens[textOverlap(merged, tokens):]...)
	}
	return strings.Join(merged, " ")
}

func textOverlap(merged, next []string) int {
	tail := merged[max(0, len(merged)-textOverlapTokens):]
	head := next[:min(len(next), textOverlapTokens)]
	for k := min(len(tail), len(head)); k >= minTextOverlap; k-- {
		if sameTokens(tail[len(tail)-k:], head[:k]) {
			return k
		}
	}
	return 0
}

func sameTokens(a, b []string) bool {
	for i := range a {
		if normalizeToken(a[i]) != normalizeToken(b[i]) {
			return false
		}
	}
	return true
}

func normalizeToken(s string) string {
	return strings.ToLower(strings.TrimFunc(s, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }))
}
