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
	transcriptSummarySweepInterval = time.Hour
	transcriptSummarySweepLimit    = 10
	transcriptSummaryLookBack      = 365 * 24 * time.Hour
)

type EnqueueTranscriptSummariesArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueTranscriptSummariesArgs) Kind() string { return "enqueue_transcript_summaries" }

func (EnqueueTranscriptSummariesArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type transcriptSummarySweepStore interface {
	ListUnsummarizedTranscripts(ctx context.Context, p store.UnsummarizedTranscriptParams) ([]uuid.UUID, error)
}

type EnqueueTranscriptSummariesWorker struct {
	river.WorkerDefaults[EnqueueTranscriptSummariesArgs]

	logger  *slog.Logger
	store   transcriptSummarySweepStore
	clock   sources.Clock
	llm     llm.Client
	enabled func(context.Context, string) bool
	queue   string
}

func (w *EnqueueTranscriptSummariesWorker) Work(ctx context.Context, job *river.Job[EnqueueTranscriptSummariesArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.enabled(ctx, FlagSummarizeTranscripts) {
		logger.InfoContext(ctx, "transcript summaries not swept: flag off", "flag", FlagSummarizeTranscripts)
		return nil
	}
	if w.llm == nil {
		logger.InfoContext(ctx, "transcript summaries not swept: no LLM configured")
		return nil
	}
	now := w.clock.Now()
	ids, err := w.store.ListUnsummarizedTranscripts(ctx, store.UnsummarizedTranscriptParams{
		From:         now.UTC().Add(-transcriptSummaryLookBack),
		To:           checkWindow(now).To,
		FailedKind:   SummarizeTranscriptArgs{}.Kind(),
		FailedWithin: summaryFailureBackoff,
		Limit:        transcriptSummarySweepLimit,
	})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		logger.InfoContext(ctx, "transcript summaries swept: none missing")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{
			Args:       SummarizeTranscriptArgs{TranscriptID: id},
			InsertOpts: &river.InsertOpts{Queue: w.queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue transcript summaries: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "transcript summaries swept", "missing", len(ids), "inserted", len(results)-skipped, "duplicates", skipped,
		"limit", transcriptSummarySweepLimit)
	return nil
}

func periodicTranscriptSummarySweep(clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueTranscriptSummariesArgs{Slot: clock.Now().UTC().Truncate(transcriptSummarySweepInterval)}, &river.InsertOpts{Queue: queue}
	}
}
