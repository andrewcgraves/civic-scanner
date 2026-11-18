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

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const (
	FlagAgendaChanges   = "ingest-agenda-changes"
	changeSweepInterval = time.Hour
	changeSweepLimit    = 50
	changeSweepGrace    = 10 * time.Minute
)

type DescribeAgendaChangeArgs struct {
	AgendaVersionID uuid.UUID `json:"agenda_version_id"`
}

func (DescribeAgendaChangeArgs) Kind() string { return "describe_agenda_change" }

func (DescribeAgendaChangeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
			rivertype.JobStateRunning, rivertype.JobStateScheduled,
		}},
	}
}

func enqueueDescribeAgendaChange(ctx context.Context, tx pgx.Tx, queue string, v store.AgendaVersion) error {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err == nil {
		_, err = client.InsertTx(ctx, tx, DescribeAgendaChangeArgs{AgendaVersionID: v.ID}, &river.InsertOpts{Queue: queue})
	}
	if err != nil {
		return fmt.Errorf("enqueue describe agenda change: %w", err)
	}
	return nil
}

type changeStore interface {
	GetAgendaVersion(ctx context.Context, versionID uuid.UUID) (store.AgendaVersion, error)
	PreviousAgendaVersion(ctx context.Context, v store.AgendaVersion) (store.AgendaVersion, error)
	RecordAgendaChange(ctx context.Context, versionID uuid.UUID, note string) (store.AgendaVersion, bool, error)
}

type DescribeAgendaChangeWorker struct {
	river.WorkerDefaults[DescribeAgendaChangeArgs]

	logger     *slog.Logger
	store      changeStore
	llm        llm.Client
	tier       llm.Tier
	llmTimeout time.Duration
	enabled    func(context.Context, string) bool
}

func (w *DescribeAgendaChangeWorker) Timeout(*river.Job[DescribeAgendaChangeArgs]) time.Duration {
	return w.llmTimeout + summarizeTimeoutMargin
}

func (w *DescribeAgendaChangeWorker) Work(ctx context.Context, job *river.Job[DescribeAgendaChangeArgs]) error {
	id := job.Args.AgendaVersionID
	logger := w.logger.With("agenda_version_id", id, "job_id", job.ID)

	v, err := w.store.GetAgendaVersion(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger = logger.With("meeting_id", v.MeetingID, "version", v.Version)
	if !v.ChangeEventPending {
		logger.InfoContext(ctx, "agenda change skipped: already recorded")
		return nil
	}

	previous, err := w.store.PreviousAgendaVersion(ctx, v)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	diff := agendadiff.Compare(previous.Text, v.Text)
	note, source, err := w.note(ctx, logger, job, diff)
	if err != nil {
		return err
	}

	_, recorded, err := w.store.RecordAgendaChange(ctx, v.ID, note)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agenda change recorded", "source", source, "recorded", recorded,
		"changes", len(diff.Changes), "coarse", diff.Coarse, "note_bytes", len(note))
	return nil
}

func (w *DescribeAgendaChangeWorker) note(ctx context.Context, logger *slog.Logger, job *river.Job[DescribeAgendaChangeArgs], diff agendadiff.Diff) (string, string, error) {
	fallback := summarize.FallbackChangeNote(diff)
	switch {
	case diff.Equivalent():
		return fallback, "no_differences", nil
	case !w.enabled(ctx, FlagAgendaChanges):
		return fallback, "fallback_flag_off", nil
	case w.llm == nil:
		return fallback, "fallback_no_llm", nil
	}

	result, err := summarize.AgendaChange(ctx, w.llm, w.tier, diff)
	attrs := []any{
		"tier", w.tier, "model", result.Response.Model, "finish_reason", result.Response.FinishReason,
		"input_tokens", result.Response.Usage.InputTokens, "output_tokens", result.Response.Usage.OutputTokens,
		"input_bytes", result.Input.Bytes, "input_truncated", result.Input.Truncated, "attempt", job.Attempt,
	}
	if err == nil {
		logger.InfoContext(ctx, "agenda change described", attrs...)
		return result.Note, "model", nil
	}
	if llm.IsRetryable(err) && job.Attempt < job.MaxAttempts {
		logger.WarnContext(ctx, "agenda change not described, will retry", append(attrs, "error", err)...)
		return "", "", fmt.Errorf("describe agenda change: %w", err)
	}
	logger.WarnContext(ctx, "agenda change not described by the model, using the word diff", append(attrs, "error", err)...)
	return fallback, "fallback_model_failed", nil
}

type changeSweepStore interface {
	ListPendingAgendaChanges(ctx context.Context, createdBefore time.Time, limit int) ([]uuid.UUID, error)
}

type EnqueueAgendaChangesArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueAgendaChangesArgs) Kind() string { return "enqueue_agenda_changes" }

func (EnqueueAgendaChangesArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type EnqueueAgendaChangesWorker struct {
	river.WorkerDefaults[EnqueueAgendaChangesArgs]

	logger *slog.Logger
	store  changeSweepStore
	clock  sources.Clock
	queue  string
}

func (w *EnqueueAgendaChangesWorker) Work(ctx context.Context, job *river.Job[EnqueueAgendaChangesArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	ids, err := w.store.ListPendingAgendaChanges(ctx, w.clock.Now().Add(-changeSweepGrace), changeSweepLimit)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		logger.InfoContext(ctx, "agenda changes swept: none pending")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{
			Args:       DescribeAgendaChangeArgs{AgendaVersionID: id},
			InsertOpts: &river.InsertOpts{Queue: w.queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue agenda changes: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "agenda changes swept", "pending", len(ids), "inserted", len(results)-skipped, "duplicates", skipped,
		"limit", changeSweepLimit)
	return nil
}

func periodicChangeSweep(clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueAgendaChangesArgs{Slot: clock.Now().UTC().Truncate(changeSweepInterval)}, &river.InsertOpts{Queue: queue}
	}
}
