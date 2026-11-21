package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/screen"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	FlagScreenAgencies     = "agency-screening"
	screenAgencyTimeout    = 10 * time.Minute
	screenAgencyAttempts   = 3
	screenFailureWriteTime = 30 * time.Second
)

type ScreenAgencyArgs struct {
	AgencyID uuid.UUID `json:"agency_id"`
}

func (ScreenAgencyArgs) Kind() string { return "screen_agency" }

func (ScreenAgencyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: screenAgencyAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
			rivertype.JobStateRunning, rivertype.JobStateScheduled,
		}},
	}
}

type Inserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

type EnqueueScreenAgency struct {
	Client  Inserter
	Queue   string
	Enabled func(ctx context.Context, flag string) bool
}

func (e EnqueueScreenAgency) AgencySubmitted(ctx context.Context, tx pgx.Tx, sub store.AgencySubmission) error {
	if e.Enabled == nil || !e.Enabled(ctx, FlagScreenAgencies) {
		return nil
	}
	if e.Client == nil {
		return errors.New("enqueue screen agency: no job client")
	}
	queue := e.Queue
	if queue == "" {
		queue = river.QueueDefault
	}
	if _, err := e.Client.InsertTx(ctx, tx, ScreenAgencyArgs{AgencyID: sub.AgencyID}, &river.InsertOpts{Queue: queue}); err != nil {
		return fmt.Errorf("enqueue screen agency: %w", err)
	}
	return nil
}

type ScreenAgencyWorker struct {
	river.WorkerDefaults[ScreenAgencyArgs]

	logger   *slog.Logger
	store    *store.Store
	screener screen.Screener
	clock    sources.Clock
	enabled  func(context.Context, string) bool
}

func (w *ScreenAgencyWorker) Timeout(*river.Job[ScreenAgencyArgs]) time.Duration {
	return screenAgencyTimeout
}

func (w *ScreenAgencyWorker) Work(ctx context.Context, job *river.Job[ScreenAgencyArgs]) error {
	id := job.Args.AgencyID
	logger := w.logger.With("agency_id", id, "job_id", job.ID, "attempt", job.Attempt)
	if !w.enabled(ctx, FlagScreenAgencies) {
		logger.InfoContext(ctx, "agency screening skipped: flag off", "flag", FlagScreenAgencies)
		return nil
	}
	target, err := w.store.GetScreeningTarget(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	if target.Status != "pending" {
		logger.InfoContext(ctx, "agency screening skipped: no longer pending", "status", target.Status)
		return nil
	}

	report, err := w.screener.Screen(ctx, target.Website)
	if err != nil {
		return w.failed(ctx, logger, job, err)
	}
	result := store.AgencyScreening{
		AgencyID:   id,
		Status:     store.ScreeningComplete,
		Verdict:    string(report.Verdict),
		ScreenedAt: w.clock.Now().UTC(),
	}
	for _, e := range report.Evidence {
		result.Evidence = append(result.Evidence, store.ScreeningEvidence{Supports: string(e.Supports), Signal: e.Signal, URL: e.URL, Page: e.Page})
	}
	for _, p := range report.Pages {
		result.Pages = append(result.Pages, store.ScreeningPage{URL: p.URL, Error: p.Error})
	}
	if err := w.store.SaveAgencyScreening(ctx, result); errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	} else if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agency screened", "verdict", report.Verdict, "evidence", len(report.Evidence), "pages", len(report.Pages))
	return nil
}

func (w *ScreenAgencyWorker) failed(ctx context.Context, logger *slog.Logger, job *river.Job[ScreenAgencyArgs], err error) error {
	retryable := screen.Retryable(err)
	err = fmt.Errorf("screen agency: %w", err)
	logger.InfoContext(ctx, "agency not screened", "error", err, "retryable", retryable)
	if retryable && job.Attempt < job.MaxAttempts {
		return err
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), screenFailureWriteTime)
	defer cancel()
	failure := store.AgencyScreening{
		AgencyID:   job.Args.AgencyID,
		Status:     store.ScreeningFailed,
		Failure:    err.Error(),
		ScreenedAt: w.clock.Now().UTC(),
	}
	if saveErr := w.store.SaveAgencyScreening(writeCtx, failure); saveErr != nil {
		logger.WarnContext(ctx, "agency screening failure not recorded", "error", saveErr)
	} else {
		logger.WarnContext(ctx, "agency screening failed", "error", err)
	}
	if retryable {
		return err
	}
	return river.JobCancel(err)
}
