package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidUser = errors.New("invalid user")

type UserInput struct {
	WorkOSUserID string
	Email        string
}

type User struct {
	ID           uuid.UUID
	WorkOSUserID string
	Email        *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const userColumns = `id, workos_user_id, email, created_at, updated_at`

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	dest := append([]any{&u.ID, &u.WorkOSUserID, &u.Email, &u.CreatedAt, &u.UpdatedAt}, extra...)
	err := row.Scan(dest...)
	return u, err
}

func (s *Store) EnsureUser(ctx context.Context, in UserInput) (User, bool, error) {
	if in.WorkOSUserID == "" {
		return User{}, false, fmt.Errorf("ensure user: empty WorkOS user id: %w", ErrInvalidUser)
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE workos_user_id = $1`, in.WorkOSUserID))
	switch {
	case err == nil && (in.Email == "" || (u.Email != nil && *u.Email == in.Email)):
		return u, false, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return User{}, false, fmt.Errorf("ensure user: get: %w", err)
	}

	var created bool
	u, err = scanUser(s.pool.QueryRow(ctx, `
INSERT INTO users (workos_user_id, email)
VALUES ($1, NULLIF($2, ''))
ON CONFLICT (workos_user_id) DO UPDATE
SET email = COALESCE(EXCLUDED.email, users.email),
    updated_at = CASE WHEN users.email IS DISTINCT FROM COALESCE(EXCLUDED.email, users.email) THEN now() ELSE users.updated_at END
RETURNING `+userColumns+`, xmax = 0`,
		in.WorkOSUserID, in.Email), &created)
	if err != nil {
		return User{}, false, fmt.Errorf("ensure user: upsert: %w", err)
	}
	return u, created, nil
}
