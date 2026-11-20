package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrInvalidUser   = errors.New("invalid user")
	ErrInvalidRole   = errors.New("invalid role")
	ErrAmbiguousUser = errors.New("more than one user matches")
	ErrUserLocked    = errors.New("the user's row is locked by another transaction")
)

const pgLockNotAvailable = "55P03"

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

func ParseRole(s string) (Role, error) {
	switch r := Role(s); r {
	case RoleUser, RoleAdmin:
		return r, nil
	}
	return "", fmt.Errorf("%w: %q, want %q or %q", ErrInvalidRole, s, RoleUser, RoleAdmin)
}

func (r Role) IsAdmin() bool { return r == RoleAdmin }

type UserInput struct {
	WorkOSUserID string
	Email        string
}

type User struct {
	ID           uuid.UUID
	WorkOSUserID string
	Email        *string
	Role         Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const userColumns = `id, workos_user_id, email, role, created_at, updated_at`

func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	dest := append([]any{&u.ID, &u.WorkOSUserID, &u.Email, &u.Role, &u.CreatedAt, &u.UpdatedAt}, extra...)
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

type UserRef struct {
	ID    uuid.UUID
	Email string
}

type RoleChange struct {
	User     User
	Previous Role
}

func (s *Store) SetUserRole(ctx context.Context, ref UserRef, role Role, lockTimeout time.Duration) (RoleChange, error) {
	if _, err := ParseRole(string(role)); err != nil {
		return RoleChange{}, fmt.Errorf("set user role: %w", err)
	}
	if lockTimeout < time.Millisecond {
		return RoleChange{}, fmt.Errorf("set user role: lock timeout %s is below 1ms", lockTimeout)
	}
	if (ref.ID == uuid.Nil) == (ref.Email == "") {
		return RoleChange{}, fmt.Errorf("set user role: give exactly one of an id or an email: %w", ErrInvalidUser)
	}
	var change RoleChange
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", lockTimeout.Milliseconds())); err != nil {
			return err
		}
		target, err := lockUserRef(ctx, tx, ref)
		if err != nil {
			return err
		}
		change.Previous = target.Role
		change.User, err = scanUser(tx.QueryRow(ctx, `
UPDATE users
SET role = $2,
    updated_at = CASE WHEN role = $2 THEN updated_at ELSE now() END
WHERE id = $1
RETURNING `+userColumns, target.ID, string(role)))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgLockNotAvailable {
		return RoleChange{}, fmt.Errorf("set user role: %w (waited %s)", ErrUserLocked, lockTimeout)
	}
	if err != nil {
		return RoleChange{}, fmt.Errorf("set user role: %w", err)
	}
	return change, nil
}

func lockUserRef(ctx context.Context, tx pgx.Tx, ref UserRef) (User, error) {
	query, arg := `SELECT `+userColumns+` FROM users WHERE id = $1 FOR UPDATE`, any(ref.ID)
	if ref.ID == uuid.Nil {
		query, arg = `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1) ORDER BY created_at, id FOR UPDATE`, ref.Email
	}
	rows, err := tx.Query(ctx, query, arg)
	if err != nil {
		return User{}, err
	}
	matches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (User, error) { return scanUser(row) })
	if err != nil {
		return User{}, err
	}
	switch len(matches) {
	case 0:
		return User{}, ErrNotFound
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, m := range matches {
		ids[i] = m.ID.String()
	}
	return User{}, fmt.Errorf("%w: %s", ErrAmbiguousUser, strings.Join(ids, ", "))
}
