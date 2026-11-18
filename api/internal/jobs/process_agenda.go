package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

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

type ProcessAgendaWorker struct {
	river.WorkerDefaults[ProcessAgendaArgs]

	logger   *slog.Logger
	pool     *pgxpool.Pool
	store    *store.Store
	fetcher  fetch.Fetcher
	enabled  func(context.Context, string) bool
	followUp AgendaFollowUp
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

	resp, err := w.fetcher.Fetch(ctx, fetch.Request{URL: args.AgendaURL})
	if err != nil {
		return agendaFetchError(args.AgendaURL, err)
	}
	text, err := extract.Text(resp.ContentType, resp.Body)
	if err != nil {
		return river.JobCancel(fmt.Errorf("agenda %s: %w", args.AgendaURL, err))
	}

	version, created, err := w.addVersion(ctx, args, text)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agenda processed", "version", version.Version, "created", created,
		"content_type", resp.ContentType, "bytes", len(resp.Body), "characters", len(text))
	return nil
}

func (w *ProcessAgendaWorker) addVersion(ctx context.Context, args ProcessAgendaArgs, text string) (store.AgendaVersion, bool, error) {
	var (
		version store.AgendaVersion
		created bool
	)
	err := pgx.BeginTxFunc(ctx, w.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		version, created, err = w.store.AddAgendaVersionTx(ctx, tx, store.AgendaVersionInput{
			MeetingID:   args.MeetingID,
			SourceURL:   args.AgendaURL,
			Text:        text,
			ContentHash: store.HashContent(text),
		})
		if err != nil || !created {
			return err
		}
		if err := w.followUp.AgendaVersionAdded(ctx, tx, version); err != nil {
			return fmt.Errorf("follow up agenda version %d of meeting %s: %w", version.Version, args.MeetingID, err)
		}
		return nil
	})
	return version, created, err
}

func agendaFetchError(agendaURL string, err error) error {
	err = fmt.Errorf("fetch agenda %s: %w", agendaURL, err)
	if sources.Retryable(err) {
		return err
	}
	return river.JobCancel(err)
}
