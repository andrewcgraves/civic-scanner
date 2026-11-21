package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	ScreeningComplete = "complete"
	ScreeningFailed   = "failed"
)

type ScreeningEvidence struct {
	Supports string `json:"supports"`
	Signal   string `json:"signal"`
	URL      string `json:"url"`
	Page     string `json:"page"`
}

type ScreeningPage struct {
	URL   string `json:"url"`
	Error string `json:"error,omitempty"`
}

type AgencyScreening struct {
	AgencyID   uuid.UUID
	Status     string
	Verdict    string
	Evidence   []ScreeningEvidence
	Pages      []ScreeningPage
	Failure    string
	ScreenedAt time.Time
}

type ScreeningTarget struct {
	AgencyID uuid.UUID
	Status   string
	Website  string
}

func (s *Store) GetScreeningTarget(ctx context.Context, agencyID uuid.UUID) (ScreeningTarget, error) {
	t := ScreeningTarget{AgencyID: agencyID}
	err := s.pool.QueryRow(ctx, `SELECT status, website FROM agencies WHERE id = $1`, agencyID).Scan(&t.Status, &t.Website)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScreeningTarget{}, ErrNotFound
	}
	if err != nil {
		return ScreeningTarget{}, fmt.Errorf("get screening target: %w", err)
	}
	return t, nil
}

func (s *Store) SaveAgencyScreening(ctx context.Context, in AgencyScreening) error {
	if in.Evidence == nil {
		in.Evidence = []ScreeningEvidence{}
	}
	if in.Pages == nil {
		in.Pages = []ScreeningPage{}
	}
	evidence, err := json.Marshal(in.Evidence)
	if err != nil {
		return fmt.Errorf("save agency screening: %w", err)
	}
	pages, err := json.Marshal(in.Pages)
	if err != nil {
		return fmt.Errorf("save agency screening: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
INSERT INTO agency_screenings (agency_id, status, verdict, evidence, pages, failure, screened_at)
SELECT id, $2::text, NULLIF($3::text, ''), $4::jsonb, $5::jsonb, NULLIF($6::text, ''), $7::timestamptz FROM agencies WHERE id = $1
ON CONFLICT (agency_id) DO UPDATE SET
    status = EXCLUDED.status,
    verdict = EXCLUDED.verdict,
    evidence = EXCLUDED.evidence,
    pages = EXCLUDED.pages,
    failure = EXCLUDED.failure,
    screened_at = EXCLUDED.screened_at`,
		in.AgencyID, in.Status, in.Verdict, evidence, pages, in.Failure, in.ScreenedAt)
	if err != nil {
		return fmt.Errorf("save agency screening: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetAgencyScreening(ctx context.Context, agencyID uuid.UUID) (AgencyScreening, error) {
	sc := AgencyScreening{AgencyID: agencyID}
	var verdict, failure *string
	err := s.pool.QueryRow(ctx, `
SELECT status, verdict, evidence, pages, failure, screened_at
FROM agency_screenings WHERE agency_id = $1`, agencyID).
		Scan(&sc.Status, &verdict, &sc.Evidence, &sc.Pages, &failure, &sc.ScreenedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgencyScreening{}, ErrNotFound
	}
	if err != nil {
		return AgencyScreening{}, fmt.Errorf("get agency screening: %w", err)
	}
	if verdict != nil {
		sc.Verdict = *verdict
	}
	if failure != nil {
		sc.Failure = *failure
	}
	return sc, nil
}
