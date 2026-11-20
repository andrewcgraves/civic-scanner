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

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	checkAgencyTimeout = 10 * time.Minute
	checkMaxAttempts   = 5
	lookbackDays       = 30
	lookaheadDays      = 90
)

type CheckAgencyArgs struct {
	AgencySlug string `json:"agency_slug"`
	Manual     bool   `json:"manual,omitempty"`
}

func (CheckAgencyArgs) Kind() string { return "check_agency" }

func (a CheckAgencyArgs) InsertOpts() river.InsertOpts {
	if a.Manual {
		return river.InsertOpts{MaxAttempts: checkMaxAttempts, UniqueOpts: manualUniqueOpts()}
	}
	return river.InsertOpts{
		MaxAttempts: checkMaxAttempts,
		UniqueOpts:  river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour, ByQueue: true},
	}
}

type UpsertedMeeting struct {
	Meeting       store.Meeting
	Created       bool
	AgendaURL     string
	AgendaSettled bool
	RecordingURL  string
	Manual        bool
}

type FollowUp interface {
	MeetingUpserted(ctx context.Context, tx pgx.Tx, m UpsertedMeeting) error
}

type NoFollowUp struct{}

func (NoFollowUp) MeetingUpserted(context.Context, pgx.Tx, UpsertedMeeting) error { return nil }

type CheckAgencyWorker struct {
	river.WorkerDefaults[CheckAgencyArgs]

	logger   *slog.Logger
	pool     *pgxpool.Pool
	store    *store.Store
	registry *sources.Registry
	clock    sources.Clock
	grace    time.Duration
	enabled  func(context.Context, string) bool
	followUp FollowUp
}

func (w *CheckAgencyWorker) Timeout(*river.Job[CheckAgencyArgs]) time.Duration {
	return checkAgencyTimeout
}

func (w *CheckAgencyWorker) Work(ctx context.Context, job *river.Job[CheckAgencyArgs]) error {
	slug := job.Args.AgencySlug
	logger := w.logger.With("agency_slug", slug, "job_id", job.ID, "trigger", trigger(job.Args.Manual))
	if job.Args.Manual {
		logger.InfoContext(ctx, "agency check triggered manually", "attempt", job.Attempt)
	}
	if !w.enabled(ctx, FlagCheckAgencies) {
		logger.InfoContext(ctx, "check agency skipped: flag off", "flag", FlagCheckAgencies)
		return nil
	}
	adapter, ok := w.registry.Lookup(slug)
	if !ok {
		logger.InfoContext(ctx, "check agency skipped: no source adapter")
		return nil
	}
	agency, err := w.store.GetLiveAgency(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		logger.InfoContext(ctx, "check agency skipped: agency is not live")
		return nil
	}
	if err != nil {
		return err
	}

	now := w.clock.Now()
	window := checkWindow(now)
	meetings, err := adapter.Meetings(ctx, window)
	if err != nil {
		return adapterError(slug, err)
	}
	if err := sources.Validate(meetings); err != nil {
		return river.JobCancel(fmt.Errorf("source adapter %s: %w", slug, err))
	}

	refresh := agendaRefresh{now: now, grace: w.grace, manual: job.Args.Manual}
	counts, err := w.upsert(ctx, agency.ID, meetings, refresh)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "agency checked", "meetings", len(meetings), "created", counts.created,
		"agendas_enqueued", counts.agendasEnqueued, "agendas_skipped_cutoff", counts.agendasSkipped,
		"agenda_cutoff", refresh.mode(), "agenda_refresh_grace", w.grace.String(),
		"window_from", window.From, "window_to", window.To)
	return nil
}

type checkCounts struct {
	created         int
	agendasEnqueued int
	agendasSkipped  int
}

type agendaRefresh struct {
	now    time.Time
	grace  time.Duration
	manual bool
}

func (r agendaRefresh) settled(startsAt time.Time, agendaURL, storedURL string) bool {
	if r.manual || agendaURL == "" || storedURL != agendaURL {
		return false
	}
	return !r.now.Before(startsAt.Add(r.grace))
}

func (r agendaRefresh) mode() string {
	if r.manual {
		return "bypassed"
	}
	return "applied"
}

func (w *CheckAgencyWorker) upsert(ctx context.Context, agencyID uuid.UUID, meetings []sources.Meeting, refresh agendaRefresh) (checkCounts, error) {
	var counts checkCounts
	err := pgx.BeginTxFunc(ctx, w.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		counts = checkCounts{}
		storedURLs, err := w.store.LatestAgendaSourceURLsTx(ctx, tx, agencyID, sourceIDs(meetings))
		if err != nil {
			return err
		}
		for _, m := range meetings {
			stored, isNew, err := w.store.UpsertMeetingTx(ctx, tx, store.MeetingInput{
				AgencyID:  agencyID,
				Title:     m.Title,
				StartsAt:  m.StartsAt.UTC(),
				Status:    string(m.Status),
				SourceURL: m.PageURL,
				SourceID:  m.SourceID,
			})
			if err != nil {
				return err
			}
			if isNew {
				counts.created++
			}
			settled := refresh.settled(stored.StartsAt, m.AgendaURL, storedURLs[m.SourceID])
			switch {
			case settled:
				counts.agendasSkipped++
			case m.AgendaURL != "":
				counts.agendasEnqueued++
			}
			if err := w.followUp.MeetingUpserted(ctx, tx, UpsertedMeeting{
				Meeting:       stored,
				Created:       isNew,
				AgendaURL:     m.AgendaURL,
				AgendaSettled: settled,
				RecordingURL:  m.RecordingURL,
				Manual:        refresh.manual,
			}); err != nil {
				return fmt.Errorf("follow up meeting %s: %w", m.SourceID, err)
			}
		}
		return nil
	})
	return counts, err
}

func sourceIDs(meetings []sources.Meeting) []string {
	ids := make([]string, 0, len(meetings))
	for _, m := range meetings {
		ids = append(ids, m.SourceID)
	}
	return ids
}

func checkWindow(now time.Time) sources.Window {
	now = now.UTC()
	return sources.Window{From: now.AddDate(0, 0, -lookbackDays), To: now.AddDate(0, 0, lookaheadDays)}
}

func adapterError(slug string, err error) error {
	err = fmt.Errorf("source adapter %s: %w", slug, err)
	if sources.Retryable(err) {
		return err
	}
	return river.JobCancel(err)
}
