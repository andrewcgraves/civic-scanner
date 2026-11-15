// Package store is the Postgres data access layer.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Agency is a tracked public agency.
type Agency struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
}

// Store runs queries against a connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and verifies the connection.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// ListAgencies returns all agencies ordered by name.
func (s *Store) ListAgencies(ctx context.Context) ([]Agency, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, slug, name, created_at FROM agencies ORDER BY name, slug`)
	if err != nil {
		return nil, fmt.Errorf("query agencies: %w", err)
	}
	agencies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Agency, error) {
		var a Agency
		err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.CreatedAt)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan agencies: %w", err)
	}
	return agencies, nil
}
