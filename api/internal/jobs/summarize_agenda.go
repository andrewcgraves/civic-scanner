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

	"github.com/andrewcgraves/civic-scanner/api/internal/llm"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/summarize"
)

const (
	FlagSummarizeAgendas   = "ingest-summarize-agendas"
	defaultLLMTimeout      = 2 * time.Minute
	summarizeTimeoutMargin = time.Minute
)

var ErrNoLLM = errors.New("jobs: no LLM client configured")

type SummarizeAgendaArgs struct {
	AgendaVersionID uuid.UUID `json:"agenda_version_id"`
}

func (SummarizeAgendaArgs) Kind() string { return "summarize_agenda" }

func (SummarizeAgendaArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByQueue: true},
	}
}

type EnqueueSummarizeAgenda struct {
	Queue  string
	Logger *slog.Logger
}

func (e EnqueueSummarizeAgenda) AgendaVersionAdded(ctx context.Context, tx pgx.Tx, v store.AgendaVersion) error {
	logger := e.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err == nil {
		err = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
			_, err := client.InsertTx(ctx, savepoint, SummarizeAgendaArgs{AgendaVersionID: v.ID}, &river.InsertOpts{Queue: e.Queue})
			return err
		})
	}
	if err != nil {
		logger.WarnContext(ctx, "agenda summary not enqueued; the version is kept without one",
			"agenda_version_id", v.ID, "meeting_id", v.MeetingID, "version", v.Version, "error", err)
	}
	return nil
}

type summaryStore interface {
	GetAgendaVersion(ctx context.Context, versionID uuid.UUID) (store.AgendaVersion, error)
	LatestAgendaVersion(ctx context.Context, meetingID uuid.UUID) (store.AgendaVersion, error)
	SetAgendaSummaryIfUnset(ctx context.Context, versionID uuid.UUID, summary string) (store.AgendaVersion, bool, error)
}

type SummarizeAgendaWorker struct {
	river.WorkerDefaults[SummarizeAgendaArgs]

	logger     *slog.Logger
	store      summaryStore
	llm        llm.Client
	tier       llm.Tier
	llmTimeout time.Duration
	enabled    func(context.Context, string) bool
}

func (w *SummarizeAgendaWorker) Timeout(*river.Job[SummarizeAgendaArgs]) time.Duration {
	return w.llmTimeout + summarizeTimeoutMargin
}

func (w *SummarizeAgendaWorker) Work(ctx context.Context, job *river.Job[SummarizeAgendaArgs]) error {
	id := job.Args.AgendaVersionID
	logger := w.logger.With("agenda_version_id", id, "job_id", job.ID)
	if !w.enabled(ctx, FlagSummarizeAgendas) {
		logger.InfoContext(ctx, "agenda summary skipped: flag off", "flag", FlagSummarizeAgendas)
		return nil
	}
	if w.llm == nil {
		return river.JobCancel(ErrNoLLM)
	}

	v, err := w.store.GetAgendaVersion(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger = logger.With("meeting_id", v.MeetingID, "version", v.Version)
	if v.Summary != nil {
		logger.InfoContext(ctx, "agenda summary skipped: already summarized")
		return nil
	}
	latest, err := w.store.LatestAgendaVersion(ctx, v.MeetingID)
	if err != nil {
		return err
	}
	if latest.ID != v.ID {
		logger.InfoContext(ctx, "agenda summary skipped: superseded", "latest_version", latest.Version)
		return nil
	}

	result, err := summarize.Agenda(ctx, w.llm, w.tier, v.Text)
	attrs := []any{
		"tier", w.tier, "model", result.Response.Model, "finish_reason", result.Response.FinishReason,
		"input_tokens", result.Response.Usage.InputTokens, "output_tokens", result.Response.Usage.OutputTokens,
		"input_bytes", result.InputBytes, "text_bytes", len(v.Text), "input_truncated", result.InputTruncated,
	}
	if err != nil {
		logger.WarnContext(ctx, "agenda not summarized", append(attrs, "error", err)...)
		return summaryError(err)
	}

	_, written, err := w.store.SetAgendaSummaryIfUnset(ctx, v.ID, result.Summary)
	if errors.Is(err, store.ErrNotFound) {
		return river.JobCancel(err)
	}
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agenda summarized", append(attrs, "written", written, "summary_bytes", len(result.Summary))...)
	return nil
}

func summaryError(err error) error {
	err = fmt.Errorf("summarize agenda: %w", err)
	if llm.IsRetryable(err) {
		return err
	}
	return river.JobCancel(err)
}
