package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	setRoleCommand  = "set-role"
	nothingChanged  = "nothing changed"
	roleLockTimeout = 5 * time.Second
)

var (
	errSetRoleUsage      = errors.New("usage: worker " + setRoleCommand + " (<user-id> | <email>) (user | admin)")
	errRoleNotMigrated   = errors.New("database has no users.role column: run /app/migrate up first; " + nothingChanged)
	errUserNotFound      = errors.New("no user matches; a user exists only after signing in once; " + nothingChanged)
	errAmbiguousUserHint = errors.New("rerun with one of the user ids above; " + nothingChanged)
	errUserBusy          = errors.New("another session is changing this user right now; " + nothingChanged + "; try again")
)

const setRoleHelp = `usage: worker set-role (<user-id> | <email>) (user | admin)

Sets the role of one user who has signed in at least once, prints the change
and exits. "admin" lets the user call the admin routes; "user" takes that away.
The API reads the role on every request, so the change applies to the user's
next request. It needs DATABASE_URL and never starts the worker.

  <user-id>         the user's id in this service (the id GET /v1/me returns)
  <email>           the user's email, matched without regard to case; refused
                    when more than one user has it
  -h, --help, help  print this help
`

type roleTarget struct {
	ref  store.UserRef
	role store.Role
	help bool
}

func parseSetRoleArgs(args []string) (roleTarget, error) {
	if len(args) == 1 && isHelp(args[0]) {
		return roleTarget{help: true}, nil
	}
	if len(args) != 2 {
		return roleTarget{}, errSetRoleUsage
	}
	role, err := store.ParseRole(args[1])
	if err != nil {
		return roleTarget{}, fmt.Errorf("%w: %w", errSetRoleUsage, err)
	}
	who := strings.TrimSpace(args[0])
	if id, err := uuid.Parse(who); err == nil && id != uuid.Nil {
		return roleTarget{ref: store.UserRef{ID: id}, role: role}, nil
	}
	if strings.Contains(who, "@") && !strings.HasPrefix(who, "-") {
		return roleTarget{ref: store.UserRef{Email: who}, role: role}, nil
	}
	return roleTarget{}, fmt.Errorf("%w: %q is neither a user id nor an email", errSetRoleUsage, args[0])
}

func setRole(ctx context.Context, args []string, stdout io.Writer) error {
	target, err := parseSetRoleArgs(args)
	if err != nil {
		return err
	}
	if target.help {
		_, err := io.WriteString(stdout, setRoleHelp)
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := openCommandPool(ctx, cfg.DatabaseURL, nothingChanged)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := requireRoleColumn(ctx, pool); err != nil {
		return err
	}

	return applyRole(ctx, pool, target, roleLockTimeout, stdout)
}

func applyRole(ctx context.Context, pool *pgxpool.Pool, target roleTarget, lockTimeout time.Duration, stdout io.Writer) error {
	change, err := store.New(pool).SetUserRole(ctx, target.ref, target.role, lockTimeout)
	switch {
	case errors.Is(err, store.ErrUserLocked):
		return fmt.Errorf("%w (%w)", errUserBusy, err)
	case errors.Is(err, store.ErrNotFound):
		return errUserNotFound
	case errors.Is(err, store.ErrAmbiguousUser):
		return fmt.Errorf("%w; %w", err, errAmbiguousUserHint)
	case err != nil:
		return fmt.Errorf("%w; %s", err, nothingChanged)
	}
	return reportRoleChange(stdout, change)
}

func requireRoleColumn(ctx context.Context, pool *pgxpool.Pool) error {
	var migrated bool
	err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'role'
)`).Scan(&migrated)
	if err != nil {
		return fmt.Errorf("check database schema: %w", err)
	}
	if !migrated {
		return errRoleNotMigrated
	}
	return nil
}

func reportRoleChange(w io.Writer, change store.RoleChange) error {
	u := change.User
	email := "none"
	if u.Email != nil {
		email = *u.Email
	}
	verdict := fmt.Sprintf("role is now %s (was %s)", u.Role, change.Previous)
	if u.Role == change.Previous {
		verdict = fmt.Sprintf("role was already %s; nothing changed", u.Role)
	}
	_, err := fmt.Fprintf(w, "user %s: %s\n  email %s, WorkOS user %s, first seen %s\n",
		u.ID, verdict, email, u.WorkOSUserID, u.CreatedAt.UTC().Format(time.RFC3339))
	return err
}
