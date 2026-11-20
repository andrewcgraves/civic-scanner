package transcribe

import (
	"strconv"
	"strings"
	"time"
)

const (
	pauseBreak     = 1500 * time.Millisecond
	maxSegmentSpan = 30 * time.Second
)

type elevenLabsWord struct {
	Text      string  `json:"text"`
	Type      string  `json:"type"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	SpeakerID string  `json:"speaker_id"`
}

type azurePhrase struct {
	Text                 string   `json:"text"`
	OffsetMilliseconds   *float64 `json:"offsetMilliseconds"`
	DurationMilliseconds *float64 `json:"durationMilliseconds"`
	Speaker              *int     `json:"speaker"`
}

func azureSegments(phrases []azurePhrase) []Segment {
	segments := make([]Segment, 0, len(phrases))
	for _, p := range phrases {
		text := strings.TrimSpace(p.Text)
		if p.OffsetMilliseconds == nil || p.DurationMilliseconds == nil || text == "" {
			continue
		}
		start := time.Duration(*p.OffsetMilliseconds * float64(time.Millisecond))
		s := Segment{Start: start, End: start + time.Duration(*p.DurationMilliseconds*float64(time.Millisecond)), Text: text}
		if p.Speaker != nil {
			s.Speaker = strconv.Itoa(*p.Speaker)
		}
		segments = append(segments, s)
	}
	return segments
}

func segmentsFromWords(words []elevenLabsWord) []Segment {
	var segments []Segment
	var text strings.Builder
	var current Segment
	open := false
	flush := func() {
		if open {
			current.Text = strings.TrimSpace(text.String())
			if current.Text != "" {
				segments = append(segments, current)
			}
		}
		text.Reset()
		open = false
	}
	for _, w := range words {
		if w.Type == "spacing" {
			if open {
				text.WriteString(w.Text)
			}
			continue
		}
		start, end := seconds(w.Start), seconds(w.End)
		if open && (w.SpeakerID != current.Speaker || start-current.End >= pauseBreak || end-current.Start > maxSegmentSpan) {
			flush()
		}
		if !open {
			current = Segment{Start: start, Speaker: w.SpeakerID}
			open = true
		}
		current.End = end
		text.WriteString(w.Text)
		if endsSentence(w.Text) {
			flush()
		}
	}
	flush()
	return segments
}

func endsSentence(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "!")
}

func gatewayWords(words []elevenLabsWord) []Word {
	var out []Word
	for _, w := range words {
		text := strings.TrimSpace(w.Text)
		if w.Type == "spacing" || text == "" {
			continue
		}
		start, end := seconds(w.Start), seconds(w.End)
		out = append(out, Word{Start: start, End: max(end, start), Text: text, Speaker: w.SpeakerID})
	}
	return out
}
