package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch/fetchtest"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

func agendaJob() *river.Job[ProcessAgendaArgs] {
	return &river.Job[ProcessAgendaArgs]{
		JobRow: &rivertype.JobRow{ID: 3},
		Args:   ProcessAgendaArgs{MeetingID: uuid.New(), AgendaURL: "https://agency.test/agenda.pdf"},
	}
}

func agendaWorker(fetcher fetch.Fetcher) *ProcessAgendaWorker {
	return &ProcessAgendaWorker{
		logger:    discard,
		enabled:   flag(true),
		fetcher:   fetcher,
		extractor: &extract.Extractor{PDF: extract.InProcess{}},
	}
}

func isCancel(err error) bool {
	var cancel *river.JobCancelError
	return errors.As(err, &cancel)
}

func TestProcessAgendaArgs(t *testing.T) {
	args := ProcessAgendaArgs{}
	if kind := args.Kind(); kind != "process_agenda" {
		t.Errorf("Kind() = %q, want process_agenda", kind)
	}
	opts := args.InsertOpts()
	if opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByPeriod != 24*time.Hour {
		t.Errorf("InsertOpts() = %+v", opts)
	}
}

func TestProcessAgendaTimeout(t *testing.T) {
	if got := (&ProcessAgendaWorker{}).Timeout(agendaJob()); got != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", got)
	}
}

func TestProcessAgendaWithFlagOffNeverFetches(t *testing.T) {
	fake := fetchtest.Returning("<p>agenda</p>")
	w := &ProcessAgendaWorker{logger: discard, enabled: flag(false), fetcher: fake}
	if err := w.Work(t.Context(), agendaJob()); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("fetched %d times with the flag off", len(calls))
	}
}

func TestProcessAgendaWithoutFetcherIsCancelled(t *testing.T) {
	w := &ProcessAgendaWorker{logger: discard, enabled: flag(true)}
	err := w.Work(t.Context(), agendaJob())
	if !isCancel(err) || !errors.Is(err, ErrNoFetcher) {
		t.Errorf("Work = %v, want a cancelled ErrNoFetcher", err)
	}
}

func TestProcessAgendaFetchesTheAgendaURL(t *testing.T) {
	fake := fetchtest.Failing(&fetch.Error{StatusCode: 404, Err: errors.New("gone")})
	w := agendaWorker(fake)
	job := agendaJob()
	_ = w.Work(t.Context(), job)
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].URL != job.Args.AgendaURL || calls[0].MaxBytes != 0 {
		t.Errorf("fetch calls = %+v", calls)
	}
}

func TestProcessAgendaRetriesOnlyRetryableFetchErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		cancel bool
	}{
		{"server error", &fetch.Error{StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}, false},
		{"job timeout", context.DeadlineExceeded, false},
		{"not found", &fetch.Error{StatusCode: 404, Err: errors.New("not found")}, true},
		{"too large", &fetch.Error{Err: fetch.ErrTooLarge}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := agendaWorker(fetchtest.Failing(tc.err))
			err := w.Work(t.Context(), agendaJob())
			if isCancel(err) != tc.cancel {
				t.Errorf("Work = %v, cancel = %v", err, tc.cancel)
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("Work = %v does not wrap the cause", err)
			}
		})
	}
}

func TestProcessAgendaCancelsWhenNoTextCanBeExtracted(t *testing.T) {
	cases := map[string]struct {
		resp fetch.Response
		want error
	}{
		"word document": {fetch.Response{Status: 200, ContentType: "application/msword", Body: []byte("\xd0\xcf\x11\xe0")}, extract.ErrUnsupported},
		"broken pdf":    {fetch.Response{Status: 200, ContentType: "application/pdf", Body: []byte("%PDF-1.4\ntruncated")}, extract.ErrMalformed},
		"empty page":    {fetch.Response{Status: 200, ContentType: "text/plain", Body: []byte(" \n ")}, extract.ErrNoText},
		"short page":    {fetch.Response{Status: 200, ContentType: "text/plain", Body: []byte("Agenda to follow.")}, extract.ErrNoText},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fetchtest.Fake{Respond: func(fetch.Request) (fetch.Response, error) { return tc.resp, nil }}
			err := agendaWorker(fake).Work(t.Context(), agendaJob())
			if !isCancel(err) || !errors.Is(err, tc.want) {
				t.Errorf("Work = %v, want a cancelled %v", err, tc.want)
			}
		})
	}
}

func responding(contentType, body string) *fetchtest.Fake {
	return &fetchtest.Fake{Respond: func(req fetch.Request) (fetch.Response, error) {
		return fetch.Response{Status: 200, ContentType: contentType, Body: []byte(body), FinalURL: req.URL}, nil
	}}
}

const interstitialPage = "<html><head><title>Pardon Our Interruption</title></head><body><h1>Pardon Our Interruption</h1></body></html>"

