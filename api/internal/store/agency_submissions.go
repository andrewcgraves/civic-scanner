package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	MaxPendingSubmissionsPerUser = 5
	SubmittedSlugPrefix          = "submitted-"
	uniqueViolation              = "23505"
	siteKeyConstraint            = "agency_websites_site_key_unique"
)

var (
	ErrWebsiteOnFile   = errors.New("website already on file")
	ErrSubmissionLimit = errors.New("too many pending submissions")
	ErrInvalidInput    = errors.New("invalid agency submission")
)

type AgencySubmissionInput struct {
	UserID     uuid.UUID
	Name       string
	Website    string
	WebsiteKey string
}

type AgencySubmission struct {
	AgencyID    uuid.UUID
	Slug        string
	Name        string
	Website     string
	SubmittedBy uuid.UUID
	SubmittedAt time.Time
}

func SubmittedSlug(id uuid.UUID) string {
	return SubmittedSlugPrefix + hex.EncodeToString(id[:])
}

func (s *Store) CreateAgencySubmission(ctx context.Context, in AgencySubmissionInput) (AgencySubmission, error) {
	if in.UserID == uuid.Nil || in.Name == "" || in.Website == "" || in.WebsiteKey == "" {
		return AgencySubmission{}, fmt.Errorf("create agency submission: %w", ErrInvalidInput)
	}
	id := uuid.New()
	sub := AgencySubmission{AgencyID: id, Slug: SubmittedSlug(id), Name: in.Name, Website: in.Website, SubmittedBy: in.UserID}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, in.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var pending int
		err := tx.QueryRow(ctx, `
SELECT count(*)
FROM agency_submissions s
JOIN agencies a ON a.id = s.agency_id AND a.status = 'pending'
WHERE s.submitted_by = $1`, in.UserID).Scan(&pending)
		if err != nil {
			return fmt.Errorf("count pending submissions: %w", err)
		}
		if pending >= MaxPendingSubmissionsPerUser {
			return ErrSubmissionLimit
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agencies (id, slug, name, status, website) VALUES ($1, $2, $3, 'pending', $4)`,
			sub.AgencyID, sub.Slug, sub.Name, sub.Website); err != nil {
			return fmt.Errorf("insert agency: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO agency_websites (agency_id, site_key) VALUES ($1, $2)`, sub.AgencyID, in.WebsiteKey)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation && pgErr.ConstraintName == siteKeyConstraint {
			return ErrWebsiteOnFile
		}
		if err != nil {
			return fmt.Errorf("insert agency website: %w", err)
		}
		err = tx.QueryRow(ctx, `INSERT INTO agency_submissions (agency_id, submitted_by) VALUES ($1, $2) RETURNING submitted_at`,
			sub.AgencyID, in.UserID).Scan(&sub.SubmittedAt)
		if err != nil {
			return fmt.Errorf("insert agency submission: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrWebsiteOnFile) || errors.Is(err, ErrSubmissionLimit) {
		return AgencySubmission{}, err
	}
	if err != nil {
		return AgencySubmission{}, fmt.Errorf("create agency submission: %w", err)
	}
	return sub, nil
}
