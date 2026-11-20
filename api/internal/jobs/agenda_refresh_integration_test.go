//go:build integration

package jobs_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type agendaSite struct {
	mu     sync.Mutex
	bodies map[string]string
	fails  map[string]int
	hits   map[string]int
}

func newAgendaSite() *agendaSite {
	return &agendaSite{bodies: map[string]string{}, fails: map[string]int{}, hits: map[string]int{}}
}

func (s *agendaSite) serve(url string, lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies[url] = plainAgenda(lines)
}

func (s *agendaSite) failNext(url string, times int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails[url] = times
}

func (s *agendaSite) Fetch(_ context.Context, req fetch.Request) (fetch.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits[req.URL]++
	if s.fails[req.URL] > 0 {
		s.fails[req.URL]--
		return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("agenda not found")}
	}
	body, ok := s.bodies[req.URL]
	if !ok {
		return fetch.Response{}, &fetch.Error{StatusCode: 404, Err: errors.New("unknown agenda")}
	}
	return fetch.Response{Status: 200, ContentType: "text/plain", Body: []byte(body), FinalURL: req.URL}, nil
}

func (s *agendaSite) requests() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.hits)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) records(msg string) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

type refreshRun struct {
	t      *testing.T
	pool   *pgxpool.Pool
	h      *harness
	slug   string
	agency uuid.UUID
	clock  *sourcestest.Clock
	fake   *sourcestest.Adapter
	site   *agendaSite
	logs   *syncBuffer
}

func newRefreshRun(t *testing.T, cfg jobs.Config) (*refreshRun, *store.Store) {
	t.Helper()
	pool := testdb.Pool(t)
	slug := "jobs-test-refresh-" + randomSuffix(t)
	r := &refreshRun{
		t:      t,
		pool:   pool,
		slug:   slug,
		agency: seedAgency(t, pool, slug, "live"),
		clock:  sourcestest.NewClock(fixedNow),
		fake:   sourcestest.Returning(),
		site:   newAgendaSite(),
		logs:   &syncBuffer{},
	}
	cfg.Registry = registry(map[string]sources.Adapter{slug: r.fake})
	cfg.Clock = r.clock
	cfg.Fetcher = r.site
	if cfg.AgendaFollowUp == nil {
		cfg.AgendaFollowUp = &recordingAgendaFollowUp{}
	}
	r.h = startLogging(t, pool, cfg, slog.New(slog.NewJSONHandler(r.logs, nil)))
	return r, store.New(pool)
}

type refreshDay struct {
	enqueued []string
	fetched  map[string]int
	log      map[string]any
}

func (r *refreshRun) day(n int, manual bool, meetings ...sources.Meeting) refreshDay {
	r.t.Helper()
	r.clock.Set(fixedNow.AddDate(0, 0, n))
	r.fake.Set(meetings...)
	r.forgetAgendaJobs()
	before := r.site.requests()
	logged := len(r.logs.records("agency checked"))

	if manual {
		checks := r.h.manualCheck(r.slug)
		if len(checks) != 1 || checks[0].Duplicate {
			r.t.Fatalf("day %d: manual check = %+v", n, checks)
		}
		testdb.WaitForJobState(r.t, r.pool, checks[0].JobID, "completed")
	} else {
		res, err := r.h.client.Insert(r.t.Context(), jobs.CheckAgencyArgs{AgencySlug: r.slug}, &river.InsertOpts{Queue: r.h.queue})
		if err != nil || res.UniqueSkippedAsDuplicate {
			r.t.Fatalf("day %d: insert check = %+v, %v", n, res, err)
		}
		testdb.WaitForJobState(r.t, r.pool, res.Job.ID, "completed")
	}
	r.waitForAgendaJobs()

	var d refreshDay
	for _, j := range r.h.processAgendaJobs() {
		var args jobs.ProcessAgendaArgs
		if err := json.Unmarshal(j.EncodedArgs, &args); err != nil {
			r.t.Fatal(err)
		}
		if args.Manual != manual {
			r.t.Errorf("day %d: process agenda job for %s has manual %v, want %v", n, args.AgendaURL, args.Manual, manual)
		}
		d.enqueued = append(d.enqueued, args.AgendaURL)
	}
	slices.Sort(d.enqueued)
	d.fetched = map[string]int{}
	for url, hits := range r.site.requests() {
		if hits > before[url] {
			d.fetched[url] = hits - before[url]
		}
	}
	records := r.logs.records("agency checked")
	if len(records) != logged+1 {
		r.t.Fatalf("day %d: %d agency checked lines, want one more than %d", n, len(records), logged)
	}
	d.log = records[len(records)-1]
	return d
}

