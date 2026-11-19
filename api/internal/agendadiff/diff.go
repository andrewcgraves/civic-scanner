package agendadiff

import (
	"context"
	"strings"
)

const (
	MaxDiffTokens  = 50_000
	MaxEditTokens  = 1_000
	ContextTokens  = 8
	mergeGapTokens = 3
)

type Change struct {
	Before  string
	Removed string
	Added   string
	After   string
}

type Diff struct {
	Changes  []Change
	Coarse   bool
	TooLarge bool
}

func (d Diff) Equivalent() bool {
	return !d.TooLarge && len(d.Changes) == 0
}

type span struct {
	a0, a1, b0, b1 int
}

func Compare(ctx context.Context, old, updated string) (Diff, error) {
	if tooLarge(old, updated) {
		return Diff{TooLarge: true}, nil
	}
	a, b, err := canonical(ctx, old, updated)
	if err != nil {
		return Diff{}, err
	}
	ka, kb := keysOf(a), keysOf(b)

	prefix := 0
	for prefix < len(ka) && prefix < len(kb) && ka[prefix] == kb[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(ka)-prefix && suffix < len(kb)-prefix && ka[len(ka)-1-suffix] == kb[len(kb)-1-suffix] {
		suffix++
	}
	middle := span{a0: prefix, a1: len(ka) - suffix, b0: prefix, b1: len(kb) - suffix}
	if middle.a0 == middle.a1 && middle.b0 == middle.b1 {
		return Diff{}, nil
	}

	var (
		spans  []span
		coarse bool
	)
	if (middle.a1-middle.a0)+(middle.b1-middle.b0) > MaxDiffTokens {
		spans, coarse = []span{middle}, true
	} else if found, ok, err := myers(ctx, ka[middle.a0:middle.a1], kb[middle.b0:middle.b1], MaxEditTokens); err != nil {
		return Diff{}, err
	} else if ok {
		for _, s := range merge(found) {
			spans = append(spans, span{a0: s.a0 + prefix, a1: s.a1 + prefix, b0: s.b0 + prefix, b1: s.b1 + prefix})
		}
	} else {
		spans, coarse = []span{middle}, true
	}

	changes := make([]Change, 0, len(spans))
	for _, s := range spans {
		changes = append(changes, Change{
			Before:  b.render(max(0, s.b0-ContextTokens), s.b0, -1, b.firstWord(s.b0, s.b1)),
			Removed: a.render(s.a0, s.a1, -1, -1),
			Added:   b.render(s.b0, s.b1, -1, -1),
			After:   b.render(s.b1, min(len(b.tokens), s.b1+ContextTokens), b.lastWord(s.b0, s.b1), -1),
		})
	}
	return Diff{Changes: changes, Coarse: coarse}, nil
}

func keysOf(d document) []string {
	keys := make([]string, len(d.tokens))
	for i, t := range d.tokens {
		keys[i] = t.key
	}
	return keys
}

func (d document) firstWord(i, j int) int {
	if i < j {
		return d.tokens[i].word
	}
	return -1
}

func (d document) lastWord(i, j int) int {
	if i < j {
		return d.tokens[j-1].word
	}
	return -1
}

func (d document) render(i, j, afterWord, beforeWord int) string {
	if i >= j {
		return ""
	}
	from, to := d.tokens[i].word, d.tokens[j-1].word
	if afterWord >= 0 && from <= afterWord {
		from = afterWord + 1
	}
	if beforeWord >= 0 && to >= beforeWord {
		to = beforeWord - 1
	}
	if from > to {
		return ""
	}
	return strings.Join(d.words[from:to+1], " ")
}

func merge(spans []span) []span {
	var merged []span
	for _, s := range spans {
		if n := len(merged); n > 0 && s.a0-merged[n-1].a1 <= mergeGapTokens && s.b0-merged[n-1].b1 <= mergeGapTokens {
			merged[n-1].a1, merged[n-1].b1 = s.a1, s.b1
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

func myers(ctx context.Context, a, b []string, maxEdits int) ([]span, bool, error) {
	n, m := len(a), len(b)
	limit := min(n+m, maxEdits)
	offset := limit + 1
	v := make([]int, 2*limit+3)
	var trace [][]int
	for d := 0; d <= limit; d++ {
		if d%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		trace = append(trace, append([]int(nil), v[offset-d-1:offset+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return backtrack(trace, n, m, d), true, nil
			}
		}
	}
	return nil, false, nil
}

func backtrack(trace [][]int, x, y, d int) []span {
	var reversed []span
	for ; d > 0; d-- {
		at := func(k int) int { return trace[d][k+d+1] }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
		}
		if x == prevX {
			reversed = append(reversed, span{a0: x, a1: x, b0: prevY, b1: y})
		} else {
			reversed = append(reversed, span{a0: prevX, a1: x, b0: y, b1: y})
		}
		x, y = prevX, prevY
	}
	spans := make([]span, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		s := reversed[i]
		if n := len(spans); n > 0 && spans[n-1].a1 == s.a0 && spans[n-1].b1 == s.b0 {
			spans[n-1].a1, spans[n-1].b1 = s.a1, s.b1
			continue
		}
		spans = append(spans, s)
	}
	return spans
}
