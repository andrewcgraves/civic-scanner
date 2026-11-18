package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	summarySweepInterval  = time.Hour
	summarySweepLimit     = 50
	summaryFailureBackoff = 7 * 24 * time.Hour
)

type EnqueueAgendaSummariesArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueAgendaSummariesArgs) Kind() string { return "enqueue_agenda_summaries" }

func (EnqueueAgendaSummariesArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type summarySweepStore interface {
	ListUnsummarizedAgendaVersions(ctx context.Context, p store.UnsummarizedParams) ([]uuid.UUID, error)
}

type EnqueueAgendaSummariesWorker struct {
	river.WorkerDefaults[EnqueueAgendaSummariesArgs]

	logger  *slog.Logger
	store   summarySweepStore
	clock   sources.Clock
	llm     llm.Client
	enabled func(context.Context, string) bool
	queue   string
}

func (w *EnqueueAgendaSummariesWorker) Work(ctx context.Context, job *river.Job[EnqueueAgendaSummariesArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.enabled(ctx, FlagSummarizeAgendas) {
		logger.InfoContext(ctx, "agenda summaries not swept: flag off", "flag", FlagSummarizeAgendas)
		return nil
	}
	if w.llm == nil {
		logger.InfoContext(ctx, "agenda summaries not swept: no LLM configured")
		return nil
	}
	window := checkWindow(w.clock.Now())
	ids, err := w.store.ListUnsummarizedAgendaVersions(ctx, store.UnsummarizedParams{
		From:         window.From,
		To:           window.To,
		FailedKind:   SummarizeAgendaArgs{}.Kind(),
		FailedWithin: summaryFailureBackoff,
		Limit:        summarySweepLimit,
	})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		logger.InfoContext(ctx, "agenda summaries swept: none missing")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{
			Args:       SummarizeAgendaArgs{AgendaVersionID: id},
			InsertOpts: &river.InsertOpts{Queue: w.queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue agenda summaries: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "agenda summaries swept", "missing", len(ids), "inserted", len(results)-skipped, "duplicates", skipped,
		"limit", summarySweepLimit)
	return nil
}

func periodicSummarySweep(clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueAgendaSummariesArgs{Slot: clock.Now().UTC().Truncate(summarySweepInterval)}, &river.InsertOpts{Queue: queue}
	}
}