func (r *refreshRun) forgetAgendaJobs() {
	r.t.Helper()
	if _, err := r.pool.Exec(r.t.Context(),
		`DELETE FROM river_job WHERE queue = $1 AND kind = ANY($2) AND state IN ('completed', 'cancelled', 'discarded')`,
		r.h.queue, []string{jobs.ProcessAgendaArgs{}.Kind(), jobs.CheckAgencyArgs{}.Kind()}); err != nil {
		r.t.Fatalf("forget agenda jobs: %v", err)
	}
}

func (r *refreshRun) waitForAgendaJobs() {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		if err := r.pool.QueryRow(r.t.Context(),
			`SELECT count(*) FROM river_job WHERE queue = $1 AND kind = $2 AND state NOT IN ('completed', 'cancelled', 'discarded')`,
			r.h.queue, jobs.ProcessAgendaArgs{}.Kind()).Scan(&waiting); err != nil {
			r.t.Fatalf("count agenda jobs: %v", err)
		}
		if waiting == 0 {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("%d agenda jobs still waiting after 30s", waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d refreshDay) expect(t *testing.T, label string, enqueued []string, skipped int, cutoff string) {
	t.Helper()
	want := slices.Sorted(slices.Values(enqueued))
	if want == nil {
		want = []string{}
	}
	got := d.enqueued
	if got == nil {
		got = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s: enqueued %v, want %v", label, got, want)
	}
	wantFetched := map[string]int{}
	for _, url := range enqueued {
		wantFetched[url] = 1
	}
	if !maps.Equal(d.fetched, wantFetched) {
		t.Errorf("%s: fetched %v, want %v", label, d.fetched, wantFetched)
	}
	if d.log["agendas_enqueued"] != float64(len(enqueued)) || d.log["agendas_skipped_cutoff"] != float64(skipped) ||
		d.log["agenda_cutoff"] != cutoff || d.log["agenda_refresh_grace"] != "48h0m0s" {
		t.Errorf("%s: agency checked log = %v, want %d enqueued, %d skipped, cutoff %s, grace 48h0m0s", label, d.log, len(enqueued), skipped, cutoff)
	}
}

func pastMeeting(id string, startsAt time.Time, status sources.Status) sources.Meeting {
	m := boardMeeting(id, startsAt, status)
	m.AgendaURL = "https://agency.test/" + id + "/agenda.pdf"
	return m
}

func TestDailyCheckDownloadsEachPastAgendaOnceForANewlyConnectedAgency(t *testing.T) {
	r, st := newRefreshRun(t, jobs.Config{})
	upcoming := pastMeeting("upcoming", fixedNow.AddDate(0, 0, 5), sources.StatusScheduled)
	recent := pastMeeting("recent", fixedNow.Add(-36*time.Hour), sources.StatusScheduled)
	week := pastMeeting("week", fixedNow.AddDate(0, 0, -7), sources.StatusHeld)
	cancelled := pastMeeting("cancelled", fixedNow.AddDate(0, 0, -15), sources.StatusCancelled)
	month := pastMeeting("month", fixedNow.AddDate(0, 0, -29), sources.StatusHeld)
	noAgenda := boardMeeting("no-agenda", fixedNow.AddDate(0, 0, -20), sources.StatusHeld)
	all := []sources.Meeting{upcoming, recent, week, cancelled, month, noAgenda}
	for _, m := range all {
		if m.AgendaURL != "" {
			r.site.serve(m.AgendaURL, append([]string{m.Title}, agendaV1...))
		}
	}

	r.day(0, false, all...).expect(t, "first check",
		[]string{upcoming.AgendaURL, recent.AgendaURL, week.AgendaURL, cancelled.AgendaURL, month.AgendaURL}, 0, "applied")

	recent.Status = sources.StatusHeld
	week.Title = "Board of Directors (corrected title)"
	r.day(1, false, upcoming, recent, week, cancelled, month, noAgenda).expect(t, "second check",
		[]string{upcoming.AgendaURL}, 4, "applied")
	meetings := meetingsOf(t, r.pool, r.agency)
	if meetings["recent"].Status != "held" || meetings["week"].Title != week.Title {
		t.Errorf("past meetings were not updated while their agendas were skipped: %+v, %+v", meetings["recent"], meetings["week"])
	}

	for n := 2; n <= 4; n++ {
		r.day(n, false, all...).expect(t, "later check", []string{upcoming.AgendaURL}, 4, "applied")
	}

	for _, id := range []string{"upcoming", "recent", "week", "cancelled", "month"} {
		versions, err := st.ListAgendaVersions(t.Context(), meetings[id].ID)
		if err != nil || len(versions) != 1 {
			t.Errorf("%s: %d agenda versions (%v), want 1", id, len(versions), err)
		}
	}
	if hits := r.site.requests(); hits[upcoming.AgendaURL] != 5 || hits[recent.AgendaURL] != 1 || hits[week.AgendaURL] != 1 ||
		hits[cancelled.AgendaURL] != 1 || hits[month.AgendaURL] != 1 {
		t.Errorf("agenda requests over five days = %v, want 5 for the upcoming meeting and 1 for each past one", hits)
	}
}

func TestDailyCheckRefreshesUntilExactlyTheGraceAfterTheStart(t *testing.T) {
	r, _ := newRefreshRun(t, jobs.Config{})
	atCutoff := pastMeeting("at-cutoff", fixedNow.Add(-24*time.Hour), sources.StatusHeld)
	justBefore := pastMeeting("just-before", fixedNow.Add(-24*time.Hour+time.Second), sources.StatusHeld)
	for _, m := range []sources.Meeting{atCutoff, justBefore} {
		r.site.serve(m.AgendaURL, agendaV1)
	}

	r.day(0, false, atCutoff, justBefore).expect(t, "a day after the start", []string{atCutoff.AgendaURL, justBefore.AgendaURL}, 0, "applied")
	r.day(1, false, atCutoff, justBefore).expect(t, "48 hours after the start", []string{justBefore.AgendaURL}, 1, "applied")
	r.day(2, false, atCutoff, justBefore).expect(t, "72 hours after the start", nil, 2, "applied")
}

func TestDailyCheckFetchesAPastAgendaAgainOnlyWhenItsURLChanges(t *testing.T) {
	r, st := newRefreshRun(t, jobs.Config{})
	m := pastMeeting("moved", fixedNow.AddDate(0, 0, -10), sources.StatusHeld)
	first := m.AgendaURL
	revised := "https://agency.test/moved/agenda-revised.pdf"
	copied := "https://agency.test/moved/agenda-copy.pdf"
	r.site.serve(first, agendaV1)
	r.site.serve(revised, agendaV2)
	r.site.serve(copied, agendaV1)

	r.day(0, false, m).expect(t, "first sight", []string{first}, 0, "applied")
	r.day(1, false, m).expect(t, "same url", nil, 1, "applied")

	m.AgendaURL = revised
	r.day(2, false, m).expect(t, "a different document", []string{revised}, 0, "applied")
	r.day(3, false, m).expect(t, "the different document again", nil, 1, "applied")

	m.AgendaURL = first
	r.day(4, false, m).expect(t, "back to the first version's url", []string{first}, 0, "applied")
	r.day(5, false, m).expect(t, "the first url again", nil, 1, "applied")

	m.AgendaURL = copied
	r.day(6, false, m).expect(t, "a new url with unchanged text", []string{copied}, 0, "applied")
	r.day(7, false, m).expect(t, "the copy again", nil, 1, "applied")

	meetingID := meetingsOf(t, r.pool, r.agency)["moved"].ID
	versions, err := st.ListAgendaVersions(t.Context(), meetingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 || versions[0].Version != 3 || versions[0].SourceURL != copied || versions[1].SourceURL != revised || versions[2].SourceURL != first {
		t.Errorf("versions = %+v, want 3 (first, revised, then the first url's text again, moved to the copy's url)", versions)
	}
}

func TestDailyCheckKeepsFetchingAPastAgendaUntilOneIsStored(t *testing.T) {
	r, _ := newRefreshRun(t, jobs.Config{})
	m := pastMeeting("late", fixedNow.AddDate(0, 0, -10), sources.StatusHeld)
	r.site.serve(m.AgendaURL, agendaV1)
	r.site.failNext(m.AgendaURL, 2)

	r.day(0, false, m).expect(t, "first failure", []string{m.AgendaURL}, 0, "applied")
	r.day(1, false, m).expect(t, "second failure", []string{m.AgendaURL}, 0, "applied")
	r.day(2, false, m).expect(t, "stored", []string{m.AgendaURL}, 0, "applied")
	r.day(3, false, m).expect(t, "settled", nil, 1, "applied")
}

func TestDailyCheckFollowsARescheduledMeeting(t *testing.T) {
	r, _ := newRefreshRun(t, jobs.Config{})
	m := pastMeeting("moving", fixedNow.AddDate(0, 0, -5), sources.StatusScheduled)
	r.site.serve(m.AgendaURL, agendaV1)

	r.day(0, false, m).expect(t, "first sight", []string{m.AgendaURL}, 0, "applied")
	r.day(1, false, m).expect(t, "past the cutoff", nil, 1, "applied")

	m.StartsAt = fixedNow.AddDate(0, 0, 9)
	r.day(2, false, m).expect(t, "rescheduled to a future date", []string{m.AgendaURL}, 0, "applied")
	r.day(3, false, m).expect(t, "still upcoming", []string{m.AgendaURL}, 0, "applied")

	m.StartsAt = fixedNow.AddDate(0, 0, 4).Add(-time.Hour)
	r.day(4, false, m).expect(t, "moved earlier, an hour into the meeting", []string{m.AgendaURL}, 0, "applied")

	m.StartsAt = fixedNow.AddDate(0, 0, 1)
	r.day(5, false, m).expect(t, "moved earlier, past the cutoff", nil, 1, "applied")

	m.StartsAt = fixedNow.AddDate(0, 0, 5).Add(time.Hour)
	r.day(6, false, m).expect(t, "moved later, inside the grace", []string{m.AgendaURL}, 0, "applied")
	r.day(7, false, m).expect(t, "inside the grace a day on", []string{m.AgendaURL}, 0, "applied")
	r.day(8, false, m).expect(t, "past the cutoff again", nil, 1, "applied")
}

func TestManualCheckBypassesTheAgendaCutoff(t *testing.T) {
	r, _ := newRefreshRun(t, jobs.Config{})
	past := pastMeeting("past", fixedNow.AddDate(0, 0, -20), sources.StatusHeld)
	upcoming := pastMeeting("upcoming", fixedNow.AddDate(0, 0, 3), sources.StatusScheduled)
	r.site.serve(past.AgendaURL, agendaV1)
	r.site.serve(upcoming.AgendaURL, agendaV1)

	r.day(0, false, past, upcoming).expect(t, "first sight", []string{past.AgendaURL, upcoming.AgendaURL}, 0, "applied")
	r.day(1, false, past, upcoming).expect(t, "scheduled", []string{upcoming.AgendaURL}, 1, "applied")
	r.day(1, true, past, upcoming).expect(t, "manual", []string{past.AgendaURL, upcoming.AgendaURL}, 0, "bypassed")
	r.day(2, false, past, upcoming).expect(t, "scheduled after the manual check", []string{upcoming.AgendaURL}, 1, "applied")
}

func TestAgendaRefreshGraceIsConfigurable(t *testing.T) {
	r, _ := newRefreshRun(t, jobs.Config{AgendaGrace: 5 * 24 * time.Hour})
	m := pastMeeting("long-grace", fixedNow.AddDate(0, 0, -3), sources.StatusHeld)
	r.site.serve(m.AgendaURL, agendaV1)

	first := r.day(0, false, m)
	if first.log["agenda_refresh_grace"] != "120h0m0s" || len(first.enqueued) != 1 {
		t.Errorf("day 0 = %+v, want one agenda and a 120h grace in the log", first)
	}
	if d := r.day(1, false, m); len(d.enqueued) != 1 || d.log["agendas_skipped_cutoff"] != float64(0) {
		t.Errorf("day 1, 4 days after the start = %+v, want the agenda refreshed", d)
	}
	if d := r.day(2, false, m); len(d.enqueued) != 0 || d.log["agendas_skipped_cutoff"] != float64(1) {
		t.Errorf("day 2, 5 days after the start = %+v, want the agenda skipped", d)
	}
}
