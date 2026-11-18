package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Agency struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	Website   string
	CreatedAt time.Time
}

type Store struct {
	pool *pgxpool.Pool
}

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

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) ListAgencies(ctx context.Context) ([]Agency, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, slug, name, website, created_at FROM agencies WHERE status = 'live' ORDER BY name, slug`)
	if err != nil {
		return nil, fmt.Errorf("query agencies: %w", err)
	}
	agencies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Agency, error) {
		var a Agency
		err := row.Scan(&a.ID, &a.Slug, &a.Name, &a.Website, &a.CreatedAt)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan agencies: %w", err)
	}
	return agencies, nil
}

func (s *Store) GetLiveAgency(ctx context.Context, slug string) (Agency, error) {
	var a Agency
	err := s.pool.QueryRow(ctx, `SELECT id, slug, name, website, created_at FROM agencies WHERE slug = $1 AND status = 'live'`, slug).
		Scan(&a.ID, &a.Slug, &a.Name, &a.Website, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agency{}, ErrNotFound
	}
	if err != nil {
		return Agency{}, fmt.Errorf("get agency: %w", err)
	}
	return a, nil
}
