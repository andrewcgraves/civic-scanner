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
	changeStaleAfter    = 3 * time.Hour
	changeOrderWait     = time.Hour
	changeOrderSnooze   = 5 * time.Minute
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
	EarlierPendingAgendaVersions(ctx context.Context, v store.AgendaVersion) ([]store.AgendaVersion, error)
	AgendaJobFailedBefore(ctx context.Context, kind string, versionID uuid.UUID) (bool, error)
	RecordAgendaChange(ctx context.Context, versionID uuid.UUID, note string, source store.NoteSource) (store.AgendaVersion, bool, error)
}

type DescribeAgendaChangeWorker struct {
	river.WorkerDefaults[DescribeAgendaChangeArgs]

	logger     *slog.Logger
	store      changeStore
	clock      sources.Clock
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

	earlier, err := w.store.EarlierPendingAgendaVersions(ctx, v)
	if err != nil {
		return err
	}
	if len(earlier) > 0 {
		waited := w.clock.Now().Sub(earlier[0].CreatedAt)
		if waited < changeOrderWait {
			logger.InfoContext(ctx, "agenda change waits for an earlier version", "earlier_version", earlier[0].Version, "waited", waited)
			return river.JobSnooze(changeOrderSnooze)
		}
		for _, e := range earlier {
			logger.WarnContext(ctx, "recording a stuck earlier agenda change with the word diff", "earlier_version", e.Version, "waited", w.clock.Now().Sub(e.CreatedAt))
			if err := w.recordWithDiff(ctx, logger, e); err != nil {
				return err
			}
		}
	}

	diff, err := w.diff(ctx, v)
	if err != nil {
		return err
	}
	note, source, reason, err := w.note(ctx, logger, job, v, diff)
	if err != nil {
		return err
	}
	return w.record(ctx, logger, v, diff, note, source, reason)
}

func (w *DescribeAgendaChangeWorker) diff(ctx context.Context, v store.AgendaVersion) (agendadiff.Diff, error) {
	previous, err := w.store.PreviousAgendaVersion(ctx, v)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return agendadiff.Diff{}, err
	}
	return agendadiff.Compare(ctx, previous.Text, v.Text)
}

func (w *DescribeAgendaChangeWorker) recordWithDiff(ctx context.Context, logger *slog.Logger, v store.AgendaVersion) error {
	diff, err := w.diff(ctx, v)
	if err != nil {
		return err
	}
	return w.record(ctx, logger.With("version", v.Version), v, diff, summarize.FallbackChangeNote(diff), store.NoteFromDiff, "stuck_earlier_version")
}

func (w *DescribeAgendaChangeWorker) record(ctx context.Context, logger *slog.Logger, v store.AgendaVersion, diff agendadiff.Diff, note string, source store.NoteSource, reason string) error {
	_, recorded, err := w.store.RecordAgendaChange(ctx, v.ID, note, source)
	switch {
	case errors.Is(err, store.ErrEarlierChangePending):
		logger.InfoContext(ctx, "agenda change waits for an earlier version")
		return river.JobSnooze(changeOrderSnooze)
	case errors.Is(err, store.ErrNotFound):
		return river.JobCancel(err)
	case err != nil:
		return err
	}
	logger.InfoContext(ctx, "agenda change recorded", "note_source", source, "reason", reason, "recorded", recorded,
		"changes", len(diff.Changes), "coarse", diff.Coarse, "too_large", diff.TooLarge, "note_bytes", len(note))
	return nil
}

func (w *DescribeAgendaChangeWorker) note(ctx context.Context, logger *slog.Logger, job *river.Job[DescribeAgendaChangeArgs], v store.AgendaVersion, diff agendadiff.Diff) (string, store.NoteSource, string, error) {
	fallback := summarize.FallbackChangeNote(diff)
	switch {
	case diff.Equivalent():
		return fallback, store.NoteFromDiff, "no_differences", nil
	case diff.TooLarge:
		return fallback, store.NoteFromDiff, "too_large", nil
	case !w.enabled(ctx, FlagAgendaChanges):
		return fallback, store.NoteFromDiff, "flag_off", nil
	case w.llm == nil:
		return fallback, store.NoteFromDiff, "no_llm", nil
	case job.Attempt > 1:
		return fallback, store.NoteFromDiff, "retry", nil
	}
	failed, err := w.store.AgendaJobFailedBefore(ctx, DescribeAgendaChangeArgs{}.Kind(), v.ID)
	if err != nil {
		return "", "", "", err
	}
	if failed {
		return fallback, store.NoteFromDiff, "earlier_job_failed", nil
	}

	result, err := summarize.AgendaChange(ctx, w.llm, w.tier, diff)
	attrs := []any{
		"tier", w.tier, "model", result.Response.Model, "finish_reason", result.Response.FinishReason,
		"input_tokens", result.Response.Usage.InputTokens, "output_tokens", result.Response.Usage.OutputTokens,
		"input_bytes", result.Input.Bytes, "input_truncated", result.Input.Truncated,
	}
	switch {
	case isLLMPaused(err):
		logger.InfoContext(ctx, "agenda change not sent to the model after an account or configuration failure, using the word diff")
		return fallback, store.NoteFromDiff, "llm_paused", nil
	case logLLMAccountFailure(ctx, logger.With("tier", w.tier), err, "note_source", store.NoteFromDiff):
		return fallback, store.NoteFromDiff, "llm_account_failure", nil
	case err != nil:
		logger.WarnContext(ctx, "agenda change not described by the model, using the word diff", append(attrs, "error", err)...)
		return fallback, store.NoteFromDiff, "model_failed", nil
	}
	logger.InfoContext(ctx, "agenda change described", attrs...)
	return result.Note, store.NoteFromModel, "model", nil
}

type changeSweepStore interface {
	ListPendingAgendaChanges(ctx context.Context, createdBefore time.Time, limit int) ([]store.PendingAgendaChange, error)
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
	now := w.clock.Now()
	pending, err := w.store.ListPendingAgendaChanges(ctx, now.Add(-changeSweepGrace), changeSweepLimit)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if age := now.Sub(p.CreatedAt); age > changeStaleAfter {
			logger.WarnContext(ctx, "agenda change still pending its event", "agenda_version_id", p.ID, "pending_for", age)
		}
	}
	ids := make([]uuid.UUID, 0, len(pending))
	for _, p := range pending {
		ids = append(ids, p.ID)
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
