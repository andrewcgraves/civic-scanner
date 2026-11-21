package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidStripeCustomer = errors.New("invalid stripe customer id")

func (s *Store) StripeCustomerID(ctx context.Context, userID uuid.UUID) (string, error) {
	var id *string
	err := s.pool.QueryRow(ctx, `SELECT stripe_customer_id FROM users WHERE id = $1`, userID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrNotFound
	case err != nil:
		return "", fmt.Errorf("stripe customer id: %w", err)
	case id == nil:
		return "", nil
	}
	return *id, nil
}

func (s *Store) ClearStripeCustomerID(ctx context.Context, userID uuid.UUID, customerID string) error {
	if _, err := s.pool.Exec(ctx, `
UPDATE users SET stripe_customer_id = NULL, plan = 'free', updated_at = now()
WHERE id = $1 AND stripe_customer_id = $2`, userID, customerID); err != nil {
		return fmt.Errorf("clear stripe customer id: %w", err)
	}
	return nil
}

func (s *Store) SetStripeCustomerID(ctx context.Context, userID uuid.UUID, customerID string) (string, error) {
	if customerID == "" {
		return "", fmt.Errorf("set stripe customer id: %w", ErrInvalidStripeCustomer)
	}
	var stored string
	err := s.pool.QueryRow(ctx, `
UPDATE users SET stripe_customer_id = $2, updated_at = now()
WHERE id = $1 AND stripe_customer_id IS NULL
RETURNING stripe_customer_id`, userID, customerID).Scan(&stored)
	switch {
	case err == nil:
		return stored, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("set stripe customer id: %w", err)
	}
	stored, err = s.StripeCustomerID(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("set stripe customer id: %w", err)
	}
	if stored == "" {
		return "", fmt.Errorf("set stripe customer id: %w", ErrNotFound)
	}
	return stored, nil
}
