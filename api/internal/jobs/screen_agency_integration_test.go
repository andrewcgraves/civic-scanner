//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func agencySite(pages map[string]string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		body, ok := pages[req.URL]
		if !ok {
			return fetch.Response{}, &fetch.Error{StatusCode: http.StatusNotFound, Err: errors.New("404 " + req.URL)}
		}
		return fetch.Response{Status: 200, ContentType: "text/html", Body: []byte(body), FinalURL: req.URL}, nil
	}}
}

func (h *harness) screen(agencyID uuid.UUID, opts *river.InsertOpts) int64 {
	h.t.Helper()
	if opts == nil {
		opts = &river.InsertOpts{}
	}
	opts.Queue = h.queue
	res, err := h.client.Insert(h.t.Context(), jobs.ScreenAgencyArgs{AgencyID: agencyID}, opts)
	if err != nil {
		h.t.Fatalf("insert screen job: %v", err)
	}
	return res.Job.ID
}

func (h *harness) screenJobs(agencyID uuid.UUID) []int64 {
	h.t.Helper()
	res, err := h.client.JobList(h.t.Context(), river.NewJobListParams().Queues(h.queue).Kinds(jobs.ScreenAgencyArgs{}.Kind()).First(100))
	if err != nil {
		h.t.Fatal(err)
	}
	var ids []int64
	for _, j := range res.Jobs {
		var args jobs.ScreenAgencyArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			h.t.Fatal(err)
		}
		if args.AgencyID == agencyID {
			ids = append(ids, j.ID)
		}
	}
	return ids
}

