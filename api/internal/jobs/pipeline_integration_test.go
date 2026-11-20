//go:build integration

package jobs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm/llmtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/media"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testbin"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe/transcribetest"
)

const (
	pipelineSettleTimeout   = 3 * time.Minute
	pipelineAgendaSummary   = "The board will call the meeting to order, hear public comment and approve the consent calendar."
	pipelineRevisedSummary  = "The revised agenda adds the adoption of the 2027 board meeting calendar."
	pipelineChangeNote      = "Added item 14: adopt the 2027 board meeting calendar."
	pipelineTranscriptReply = "The board opened the meeting, took roll and approved the consent calendar without discussion."
	pipelinePublicWebURL    = "https://civic.example"
)

var pipelineTranscript = transcribe.Transcript{
	Duration: 15 * time.Second,
	Segments: []transcribe.Segment{
		{Start: 0, End: 6 * time.Second, Text: "Good morning, I call this regular meeting of the board of directors to order."},
		{Start: 6 * time.Second, End: 15 * time.Second, Text: "The clerk will take the roll, and then we will vote on the consent calendar items five through twelve."},
	},
}

type agencyEdge struct {
	t        *testing.T
	srv      *httptest.Server
	release  chan struct{}
	released sync.Once

	mu           sync.Mutex
	agenda       []byte
	agendaHits   int
	playerHits   int
	mediaHits    int
	unknownPaths []string
}

