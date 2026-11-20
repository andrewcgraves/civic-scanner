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

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const (
	FlagSummarizeTranscripts       = "ingest-summarize-transcripts"
	summarizeTranscriptMaxAttempts = 3
)

type SummarizeTranscriptArgs struct {
	TranscriptID uuid.UUID `json:"transcript_id"`
}

func (SummarizeTranscriptArgs) Kind() string { return "summarize_transcript" }

func (SummarizeTranscriptArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: summarizeTranscriptMaxAttempts,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{
			rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
			rivertype.JobStateRunning, rivertype.JobStateScheduled,
		}},
	}
}

type EnqueueSummarizeTranscript struct {
	Queue   string
	Enabled func(ctx context.Context, flag string) bool
	Logger  *slog.Logger
}

func (e EnqueueSummarizeTranscript) TranscriptSaved(ctx context.Context, tx pgx.Tx, t store.Transcript) error {
	if e.Enabled == nil || !e.Enabled(ctx, FlagSummarizeTranscripts) {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err == nil {
		err = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
			_, err := client.InsertTx(ctx, savepoint, SummarizeTranscriptArgs{TranscriptID: t.ID}, &river.InsertOpts{Queue: e.Queue})
			return err
		})
	}
	if err != nil {
		logger := e.Logger
		if logger == nil {
			logger = slog.New(slog.DiscardHandler)
		}
		logger.WarnContext(ctx, "transcript summary not enqueued; the sweep will pick the transcript up",
			"transcript_id", t.ID, "recording_id", t.RecordingID, "error", err)
	}
	return nil
}

type transcriptSummaryStore interface {
	GetTranscriptByID(ctx context.Context, transcriptID uuid.UUID) (store.Transcript, error)
	RecordTranscriptSummary(ctx context.Context, transcriptID uuid.UUID, summarized store.ContentHash, summary string) (store.Transcript, bool, error)
}

type SummarizeTranscriptWorker struct {
	river.WorkerDefaults[SummarizeTranscriptArgs]

	logger     *slog.Logger
	store      transcriptSummaryStore
	llm        llm.Client
	tier       llm.Tier
	llmTimeout time.Duration
	enabled    func(context.Context, string) bool
}

func (w *SummarizeTranscriptWorker) Timeout(*river.Job[SummarizeTranscriptArgs]) time.Duration {
	return w.llmTimeout*summarize.MaxTranscriptCalls + summarizeTimeoutMargin
}

func (w *SummarizeTranscriptWorker) Work(ctx context.Context, job *river.Job[SummarizeTranscriptArgs]) error {
	id := job.Args.TranscriptID
	logger := w.logger.With("transcript_id", id, "job_id", job.ID, "attempt", job.Attempt)
	if !w.enabled(ctx, FlagSummarizeTranscripts) {
		logger.InfoContext(ctx, "transcript summary skipped: flag off", "flag", FlagSummarizeTranscripts)
		return nil
	}
	if w.llm == nil {
		logger.InfoContext(ctx, "transcript summary skipped: no LLM configured")
		return nil
	}
	t, err := w.store.GetTranscriptByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger = logger.With("recording_id", t.RecordingID)
	if t.Summary != nil {
		logger.InfoContext(ctx, "transcript summary skipped: already summarized")
		return nil
	}

	segments := make([]summarize.TranscriptSegment, len(t.Segments))
	for i, s := range t.Segments {
		segments[i] = summarize.TranscriptSegment{Start: s.Start, End: s.End, Text: s.Text}
	}
	result, err := summarize.Transcript(ctx, w.llm, w.tier, segments, t.Text)
	attrs := []any{
		"tier", w.tier, "model", result.Model, "parts", result.Parts, "calls", result.Calls, "no_speech", result.NoSpeech,
		"input_tokens", result.Usage.InputTokens, "output_tokens", result.Usage.OutputTokens,
		"input_bytes", result.Bytes, "text_bytes", len(t.Text), "segments", len(t.Segments), "input_truncated", result.Truncated,
	}
	if errors.Is(err, llm.ErrNotConfigured) {
		logger.WarnContext(ctx, "transcript summary skipped: LLM not configured", attrs...)
		return nil
	}
	if snooze := llmAccountSnoozeFor(ctx, logger.With("tier", w.tier, "calls", result.Calls), err); snooze != nil {
		return snooze
	}
	if err != nil {
		logger.WarnContext(ctx, "transcript not summarized", append(attrs, "error", err)...)
		return transcriptSummaryError(err)
	}
	return w.write(ctx, logger.With(attrs...), t, result.Summary)
}

func (w *SummarizeTranscriptWorker) write(ctx context.Context, logger *slog.Logger, t store.Transcript, summary string) error {
	_, recorded, err := w.store.RecordTranscriptSummary(ctx, t.ID, store.HashContent(t.Text), summary)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "transcript summary written", "written", recorded, "summary_bytes", len(summary))
	return nil
}

func transcriptSummaryError(err error) error {
	err = fmt.Errorf("summarize transcript: %w", err)
	if llm.IsRetryable(err) {
		return err
	}
	return river.JobCancel(err)
}