func screening(t *testing.T, s *store.Store, agencyID uuid.UUID) (store.AgencyScreening, bool) {
	t.Helper()
	sc, err := s.GetAgencyScreening(t.Context(), agencyID)
	if errors.Is(err, store.ErrNotFound) {
		return store.AgencyScreening{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return sc, true
}

func screeningSubmitter(t *testing.T, s *store.Store) store.User {
	t.Helper()
	workosID := "user_screen_test_" + uuid.NewString()
	user, _, err := s.EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workosID})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestSubmissionIsScreenedAndTheReportStored(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	key := "civ47-" + uuid.NewString() + ".example.org"
	website := "https://" + key
	fake := agencySite(map[string]string{
		website:               `<a href="/meetings">Board meetings</a>`,
		website + "/meetings": `<a href="https://metro.legistar.com/Calendar.aspx">Calendar</a><iframe src="https://www.youtube.com/embed/x"></iframe>`,
	})
	h := start(t, pool, jobs.Config{Screener: fake})
	user := screeningSubmitter(t, s)
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id IN (SELECT agency_id FROM agency_submissions WHERE submitted_by = $1)`, user.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, user.ID)
	})

	sub, err := s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{
		UserID: user.ID, Name: "Screened Transit", Website: website, WebsiteKey: key,
		FollowUp: jobs.EnqueueScreenAgency{Client: h.client, Queue: h.queue, Enabled: func(context.Context, string) bool { return true }},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := h.screenJobs(sub.AgencyID)
	if len(ids) != 1 {
		t.Fatalf("screen jobs = %v, want 1", ids)
	}
	testdb.WaitForJobState(t, pool, ids[0], "completed")

	sc, ok := screening(t, s, sub.AgencyID)
	if !ok {
		t.Fatal("no screening stored")
	}
	if sc.Status != store.ScreeningComplete || sc.Verdict != "both" || sc.Failure != "" || !sc.ScreenedAt.Equal(fixedNow) {
		t.Errorf("screening = %+v", sc)
	}
	if len(sc.Evidence) != 2 || sc.Evidence[0].URL != "https://metro.legistar.com/Calendar.aspx" || sc.Evidence[1].Signal != "youtube" {
		t.Errorf("evidence = %+v", sc.Evidence)
	}
	if len(sc.Pages) != 3 || sc.Pages[1].URL != website+"/meetings" || sc.Pages[2].Error == "" {
		t.Errorf("pages = %+v", sc.Pages)
	}
}

func TestSubmissionIsNotScreenedWithTheFlagOff(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	h := start(t, pool, jobs.Config{Screener: agencySite(nil)})
	user := screeningSubmitter(t, s)
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id IN (SELECT agency_id FROM agency_submissions WHERE submitted_by = $1)`, user.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	key := "civ47-" + uuid.NewString() + ".example.org"
	var asked string
	sub, err := s.CreateAgencySubmission(ctx, store.AgencySubmissionInput{
		UserID: user.ID, Name: "Unscreened", Website: "https://" + key, WebsiteKey: key,
		FollowUp: jobs.EnqueueScreenAgency{Client: h.client, Queue: h.queue, Enabled: func(_ context.Context, flag string) bool {
			asked = flag
			return false
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked != jobs.FlagScreenAgencies {
		t.Errorf("flag asked = %q", asked)
	}
	if ids := h.screenJobs(sub.AgencyID); len(ids) != 0 {
		t.Errorf("screen jobs = %v, want none", ids)
	}
}

func TestScreeningFailureIsStoredNotGuessed(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	slug := "screen-fail-" + randomSuffix(t)
	id := seedAgency(t, pool, slug, "pending")
	fake := fetchtest.Failing(&fetch.Error{StatusCode: http.StatusNotFound, Err: errors.New("home page is gone")})
	h := start(t, pool, jobs.Config{Screener: fake})

	testdb.WaitForJobState(t, pool, h.screen(id, nil), "cancelled")
	sc, ok := screening(t, s, id)
	if !ok {
		t.Fatal("no screening stored")
	}
	if sc.Status != store.ScreeningFailed || sc.Verdict != "" || len(sc.Evidence) != 0 || !strings.Contains(sc.Failure, "home page is gone") {
		t.Errorf("screening = %+v", sc)
	}
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}
}

func TestScreeningRetriesBeforeStoringAFailure(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	id := seedAgency(t, pool, "screen-retry-"+randomSuffix(t), "pending")
	fake := fetchtest.Failing(&fetch.Error{StatusCode: http.StatusServiceUnavailable, Retryable: true, Err: errors.New("unavailable")})
	h := start(t, pool, jobs.Config{Screener: fake})

	first := h.screen(id, nil)
	h.waitForError(first)
	if _, ok := screening(t, s, id); ok {
		t.Error("a failure was stored before the last attempt")
	}
	if _, err := h.client.JobCancel(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, first, "cancelled")

	last := h.screen(id, &river.InsertOpts{MaxAttempts: 1})
	testdb.WaitForJobState(t, pool, last, "discarded")
	sc, ok := screening(t, s, id)
	if !ok || sc.Status != store.ScreeningFailed || !strings.Contains(sc.Failure, "unavailable") {
		t.Errorf("screening = %+v (stored %v), want a failed report", sc, ok)
	}
}

func TestScreeningRefusesNonPublicAddresses(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	var calls atomic.Int32
	final := "http://169.254.169.254/latest/meta-data/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		body, _ := json.Marshal(map[string]any{"success": true, "data": map[string]any{
			"rawHtml": `<a href="/meetings">Meetings</a>`, "metadata": map[string]any{"statusCode": 200, "url": final},
		}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	firecrawl := fetch.NewFirecrawl(config.Fetch{
		FirecrawlAPIKey: "fc-test", FirecrawlBaseURL: srv.URL, FirecrawlTimeout: 5 * time.Second, MaxBytes: 1 << 20,
	}, fetch.WithLogger(slog.New(slog.DiscardHandler)))
	h := start(t, pool, jobs.Config{Screener: firecrawl})

	literal := seedAgency(t, pool, "screen-literal-"+randomSuffix(t), "pending")
	if _, err := pool.Exec(t.Context(), `UPDATE agencies SET website = 'http://127.0.0.1:5432/' WHERE id = $1`, literal); err != nil {
		t.Fatal(err)
	}
	testdb.WaitForJobState(t, pool, h.screen(literal, nil), "cancelled")
	if calls.Load() != 0 {
		t.Errorf("firecrawl called %d times for a loopback literal", calls.Load())
	}
	sc, ok := screening(t, s, literal)
	if !ok || sc.Status != store.ScreeningFailed || !strings.Contains(sc.Failure, "not a public internet address") {
		t.Errorf("literal: screening = %+v (stored %v)", sc, ok)
	}

	redirected := seedAgency(t, pool, "screen-redirect-"+randomSuffix(t), "pending")
	testdb.WaitForJobState(t, pool, h.screen(redirected, nil), "cancelled")
	sc, ok = screening(t, s, redirected)
	if !ok || sc.Status != store.ScreeningFailed || !strings.Contains(sc.Failure, "169.254.169.254") {
		t.Errorf("redirect: screening = %+v (stored %v)", sc, ok)
	}
	if calls.Load() != 1 {
		t.Errorf("firecrawl called %d times, want 1 (no page followed after the refused redirect)", calls.Load())
	}
}

func TestScreeningSkipsAgenciesThatAreNotPendingOrWhenOff(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	fake := agencySite(nil)
	h := start(t, pool, jobs.Config{Screener: fake})
	live := seedAgency(t, pool, "screen-live-"+randomSuffix(t), "live")
	testdb.WaitForJobState(t, pool, h.screen(live, nil), "completed")
	if _, ok := screening(t, s, live); ok {
		t.Error("a live agency was screened")
	}
	testdb.WaitForJobState(t, pool, h.screen(uuid.New(), nil), "cancelled")

	off := start(t, pool, jobs.Config{Screener: fake, Enabled: func(context.Context, string) bool { return false }})
	pending := seedAgency(t, pool, "screen-off-"+randomSuffix(t), "pending")
	testdb.WaitForJobState(t, pool, off.screen(pending, nil), "completed")
	if _, ok := screening(t, s, pending); ok {
		t.Error("screened with the flag off")
	}
	if n := len(fake.Calls()); n != 0 {
		t.Errorf("fetches = %d, want 0", n)
	}
}

func TestScreeningWithoutAFetcherStoresAFailure(t *testing.T) {
	pool := testdb.Pool(t)
	s := store.New(pool)
	h := start(t, pool, jobs.Config{})
	id := seedAgency(t, pool, "screen-nofetch-"+randomSuffix(t), "pending")
	testdb.WaitForJobState(t, pool, h.screen(id, nil), "cancelled")
	sc, ok := screening(t, s, id)
	if !ok || sc.Status != store.ScreeningFailed || !strings.Contains(sc.Failure, "no fetcher") {
		t.Errorf("screening = %+v (stored %v)", sc, ok)
	}
}
