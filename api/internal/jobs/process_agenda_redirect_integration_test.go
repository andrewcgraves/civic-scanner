//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract/extracttest"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestProcessAgendaStoresTheStableURLNotTheRedirectTarget(t *testing.T) {
	const blobPath = "/sbcta/Meetings/100/Packet_20261029120705111.pdf"
	const signature = "sv=2017-04-17&sr=b&sig=c2lnbmF0dXJlLXRoYXQtbXVzdC1ub3QtbGVhaw%3D%3D&st=2026-11-19T07%3A29%3A39Z&se=2026-11-21T07%3A29%3A39Z&sp=r"
	var blobHits atomic.Int32
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != blobPath || r.URL.RawQuery != signature {
			http.NotFound(w, r)
			return
		}
		blobHits.Add(1)
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(extracttest.Lines(agendaV1...))
	}))
	t.Cleanup(blob.Close)
	target := strings.Replace(blob.URL, "127.0.0.1", "localhost", 1) + blobPath + "?" + signature
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	stableURL := origin.URL + "/Public/CompiledDocument?meetingTemplateId=645&compileOutputType=1"

	pool := testdb.Pool(t)
	agencyID := seedAgency(t, pool, "jobs-test-redirect-"+randomSuffix(t), "live")
	meeting := seedMeeting(t, pool, agencyID, "100")

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.WithoutCancel(t.Context())
	queue := "jobs_test_" + randomSuffix(t)
	client, err := jobs.NewClient(pool, logger, jobs.Config{
		Enabled:        func(context.Context, string) bool { return true },
		Clock:          sourcestest.NewClock(fixedNow),
		Fetcher:        fetch.NewPlain(config.Fetch{UserAgent: "CivicScanner/test", Timeout: 10 * time.Second, MaxBytes: 1 << 20, MaxRedirects: 5}, fetch.AllowAddress(netip.Addr.IsLoopback)),
		Extractor:      &extract.Extractor{PDF: extract.InProcess{}},
		Queue:          queue,
		AudioQueue:     queue + "_audio",
		NoPeriodicJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop(ctx) })

	res, err := client.Insert(ctx, jobs.ProcessAgendaArgs{MeetingID: meeting.ID, AgendaURL: stableURL}, &river.InsertOpts{Queue: queue, ScheduledAt: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, res.Job.ID, "completed")

	if blobHits.Load() != 1 {
		t.Fatalf("redirect target fetched %d times, want 1", blobHits.Load())
	}
	versions := versionsOf(t, pool, meeting.ID)
	if len(versions) != 1 || versions[0].SourceURL != stableURL || versions[0].Text != strings.Join(agendaV1, "\n") {
		t.Fatalf("versions = %+v, want one with source url %s", versions, stableURL)
	}

	api := httptest.NewServer(httpapi.NewRouter(httpapi.NewHandler(store.New(pool), slog.New(slog.DiscardHandler), "test",
		httpapi.WithFlags(func(context.Context, string) bool { return true })), slog.New(slog.DiscardHandler)))
	t.Cleanup(api.Close)
	resp, err := http.Get(api.URL + "/v1/meetings/" + meeting.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var detail struct {
		Agenda *struct {
			DocumentURL string `json:"documentUrl"`
		} `json:"agenda"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET meeting: status %d, decode %v", resp.StatusCode, err)
	}
	if detail.Agenda == nil || detail.Agenda.DocumentURL != stableURL {
		t.Errorf("API agenda = %+v, want documentUrl %s", detail.Agenda, stableURL)
	}

	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agenda_versions v WHERE v.meeting_id = $1 AND (v::text LIKE '%sig=%' OR v::text LIKE '%Packet_2026%' OR v::text LIKE '%localhost:%')`, meeting.ID).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("agenda versions holding the redirect target: %d (%v)", stored, err)
	}
	for _, leak := range []string{"sig=", "Packet_20261029120705111", "localhost:"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("logs contain %q from the redirect target:\n%s", leak, logs)
		}
	}
	if !strings.Contains(logs.String(), "agenda processed") {
		t.Errorf("no agenda processed log:\n%s", logs)
	}
}