func newAgencyEdge(t *testing.T, clip string) *agencyEdge {
	t.Helper()
	e := &agencyEdge{t: t, release: make(chan struct{}), agenda: extracttest.Lines(agendaV1...)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agendas/board.pdf", func(w http.ResponseWriter, _ *http.Request) {
		e.mu.Lock()
		e.agendaHits++
		body := e.agenda
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /MediaPlayer.php", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.playerHits++
		e.mu.Unlock()
		select {
		case <-e.release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><script>var video_url="%s/media/clip.mp4";</script></html>`, e.srv.URL)
	})
	files := http.FileServer(http.Dir(filepath.Dir(clip)))
	mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.mediaHits++
		e.mu.Unlock()
		r.URL.Path = "/" + filepath.Base(clip)
		files.ServeHTTP(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.unknownPaths = append(e.unknownPaths, r.URL.String())
		e.mu.Unlock()
		http.NotFound(w, r)
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		e.releaseRecordings()
		e.srv.Close()
	})
	return e
}

func (e *agencyEdge) agendaURL() string { return e.srv.URL + "/agendas/board.pdf" }

func (e *agencyEdge) playerURL() string {
	return strings.Replace(e.srv.URL, "127.0.0.1", "localhost", 1) + "/MediaPlayer.php?view_id=8&clip_id=1438"
}

func (e *agencyEdge) releaseRecordings() { e.released.Do(func() { close(e.release) }) }

func (e *agencyEdge) publish(lines []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.agenda = extracttest.Lines(lines...)
}

type edgeHits struct{ agenda, player, media int }

func (e *agencyEdge) hits() edgeHits {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.unknownPaths) > 0 {
		e.t.Errorf("agency edge served unknown paths %v", e.unknownPaths)
	}
	return edgeHits{agenda: e.agendaHits, player: e.playerHits, media: e.mediaHits}
}

func (e *agencyEdge) downloader(t *testing.T) media.Downloader {
	t.Helper()
	guard := media.Guard{
		AllowAddress: func(netip.Addr) bool { return true },
		AllowPort:    func(string, string) bool { return true },
	}
	return media.Downloader{
		Resolver: media.Resolver{
			Fetcher: media.PageFetcher{Hosts: []string{"localhost"}, Guard: guard, UserAgent: "CivicScanner-test", Timeout: time.Minute},
			Hosts:   media.Hosts{Players: []string{"localhost"}, Media: []string{"127.0.0.1"}},
		},
		Tool: media.Tool{
			Limit:     testbin.BuildPackage(t, "../../cmd/limit"),
			Timeout:   2 * time.Minute,
			UserAgent: "CivicScanner-test",
			Env:       testbin.CoverEnv(),
			Hosts:     []string{"127.0.0.1"},
			Guard:     guard,
		},
	}
}

func agencyFetchers() (fetch.Fetcher, fetch.Fetcher) {
	cfg := config.Fetch{UserAgent: "CivicScanner-test", Timeout: 30 * time.Second, MaxBytes: 128 << 20, MaxRedirects: 5}
	firecrawl := fetch.NewFirecrawl(cfg)
	return fetch.NewFallback(fetch.NewPlain(cfg), firecrawl), firecrawl
}

func recordedClip(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=15", "-f", "lavfi", "-i", "sine=frequency=330:duration=15",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-shortest", "-movflags", "+faststart", "clip.mp4")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return filepath.Join(dir, "clip.mp4")
}

type pipelineModel struct {
	*llmtest.Fake
}

func newPipelineModel() pipelineModel {
	return pipelineModel{&llmtest.Fake{Respond: func(req llm.Request) (llm.Response, error) {
		switch {
		case isChangeRequest(req):
			return llm.Response{Text: pipelineChangeNote, FinishReason: "stop"}, nil
		case strings.Contains(req.User, "\n<transcript-"):
			return llm.Response{Text: pipelineTranscriptReply, FinishReason: "stop"}, nil
		case strings.Contains(req.User, "\n<document-") && strings.Contains(req.User, "Adopt the 2027 board meeting calendar"):
			return llm.Response{Text: pipelineRevisedSummary, FinishReason: "stop"}, nil
		case strings.Contains(req.User, "\n<document-"):
			return llm.Response{Text: pipelineAgendaSummary, FinishReason: "stop"}, nil
		}
		return llm.Response{}, &llm.Error{Err: fmt.Errorf("unexpected model request: %.200s", req.User)}
	}}}
}

type modelCalls struct{ agendaSummaries, changeNotes, transcriptSummaries, other int }

func (m pipelineModel) calls(t *testing.T) modelCalls {
	t.Helper()
	var c modelCalls
	for _, req := range m.Calls() {
		if req.Tier != llm.TierRoutine {
			t.Errorf("model call on tier %q, want routine", req.Tier)
		}
		switch {
		case isChangeRequest(req):
			c.changeNotes++
		case strings.Contains(req.User, "\n<transcript-"):
			c.transcriptSummaries++
		case strings.Contains(req.User, "\n<document-"):
			c.agendaSummaries++
		default:
			c.other++
		}
	}
	return c
}

type pipelineTranscriber struct {
	*transcribetest.Fake

	mu     sync.Mutex
	hashes []string
}

func newPipelineTranscriber() *pipelineTranscriber {
	p := &pipelineTranscriber{}
	p.Fake = &transcribetest.Fake{Respond: func(audioPath string) (transcribe.Transcript, error) {
		b, err := os.ReadFile(audioPath) //nolint:gosec
		if err != nil {
			return transcribe.Transcript{}, err
		}
		p.mu.Lock()
		p.hashes = append(p.hashes, sha256Hex(b))
		p.mu.Unlock()
		return pipelineTranscript, nil
	}}
	return p
}

func (p *pipelineTranscriber) audioHashes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.hashes)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (h *harness) settle() map[string]int {
	h.t.Helper()
	queues := []string{h.queue, h.audioQueue}
	deadline := time.Now().Add(pipelineSettleTimeout)
	for {
		var waiting int
		if err := h.pool.QueryRow(h.t.Context(),
			`SELECT count(*) FROM river_job WHERE queue = ANY($1) AND state NOT IN ('completed', 'cancelled', 'discarded')`,
			queues).Scan(&waiting); err != nil {
			h.t.Fatalf("count waiting jobs: %v", err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%d jobs still waiting or running after %s: %s", waiting, pipelineSettleTimeout, h.describeJobsIn(queues))
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows, err := h.pool.Query(h.t.Context(), `SELECT kind, state, errors::text FROM river_job WHERE queue = ANY($1) ORDER BY id`, queues)
	if err != nil {
		h.t.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var kind, state string
		var errs *string
		if err := rows.Scan(&kind, &state, &errs); err != nil {
			h.t.Fatal(err)
		}
		counts[kind]++
		if state != "completed" {
			h.t.Errorf("job %s ended %s, errors %v", kind, state, deref(errs))
		}
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return counts
}

func (h *harness) describeJobsIn(queues []string) string {
	rows, err := h.pool.Query(context.WithoutCancel(h.t.Context()),
		`SELECT kind, state, attempt, errors::text FROM river_job WHERE queue = ANY($1) ORDER BY id`, queues)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var kind, state string
		var attempt int
		var errs *string
		if err := rows.Scan(&kind, &state, &attempt, &errs); err != nil {
			return err.Error()
		}
		lines = append(lines, fmt.Sprintf("%s %s attempt %d %s", kind, state, attempt, deref(errs)))
	}
	return strings.Join(lines, "; ")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *harness) rerunCheck(slug string) {
	h.t.Helper()
	if checks := h.manualCheck(slug); len(checks) != 1 || checks[0].Duplicate {
		h.t.Fatalf("manual check = %+v", checks)
	}
}

func waitForEvent(t *testing.T, pool *pgxpool.Pool, meetingID func() (uuid.UUID, bool), want store.EventType) {
	t.Helper()
	deadline := time.Now().Add(pipelineSettleTimeout)
	for time.Now().Before(deadline) {
		if id, ok := meetingID(); ok && slices.Contains(eventTypesOf(t, pool, id), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s event after %s", want, pipelineSettleTimeout)
}

func publicAPI(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	h := httpapi.NewHandler(store.New(pool), logger, "test",
		httpapi.WithPublicWebURL(pipelinePublicWebURL),
		httpapi.WithFlags(func(context.Context, string) bool { return true }))
	srv := httptest.NewServer(httpapi.NewRouter(h, logger))
	t.Cleanup(srv.Close)
	return srv
}

func getPublic(t *testing.T, url string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}
	return body
}

func meetingDetail(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) v1.MeetingDetail {
	t.Helper()
	var d v1.MeetingDetail
	if err := json.Unmarshal(getPublic(t, publicAPI(t, pool).URL+"/v1/meetings/"+meetingID.String()), &d); err != nil {
		t.Fatalf("decode meeting detail: %v", err)
	}
	return d
}

type feedItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	GUID        string `xml:"guid"`
}

func agencyFeedItems(t *testing.T, pool *pgxpool.Pool, slug string) []feedItem {
	t.Helper()
	var feed struct {
		Channel struct {
			Items []feedItem `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(getPublic(t, publicAPI(t, pool).URL+"/v1/agencies/"+slug+"/feed.xml"), &feed); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	return feed.Channel.Items
}

func eventsOf(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) []store.MeetingEvent {
	t.Helper()
	events, err := store.New(pool).ListMeetingEvents(t.Context(), meetingID)
	if err != nil {
		t.Fatalf("list meeting events: %v", err)
	}
	return events
}

func agendaPayload(t *testing.T, e store.MeetingEvent) store.AgendaEventPayload {
	t.Helper()
	var p store.AgendaEventPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("decode %s payload: %v", e.Type, err)
	}
	return p
}

func pipelineMeeting(edge *agencyEdge) sources.Meeting {
	m := boardMeeting("e2e", fixedNow.AddDate(0, 0, -3), sources.StatusHeld)
	m.AgendaURL = edge.agendaURL()
	m.RecordingURL = edge.playerURL()
	return m
}

func feedTitles(items []feedItem) []string {
	titles := make([]string, 0, len(items))
	for _, it := range items {
		titles = append(titles, it.Title)
	}
	return titles
}

func checkFeed(t *testing.T, items []feedItem, meetingID uuid.UUID, wantPrefixes []string, wantInDescription []string) {
	t.Helper()
	if len(items) != len(wantPrefixes) {
		t.Fatalf("feed items = %q, want %d items starting %q", feedTitles(items), len(wantPrefixes), wantPrefixes)
	}
	guids := map[string]bool{}
	for i, it := range items {
		if !strings.HasPrefix(it.Title, wantPrefixes[i]+"Board of Directors e2e") {
			t.Errorf("feed item %d title = %q, want %q followed by the meeting title", i, it.Title, wantPrefixes[i])
		}
		if want := pipelinePublicWebURL + "/meetings/" + meetingID.String(); it.Link != want {
			t.Errorf("feed item %d link = %q, want %q", i, it.Link, want)
		}
		if guids[it.GUID] {
			t.Errorf("feed item %d repeats guid %s", i, it.GUID)
		}
		guids[it.GUID] = true
		if !strings.Contains(it.Description, wantInDescription[i]) {
			t.Errorf("feed item %d description = %q, want it to contain %q", i, it.Description, wantInDescription[i])
		}
	}
}

func TestPipelineRunsEndToEndFromAgencyCheckToTranscriptSummary(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	pool := testdb.Pool(t)
	slug := "jobs-test-pipeline-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	edge := newAgencyEdge(t, recordedClip(t))
	source := pipelineMeeting(edge)
	model := newPipelineModel()
	transcriber := newPipelineTranscriber()
	bucket := realBucket(t)
	fetcher, firecrawl := agencyFetchers()
	h := start(t, pool, jobs.Config{
		Registry:      registry(map[string]sources.Adapter{slug: sourcestest.Returning(source)}),
		Fetcher:       fetcher,
		Fallback:      firecrawl,
		Extractor:     subprocessExtractor(t),
		Storage:       bucket,
		Audio:         edge.downloader(t),
		LLM:           model,
		SummaryTier:   llm.TierRoutine,
		Transcription: jobs.Transcription{Client: transcriber, Model: "fake/transcriber"},
	})
	meetingID := func() (uuid.UUID, bool) {
		m, ok := meetingsOf(t, pool, agencyID)["e2e"]
		return m.ID, ok
	}

	h.check(slug, day(0))
	waitForEvent(t, pool, meetingID, store.EventAgendaPublished)
	edge.releaseRecordings()
	jobsRun := h.settle()

	meeting, ok := meetingsOf(t, pool, agencyID)["e2e"]
	if !ok {
		t.Fatal("meeting not stored")
	}
	recs := recordingsOf(t, pool, meeting.ID)
	if len(recs) == 1 && recs[0].Audio != nil {
		key := recs[0].Audio.Key
		t.Cleanup(func() { _ = bucket.Delete(context.WithoutCancel(t.Context()), key) })
	}

	t.Run("first check", func(t *testing.T) {
		if meeting.Title != source.Title || !meeting.StartsAt.Equal(source.StartsAt) || meeting.Status != "held" || meeting.SourceURL != source.PageURL {
			t.Errorf("meeting = %+v, want %+v", meeting, source)
		}
		wantJobs := map[string]int{
			"check_agency": 1, "process_agenda": 1, "summarize_agenda": 1,
			"download_audio": 1, "transcribe_audio": 1, "summarize_transcript": 1,
		}
		if !mapsEqual(jobsRun, wantJobs) {
			t.Errorf("jobs = %v, want %v", jobsRun, wantJobs)
		}

		versions := versionsOf(t, pool, meeting.ID)
		if len(versions) != 1 {
			t.Fatalf("agenda versions = %d, want 1", len(versions))
		}
		v := versions[0]
		wantText := strings.Join(agendaV1, "\n")
		if v.Version != 1 || v.Text != wantText || v.ContentHash != store.HashContent(wantText) || v.SourceURL != source.AgendaURL || v.ChangeEventPending {
			t.Errorf("agenda version = %+v, want version 1 of the PDF's text from %s", v, source.AgendaURL)
		}
		if v.Summary == nil || *v.Summary != pipelineAgendaSummary || v.ChangeNote != nil {
			t.Errorf("agenda summary = %v, change note = %v; want the model's summary and no note", deref(v.Summary), deref(v.ChangeNote))
		}

		if len(recs) != 1 || recs[0].SourceURL != source.RecordingURL || recs[0].Audio == nil || recs[0].Audio.Key != jobs.AudioKey(recs[0].ID) {
			t.Fatalf("recordings = %+v, want one with audio from %s", recs, source.RecordingURL)
		}
		stored := objectBytes(t, bucket, recs[0].Audio.Key)
		path := filepath.Join(t.TempDir(), "stored.mp3")
		if err := os.WriteFile(path, stored, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := media.CheckMP3(path, media.DefaultMinBytes); err != nil {
			t.Errorf("stored audio is not a usable mp3: %v", err)
		}
		if got := transcriber.audioHashes(); len(got) != 1 || got[0] != sha256Hex(stored) {
			t.Errorf("transcriber received %d files, hashes %v; want the stored object %s once", len(got), got, sha256Hex(stored))
		}

		tr, ok := transcriptOf(t, pool, recs[0].ID)
		if !ok {
			t.Fatal("no transcript")
		}
		wantTranscript := pipelineTranscript.Segments[0].Text + " " + pipelineTranscript.Segments[1].Text
		if tr.Text != wantTranscript || len(tr.Segments) != 2 || tr.Segments[1].Start != 6*time.Second || tr.Segments[1].End != 15*time.Second {
			t.Errorf("transcript = %+v", tr)
		}
		if tr.Summary == nil || *tr.Summary != pipelineTranscriptReply {
			t.Errorf("transcript summary = %q, want the model's summary", deref(tr.Summary))
		}

		events := eventsOf(t, pool, meeting.ID)
		if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventTranscriptReady) {
			t.Fatalf("events = %v, want agenda_published then transcript_ready", got)
		}
		if p := agendaPayload(t, events[0]); p.AgendaVersionID != v.ID || p.Version != 1 {
			t.Errorf("agenda_published payload = %+v, want version 1 %s", p, v.ID)
		}
		var ready store.TranscriptEventPayload
		if err := json.Unmarshal(events[1].Payload, &ready); err != nil || ready.TranscriptID != tr.ID || ready.RecordingID != recs[0].ID {
			t.Errorf("transcript_ready payload = %s, %v; want transcript %s of recording %s", events[1].Payload, err, tr.ID, recs[0].ID)
		}

		if got := model.calls(t); got != (modelCalls{agendaSummaries: 1, transcriptSummaries: 1}) {
			t.Errorf("model calls = %+v, want one agenda summary and one transcript summary", got)
		}
		if hits := edge.hits(); hits.agenda != 1 || hits.player != 1 || hits.media == 0 {
			t.Errorf("agency edge hits = %+v, want the agenda and the player page once and the media at least once", hits)
		}

		d := meetingDetail(t, pool, meeting.ID)
		if d.Id != meeting.ID || d.Title != source.Title || d.Status != v1.MeetingStatus("held") || d.SourceUrl == nil || *d.SourceUrl != source.PageURL || d.Agency.Slug != slug {
			t.Errorf("meeting detail = %+v", d)
		}
		if a := d.Agenda; a == nil || a.Version != 1 || a.DocumentUrl != source.AgendaURL || a.Summary == nil || *a.Summary != pipelineAgendaSummary || a.ChangeNote != nil {
			t.Errorf("meeting detail agenda = %+v", d.Agenda)
		}
		if len(d.Recordings) != 1 || d.Recordings[0].SourceUrl != source.RecordingURL || d.Recordings[0].Transcript == nil {
			t.Fatalf("meeting detail recordings = %+v", d.Recordings)
		}
		dt := d.Recordings[0].Transcript
		wantSegments := []v1.TranscriptSegment{
			{StartMs: 0, EndMs: 6000, Text: pipelineTranscript.Segments[0].Text},
			{StartMs: 6000, EndMs: 15000, Text: pipelineTranscript.Segments[1].Text},
		}
		if !slices.Equal(dt.Segments, wantSegments) || dt.Summary == nil || *dt.Summary != pipelineTranscriptReply {
			t.Errorf("meeting detail transcript = %+v", dt)
		}

		checkFeed(t, agencyFeedItems(t, pool, slug), meeting.ID,
			[]string{"Transcript ready: ", "Agenda posted: "},
			[]string{"transcript", pipelineAgendaSummary})
	})
	if t.Failed() {
		return
	}

	edge.publish(agendaV2)
	h.rerunCheck(slug)
	jobsRun = h.settle()

	t.Run("second check with a revised agenda", func(t *testing.T) {
		wantJobs := map[string]int{
			"check_agency": 2, "process_agenda": 2, "describe_agenda_change": 1, "summarize_agenda": 2,
			"download_audio": 1, "transcribe_audio": 1, "summarize_transcript": 1,
		}
		if !mapsEqual(jobsRun, wantJobs) {
			t.Errorf("jobs = %v, want %v", jobsRun, wantJobs)
		}
		versions := versionsOf(t, pool, meeting.ID)
		if len(versions) != 2 {
			t.Fatalf("agenda versions = %d, want 2", len(versions))
		}
		v2, v1st := versions[0], versions[1]
		wantText := strings.Join(agendaV2, "\n")
		if v2.Version != 2 || v2.Text != wantText || v2.ChangeEventPending {
			t.Errorf("version 2 = %+v, want the revised text with its event recorded", v2)
		}
		if v2.Summary == nil || *v2.Summary != pipelineRevisedSummary || v2.ChangeNote == nil || *v2.ChangeNote != pipelineChangeNote {
			t.Errorf("version 2 summary = %q, note = %q", deref(v2.Summary), deref(v2.ChangeNote))
		}
		if v1st.Version != 1 || v1st.Summary == nil || *v1st.Summary != pipelineAgendaSummary {
			t.Errorf("version 1 = %+v, want it unchanged", v1st)
		}

		events := eventsOf(t, pool, meeting.ID)
		if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished, store.EventTranscriptReady, store.EventAgendaChanged) {
			t.Fatalf("events = %v, want agenda_published, transcript_ready, agenda_changed", got)
		}
		if p := agendaPayload(t, events[2]); p.AgendaVersionID != v2.ID || p.Version != 2 || p.ChangeNote == nil || *p.ChangeNote != pipelineChangeNote || p.NoteSource != store.NoteSource("model") {
			t.Errorf("agenda_changed payload = %s", events[2].Payload)
		}

		if got := model.calls(t); got != (modelCalls{agendaSummaries: 2, changeNotes: 1, transcriptSummaries: 1}) {
			t.Errorf("model calls = %+v, want one more agenda summary and one change note", got)
		}
		if n := len(transcriber.Calls()); n != 1 {
			t.Errorf("transcriber calls = %d, want still 1", n)
		}
		if hits := edge.hits(); hits.agenda != 2 || hits.player != 1 {
			t.Errorf("agency edge hits = %+v, want a second agenda fetch and no second recording fetch", hits)
		}
		recs := recordingsOf(t, pool, meeting.ID)
		if len(recs) != 1 || countTranscripts(t, pool, recs[0].ID) != 1 {
			t.Errorf("recordings = %+v, want the one recording with its one transcript", recs)
		}

		d := meetingDetail(t, pool, meeting.ID)
		if a := d.Agenda; a == nil || a.Version != 2 || a.DocumentUrl != source.AgendaURL || a.Summary == nil || *a.Summary != pipelineRevisedSummary || a.ChangeNote == nil || *a.ChangeNote != pipelineChangeNote {
			t.Errorf("meeting detail agenda = %+v", d.Agenda)
		}
		if len(d.Recordings) != 1 || d.Recordings[0].Transcript == nil || d.Recordings[0].Transcript.Summary == nil || *d.Recordings[0].Transcript.Summary != pipelineTranscriptReply {
			t.Errorf("meeting detail recordings = %+v", d.Recordings)
		}

		checkFeed(t, agencyFeedItems(t, pool, slug), meeting.ID,
			[]string{"Agenda updated: ", "Transcript ready: ", "Agenda posted: "},
			[]string{pipelineChangeNote, "transcript", pipelineAgendaSummary})
	})
	if t.Failed() {
		return
	}

	eventsBefore := eventsOf(t, pool, meeting.ID)
	versionsBefore := versionsOf(t, pool, meeting.ID)
	modelBefore := model.calls(t)
	h.rerunCheck(slug)
	jobsRun = h.settle()

	t.Run("third check with nothing changed", func(t *testing.T) {
		wantJobs := map[string]int{
			"check_agency": 3, "process_agenda": 3, "describe_agenda_change": 1, "summarize_agenda": 2,
			"download_audio": 1, "transcribe_audio": 1, "summarize_transcript": 1,
		}
		if !mapsEqual(jobsRun, wantJobs) {
			t.Errorf("jobs = %v, want %v", jobsRun, wantJobs)
		}
		if got := versionsOf(t, pool, meeting.ID); !slices.EqualFunc(got, versionsBefore, sameVersion) {
			t.Errorf("versions changed: %+v, was %+v", got, versionsBefore)
		}
		if got := eventsOf(t, pool, meeting.ID); !slices.EqualFunc(got, eventsBefore, func(a, b store.MeetingEvent) bool { return a.ID == b.ID }) {
			t.Errorf("events = %v, was %v", eventTypesOf(t, pool, meeting.ID), eventsBefore)
		}
		if got := model.calls(t); got != modelBefore {
			t.Errorf("model calls = %+v, want still %+v", got, modelBefore)
		}
		if n := len(transcriber.Calls()); n != 1 {
			t.Errorf("transcriber calls = %d, want still 1", n)
		}
		if hits := edge.hits(); hits.agenda != 3 || hits.player != 1 {
			t.Errorf("agency edge hits = %+v, want a third agenda fetch and no recording fetch", hits)
		}
		if n := len(agencyFeedItems(t, pool, slug)); n != 3 {
			t.Errorf("feed items = %d, want still 3", n)
		}
	})
}