func TestProcessAgendaRetriesABlockedPageWithoutAFallback(t *testing.T) {
	cases := map[string]*ProcessAgendaWorker{
		"no fallback":             agendaWorker(responding("text/html", interstitialPage)),
		"fallback not configured": agendaWorker(responding("text/html", interstitialPage)),
		"fallback is blocked too": agendaWorker(responding("text/html", interstitialPage)),
	}
	cases["fallback not configured"].fallback = fetchtest.Failing(&fetch.Error{Err: fetch.ErrNotConfigured})
	cases["fallback is blocked too"].fallback = responding("text/markdown", "# Pardon Our Interruption")
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			err := w.Work(t.Context(), agendaJob())
			if isCancel(err) || !errors.Is(err, extract.ErrBlocked) {
				t.Errorf("Work = %v, want a retryable ErrBlocked", err)
			}
		})
	}
}

func TestProcessAgendaTreatsHTMLForAPDFURLAsBlocked(t *testing.T) {
	page := "<html><body><p>" + strings.Repeat("Our new website has moved this page. ", 10) + "</p></body></html>"
	fallback := fetchtest.Failing(&fetch.Error{StatusCode: 404, Err: errors.New("not found")})
	w := agendaWorker(responding("text/html", page))
	w.fallback = fallback
	err := w.Work(t.Context(), agendaJob())
	var fetchErr *fetch.Error
	if !isCancel(err) || !errors.As(err, &fetchErr) || fetchErr.StatusCode != 404 {
		t.Errorf("Work = %v, want the fallback's permanent 404", err)
	}
	if calls := fallback.Calls(); len(calls) != 1 {
		t.Errorf("fallback calls = %d, want 1", len(calls))
	}
}

func TestProcessAgendaRetriesWhenExtractionIsInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	w := agendaWorker(&fetchtest.Fake{Respond: func(fetch.Request) (fetch.Response, error) {
		cancel()
		return fetch.Response{Status: 200, ContentType: "application/pdf", Body: []byte("%PDF-1.4\n")}, nil
	}})
	w.extractor = &extract.Extractor{PDF: extract.Subprocess{Binary: "/usr/bin/true"}}
	if err := w.Work(ctx, agendaJob()); isCancel(err) || !errors.Is(err, context.Canceled) {
		t.Errorf("Work = %v, want a retryable context.Canceled", err)
	}
}

func TestExpectsPDF(t *testing.T) {
	cases := map[string]bool{
		"https://agency.test/files/Agenda.PDF":                  true,
		"https://agency.test/files/agenda.pdf?version=2":        true,
		"https://metro.legistar.test/View.ashx?M=A&ID=1&GUID=x": false,
		"https://agency.test/agenda.pdf.html":                   false,
		"://bad":                                                false,
	}
	for u, want := range cases {
		if got := expectsPDF(u); got != want {
			t.Errorf("expectsPDF(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestEnqueueProcessAgendaSkipsMeetingsWithoutAgenda(t *testing.T) {
	err := EnqueueProcessAgenda{}.MeetingUpserted(t.Context(), nil, UpsertedMeeting{Meeting: store.Meeting{ID: uuid.New()}})
	if err != nil {
		t.Errorf("MeetingUpserted without agenda = %v", err)
	}
}

func TestEnqueueProcessAgendaNeedsARiverClient(t *testing.T) {
	err := EnqueueProcessAgenda{}.MeetingUpserted(t.Context(), nil, UpsertedMeeting{AgendaURL: "https://agency.test/a.pdf"})
	if err == nil {
		t.Error("MeetingUpserted outside a River job returned no error")
	}
}

func TestNoAgendaFollowUpDoesNothing(t *testing.T) {
	if err := (NoAgendaFollowUp{}).AgendaVersionAdded(t.Context(), nil, store.AgendaVersion{}); err != nil {
		t.Errorf("AgendaVersionAdded = %v", err)
	}
	if err := (NoFollowUp{}).MeetingUpserted(t.Context(), nil, UpsertedMeeting{}); err != nil {
		t.Errorf("MeetingUpserted = %v", err)
	}
}

func TestConfigDefaultsEnqueueAgendasOnTheConfiguredQueue(t *testing.T) {
	cfg := Config{Queue: "ingest"}.withDefaults(discard)
	if got, ok := cfg.FollowUp.(EnqueueProcessAgenda); !ok || got.Queue != "ingest" {
		t.Errorf("FollowUp = %#v, want EnqueueProcessAgenda on ingest", cfg.FollowUp)
	}
	if _, ok := cfg.Extractor.(*extract.Extractor); !ok {
		t.Errorf("Extractor = %#v", cfg.Extractor)
	}
	if got, ok := cfg.AgendaFollowUp.(EnqueueSummarizeAgenda); !ok || got.Queue != "ingest" || got.Logger != discard {
		t.Errorf("AgendaFollowUp = %#v, want EnqueueSummarizeAgenda on ingest", cfg.AgendaFollowUp)
	}
	if cfg.SummaryTier != llm.TierRoutine || cfg.LLMTimeout != 2*time.Minute {
		t.Errorf("SummaryTier = %q, LLMTimeout = %v, want routine and 2m", cfg.SummaryTier, cfg.LLMTimeout)
	}
	if got := (Config{}).withDefaults(discard).FollowUp.(EnqueueProcessAgenda).Queue; got != river.QueueDefault {
		t.Errorf("default queue = %q", got)
	}
}
