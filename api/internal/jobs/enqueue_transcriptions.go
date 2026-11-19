package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

const (
	transcriptSweepInterval  = time.Hour
	transcriptSweepLimit     = 10
	transcribeFailureBackoff = summaryFailureBackoff
)

type EnqueueTranscriptionsArgs struct {
	Slot time.Time `json:"slot"`
}

func (EnqueueTranscriptionsArgs) Kind() string { return "enqueue_transcriptions" }

func (EnqueueTranscriptionsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type transcriptSweepStore interface {
	ListUntranscribedRecordings(ctx context.Context, p store.UntranscribedParams) ([]uuid.UUID, error)
}

type EnqueueTranscriptionsWorker struct {
	river.WorkerDefaults[EnqueueTranscriptionsArgs]

	logger      *slog.Logger
	store       transcriptSweepStore
	transcriber transcribe.Client
	enabled     func(context.Context, string) bool
	queue       string
}

func (w *EnqueueTranscriptionsWorker) Work(ctx context.Context, job *river.Job[EnqueueTranscriptionsArgs]) error {
	logger := w.logger.With("job_id", job.ID, "slot", job.Args.Slot)
	if !w.enabled(ctx, FlagTranscribe) {
		logger.InfoContext(ctx, "transcriptions not swept: flag off", "flag", FlagTranscribe)
		return nil
	}
	if w.transcriber == nil {
		logger.InfoContext(ctx, "transcriptions not swept: no transcription client configured")
		return nil
	}
	ids, err := w.store.ListUntranscribedRecordings(ctx, store.UntranscribedParams{
		FailedKind:   TranscribeAudioArgs{}.Kind(),
		FailedWithin: transcribeFailureBackoff,
		Limit:        transcriptSweepLimit,
	})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		logger.InfoContext(ctx, "transcriptions swept: none missing")
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{
			Args:       TranscribeAudioArgs{RecordingID: id},
			InsertOpts: &river.InsertOpts{Queue: w.queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return fmt.Errorf("enqueue transcriptions: %w", err)
	}
	skipped := 0
	for _, r := range results {
		if r.UniqueSkippedAsDuplicate {
			skipped++
		}
	}
	logger.InfoContext(ctx, "transcriptions swept", "missing", len(ids), "inserted", len(results)-skipped, "duplicates", skipped,
		"limit", transcriptSweepLimit)
	return nil
}

func periodicTranscriptSweep(clock sources.Clock, queue string) river.PeriodicJobConstructor {
	return func() (river.JobArgs, *river.InsertOpts) {
		return EnqueueTranscriptionsArgs{Slot: clock.Now().UTC().Truncate(transcriptSweepInterval)}, &river.InsertOpts{Queue: queue}
	}
}