func TestPipelineWithOnlyTheCheckFlagOnStoresAgendasAndCallsNothingElse(t *testing.T) {
	pool := testdb.Pool(t)
	slug := "jobs-test-pipeline-off-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	edge := newAgencyEdge(t, filepath.Join(t.TempDir(), "clip.mp4"))
	edge.releaseRecordings()
	source := pipelineMeeting(edge)
	model := newPipelineModel()
	transcriber := newPipelineTranscriber()
	fetcher, firecrawl := agencyFetchers()
	h := start(t, pool, jobs.Config{
		Registry:      registry(map[string]sources.Adapter{slug: sourcestest.Returning(source)}),
		Fetcher:       fetcher,
		Fallback:      firecrawl,
		Extractor:     subprocessExtractor(t),
		Audio:         edge.downloader(t),
		LLM:           model,
		Transcription: jobs.Transcription{Client: transcriber, Model: "fake/transcriber"},
		Enabled:       func(_ context.Context, name string) bool { return name == jobs.FlagCheckAgencies },
	})

	h.check(slug, day(0))
	jobsRun := h.settle()

	meeting, ok := meetingsOf(t, pool, agencyID)["e2e"]
	if !ok || meeting.Status != "held" || meeting.SourceURL != source.PageURL {
		t.Fatalf("meeting = %+v, %v", meeting, ok)
	}
	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 1 || versions[0].Text != strings.Join(agendaV1, "\n") || versions[0].Summary != nil || versions[0].ChangeNote != nil {
		t.Errorf("versions = %+v, want the agenda text without a summary", versions)
	}
	if got := eventTypesOf(t, pool, meeting.ID); !sameTypes(got, store.EventAgendaPublished) {
		t.Errorf("events = %v, want only agenda_published", got)
	}
	if recs := recordingsOf(t, pool, meeting.ID); len(recs) != 0 {
		t.Errorf("recordings = %+v, want none", recs)
	}
	for _, kind := range []string{"download_audio", "transcribe_audio", "summarize_transcript", "describe_agenda_change"} {
		if jobsRun[kind] != 0 {
			t.Errorf("jobs = %v, want no %s", jobsRun, kind)
		}
	}
	if n := len(model.Calls()); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
	if n := len(transcriber.Calls()); n != 0 {
		t.Errorf("transcriber calls = %d, want 0", n)
	}
	if hits := edge.hits(); hits != (edgeHits{agenda: 1}) {
		t.Errorf("agency edge hits = %+v, want only the agenda", hits)
	}
	d := meetingDetail(t, pool, meeting.ID)
	if d.Agenda == nil || d.Agenda.Summary != nil || len(d.Recordings) != 0 {
		t.Errorf("meeting detail = %+v", d)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sameVersion(a, b store.AgendaVersion) bool {
	return a.ID == b.ID && a.Version == b.Version && a.ContentHash == b.ContentHash &&
		deref(a.Summary) == deref(b.Summary) && deref(a.ChangeNote) == deref(b.ChangeNote) && a.UpdatedAt.Equal(b.UpdatedAt)
}
