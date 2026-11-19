package media

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

const (
	sentencePeriod = 13.5
	sentenceLength = 12.0
	wordPeriod     = 0.5
	wordLength     = 0.3
	meetingSeconds = 1500.0
)

var silencePattern = regexp.MustCompile(`silence_(start|end): (-?[0-9.]+)`)

type sound struct{ start, end float64 }

func sounds(t *testing.T, path string) []sound {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-hide_banner", "-i", path, //nolint:gosec
		"-af", "silencedetect=noise=-30dB:d=1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Errorf("silencedetect: %v\n%s", err, out)
		return nil
	}
	probe, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-show_entries", "format=duration", //nolint:gosec
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Errorf("ffprobe: %v", err)
		return nil
	}
	total, _ := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64)
	var found []sound
	cursor, inSound := 0.0, true
	for _, m := range silencePattern.FindAllStringSubmatch(string(out), -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		if m[1] == "start" {
			if inSound && v > cursor {
				found = append(found, sound{cursor, v})
			}
			inSound = false
			continue
		}
		cursor, inSound = v, true
	}
	if inSound && total > cursor {
		found = append(found, sound{cursor, total})
	}
	return found
}

func silenceSegmentingModel(t *testing.T, edgeSegments *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(64 << 20); err != nil { //nolint:gosec
			t.Errorf("parse upload: %v", err)
			return
		}
		f, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("upload: %v", err)
			return
		}
		body, _ := io.ReadAll(f)
		path := filepath.Join(t.TempDir(), filepath.Base(header.Filename))
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Error(err)
			return
		}
		var segments []map[string]any
		found := sounds(t, path)
		for i, s := range found {
			if i == 0 && s.start < 0.05 || i == len(found)-1 && s.end > 599.9 {
				edgeSegments.Add(1)
			}
			n := int(math.Round((s.end - s.start + wordPeriod - wordLength) / wordPeriod))
			segments = append(segments, map[string]any{"start": s.start, "end": s.end, "text": strings.TrimSpace(strings.Repeat("w ", n))})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"duration": 600, "text": "", "segments": segments})
	}))
}

func TestChunkStitchingKeepsEveryWordOnceAcrossBoundaries(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	expr := fmt.Sprintf("if(lt(mod(t,%g),%g)*lt(mod(mod(t,%g),%g),%g),0.5*sin(2*PI*440*t),0)",
		sentencePeriod, sentenceLength, sentencePeriod, wordPeriod, wordLength)
	generate(t, dir, "-f", "lavfi", "-i", "aevalsrc=exprs='"+expr+"':s=16000:d="+strconv.Itoa(int(meetingSeconds)),
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k", "meeting.mp3")
	var edgeSegments atomic.Int32
	srv := silenceSegmentingModel(t, &edgeSegments)
	defer srv.Close()
	cfg := config.Transcription{
		Provider: config.TranscriptionGroq, APIKey: "k", BaseURL: srv.URL, Model: "m",
		Timeout: time.Minute, DirectUploadBytes: 1024, MaxChunkBytes: 32 << 20,
	}
	client, ok := transcribe.New(cfg, transcribe.WithSplitter(NewSplitter(realTool(t)))).(transcribe.Resumable)
	if !ok {
		t.Fatal("client is not resumable")
	}

	got, err := client.TranscribeWith(t.Context(), filepath.Join(dir, "meeting.mp3"), transcribe.Options{Concurrency: 3, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("TranscribeWith: %v", err)
	}

	if n := edgeSegments.Load(); n < 4 {
		t.Errorf("segments cut by a chunk edge = %d, want speech crossing both boundaries from both sides", n)
	}
	for i := 1; i < len(got.Segments); i++ {
		if got.Segments[i].Start < got.Segments[i-1].End {
			t.Errorf("segment %d starts at %s, before the previous one ends at %s", i, got.Segments[i].Start, got.Segments[i-1].End)
		}
	}
	words, duplicated, dropped := 0, 0, 0
	for k := 0; float64(k)*sentencePeriod < meetingSeconds; k++ {
		for j := 0; float64(j)*wordPeriod < sentenceLength; j++ {
			mid := float64(k)*sentencePeriod + float64(j)*wordPeriod + wordLength/2
			if mid > meetingSeconds {
				continue
			}
			words++
			n := 0
			for _, s := range got.Segments {
				if s.Start.Seconds() <= mid && mid < s.End.Seconds() {
					n++
				}
			}
			switch {
			case n == 0:
				dropped++
			case n > 1:
				duplicated++
			}
		}
	}
	textWords := len(strings.Fields(got.Text))
	t.Logf("words %d, in text %d, duplicated %d, dropped %d, segments %d", words, textWords, duplicated, dropped, len(got.Segments))
	if duplicated != 0 || dropped != 0 || textWords != words {
		t.Errorf("of %d words, %d duplicated, %d dropped, %d in the text", words, duplicated, dropped, textWords)
	}
}
