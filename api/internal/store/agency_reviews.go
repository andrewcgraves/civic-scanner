package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxPendingSubmissionsListed = 100
	MinSlugLength               = 2
	MaxSlugLength               = 60
	agencySlugConstraint        = "agencies_slug_key"
)

var (
	ErrNotPending  = errors.New("agency is not pending")
	ErrSlugTaken   = errors.New("slug is used by another agency")
	ErrInvalidSlug = errors.New("invalid slug")
	slugPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type AgencyStatus string

const (
	AgencyStatusPending  AgencyStatus = "pending"
	AgencyStatusLive     AgencyStatus = "live"
	AgencyStatusRejected AgencyStatus = "rejected"
)

type PendingAgencySubmission struct {
	AgencyID       uuid.UUID
	Name           string
	Website        string
	SubmittedAt    time.Time
	SubmittedBy    uuid.UUID
	SubmitterEmail *string
}

type ReviewedAgency struct {
	ID      uuid.UUID
	Slug    string
	Name    string
	Website string
	Status  AgencyStatus
}

func ValidLiveSlug(slug string) bool {
	return len(slug) >= MinSlugLength && len(slug) <= MaxSlugLength &&
		slugPattern.MatchString(slug) && !strings.HasPrefix(slug, SubmittedSlugPrefix)
}

func (s *Store) ListPendingAgencySubmissions(ctx context.Context) ([]PendingAgencySubmission, error) {
	rows, err := s.pool.Query(ctx, `
SELECT a.id, a.name, a.website, s.submitted_at, u.id, u.email
FROM agency_submissions s
JOIN agencies a ON a.id = s.agency_id AND a.status = 'pending'
JOIN users u ON u.id = s.submitted_by
ORDER BY s.submitted_at, a.id
LIMIT $1`, MaxPendingSubmissionsListed)
	if err != nil {
		return nil, fmt.Errorf("query pending submissions: %w", err)
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingAgencySubmission, error) {
		var p PendingAgencySubmission
		err := row.Scan(&p.AgencyID, &p.Name, &p.Website, &p.SubmittedAt, &p.SubmittedBy, &p.SubmitterEmail)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan pending submissions: %w", err)
	}
	return pending, nil
}

func (s *Store) ApproveAgencySubmission(ctx context.Context, id uuid.UUID, slug string) (ReviewedAgency, error) {
	if !ValidLiveSlug(slug) {
		return ReviewedAgency{}, fmt.Errorf("approve agency submission: %w", ErrInvalidSlug)
	}
	return s.reviewAgencySubmission(ctx, id, AgencyStatusLive, slug)
}

func (s *Store) RejectAgencySubmission(ctx context.Context, id uuid.UUID) (ReviewedAgency, error) {
	return s.reviewAgencySubmission(ctx, id, AgencyStatusRejected, "")
}

func (s *Store) reviewAgencySubmission(ctx context.Context, id uuid.UUID, to AgencyStatus, slug string) (ReviewedAgency, error) {
	var reviewed ReviewedAgency
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status AgencyStatus
		err := tx.QueryRow(ctx, `
SELECT a.status
FROM agencies a
JOIN agency_submissions s ON s.agency_id = a.id
WHERE a.id = $1
FOR UPDATE OF a`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock agency: %w", err)
		}
		if status != AgencyStatusPending {
			return ErrNotPending
		}
		err = tx.QueryRow(ctx, `
UPDATE agencies SET status = $2, slug = COALESCE(NULLIF($3, ''), slug)
WHERE id = $1
RETURNING id, slug, name, website, status`, id, to, slug).
			Scan(&reviewed.ID, &reviewed.Slug, &reviewed.Name, &reviewed.Website, &reviewed.Status)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation && pgErr.ConstraintName == agencySlugConstraint {
			return ErrSlugTaken
		}
		if err != nil {
			return fmt.Errorf("update agency: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotPending) || errors.Is(err, ErrSlugTaken) {
		return ReviewedAgency{}, err
	}
	if err != nil {
		return ReviewedAgency{}, fmt.Errorf("review agency submission: %w", err)
	}
	return reviewed, nil
}
