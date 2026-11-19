package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const processAgendaTimeout = 5 * time.Minute

var ErrNoFetcher = errors.New("jobs: no fetcher configured")

type ProcessAgendaArgs struct {
	MeetingID uuid.UUID `json:"meeting_id"`
	AgendaURL string    `json:"agenda_url"`
}

func (ProcessAgendaArgs) Kind() string { return "process_agenda" }

func (ProcessAgendaArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour, ByQueue: true},
	}
}

type EnqueueProcessAgenda struct {
	Queue string
}

func (e EnqueueProcessAgenda) MeetingUpserted(ctx context.Context, tx pgx.Tx, m UpsertedMeeting) error {
	if m.AgendaURL == "" {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	_, err = client.InsertTx(ctx, tx, ProcessAgendaArgs{MeetingID: m.Meeting.ID, AgendaURL: m.AgendaURL}, &river.InsertOpts{Queue: e.Queue})
	if err != nil {
		return fmt.Errorf("enqueue process agenda: %w", err)
	}
	return nil
}

type AgendaFollowUp interface {
	AgendaVersionAdded(ctx context.Context, tx pgx.Tx, v store.AgendaVersion) error
}

type NoAgendaFollowUp struct{}

func (NoAgendaFollowUp) AgendaVersionAdded(context.Context, pgx.Tx, store.AgendaVersion) error {
	return nil
}

type TextExtractor interface {
	Text(ctx context.Context, req extract.Request) (extract.Result, error)
}

type ProcessAgendaWorker struct {
	river.WorkerDefaults[ProcessAgendaArgs]

	logger    *slog.Logger
	pool      *pgxpool.Pool
	store     *store.Store
	fetcher   fetch.Fetcher
	fallback  fetch.Fetcher
	extractor TextExtractor
	enabled   func(context.Context, string) bool
	followUp  AgendaFollowUp
	queue     string
}

func (w *ProcessAgendaWorker) Timeout(*river.Job[ProcessAgendaArgs]) time.Duration {
	return processAgendaTimeout
}

func (w *ProcessAgendaWorker) Work(ctx context.Context, job *river.Job[ProcessAgendaArgs]) error {
	args := job.Args
	logger := w.logger.With("meeting_id", args.MeetingID, "agenda_url", args.AgendaURL, "job_id", job.ID)
	if !w.enabled(ctx, FlagCheckAgencies) {
		logger.InfoContext(ctx, "process agenda skipped: flag off", "flag", FlagCheckAgencies)
		return nil
	}
	if w.fetcher == nil {
		return river.JobCancel(ErrNoFetcher)
	}

	text, resp, err := w.read(ctx, logger, w.fetcher, args.AgendaURL)
	if errors.Is(err, extract.ErrBlocked) && w.fallback != nil {
		logger.WarnContext(ctx, "agenda blocked, trying the fallback fetcher", "error", err)
		var fallbackErr error
		text, resp, fallbackErr = w.read(ctx, logger, w.fallback, args.AgendaURL)
		if !errors.Is(fallbackErr, fetch.ErrNotConfigured) {
			err = fallbackErr
		}
	}
	if err != nil {
		return agendaError(args.AgendaURL, err)
	}

	detectChanges := w.enabled(ctx, FlagAgendaChanges)
	hash := store.HashContent(text)
	version, created, err := w.addVersion(ctx, args, text, hash, detectChanges)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agenda processed", "version", version.Version, "created", created,
		"formatting_only", version.RefreshedInPlace, "change_event_pending", created && version.ChangeEventPending,
		"content_type", resp.ContentType, "bytes", len(resp.Body), "characters", len(text))
	return nil
}

func (w *ProcessAgendaWorker) read(ctx context.Context, logger *slog.Logger, fetcher fetch.Fetcher, agendaURL string) (string, fetch.Response, error) {
	resp, err := fetcher.Fetch(ctx, fetch.Request{URL: agendaURL})
	if err != nil {
		return "", resp, fmt.Errorf("fetch agenda: %w", err)
	}
	result, err := w.extractor.Text(ctx, extract.Request{ContentType: resp.ContentType, Body: resp.Body, ExpectPDF: expectsPDF(agendaURL)})
	if err != nil {
		logger.InfoContext(ctx, "agenda text not extracted", "content_type", resp.ContentType, "bytes", len(resp.Body), "error", err)
		return "", resp, fmt.Errorf("extract agenda: %w", err)
	}
	logPDFReport(ctx, logger, result.PDF)
	return result.Text, resp, nil
}

func logPDFReport(ctx context.Context, logger *slog.Logger, report *extract.Report) {
	if report == nil {
		return
	}
	level, msg := slog.LevelInfo, "agenda pdf read"
	if report.Partial() {
		level, msg = slog.LevelWarn, "agenda pdf read in part"
	}
	logger.Log(ctx, level, msg,
		"pages", report.Pages,
		"unreadable_pages", report.UnreadablePages,
		"truncated_pages", report.TruncatedPages,
		"first_error", report.FirstError,
		"decoded_content_bytes", report.DecodedContentBytes,
		"trailing_bytes_cut", report.TrailingBytesCut)
}

func expectsPDF(agendaURL string) bool {
	u, err := url.Parse(agendaURL)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".pdf")
}

func (w *ProcessAgendaWorker) addVersion(ctx context.Context, args ProcessAgendaArgs, text string, hash store.ContentHash, detectChanges bool) (store.AgendaVersion, bool, error) {
	var (
		version store.AgendaVersion
		created bool
	)
	in := store.AgendaVersionInput{
		MeetingID:   args.MeetingID,
		SourceURL:   args.AgendaURL,
		Text:        text,
		ContentHash: hash,
	}
	if detectChanges {
		in.Equivalent = func(latest string) bool { return agendadiff.Equivalent(ctx, latest, text) }
		in.DeferChangeEvent = true
	}
	err := pgx.BeginTxFunc(ctx, w.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		version, created, err = w.store.AddAgendaVersionTx(ctx, tx, in)
		if err != nil || !created {
			return err
		}
		if version.ChangeEventPending {
			if err := enqueueDescribeAgendaChange(ctx, tx, w.queue, version); err != nil {
				return err
			}
		}
		if err := w.followUp.AgendaVersionAdded(ctx, tx, version); err != nil {
			return fmt.Errorf("follow up agenda version %d of meeting %s: %w", version.Version, args.MeetingID, err)
		}
		return nil
	})
	return version, created, err
}

func agendaError(agendaURL string, err error) error {
	err = fmt.Errorf("agenda %s: %w", agendaURL, err)
	if sources.Retryable(err) || errors.Is(err, extract.ErrBlocked) {
		return err
	}
	return river.JobCancel(err)
}
