//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func webhookUser(t *testing.T, pool *pgxpool.Pool) store.User {
	t.Helper()
	u, _, err := store.New(pool).EnsureUser(t.Context(), store.UserInput{WorkOSUserID: workOSUserID(t, pool)})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func webhookAgency(t *testing.T, pool *pgxpool.Pool, status string) string {
	t.Helper()
	slug := "civ46-" + uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO agencies (slug, name, status, website) VALUES ($1, 'Webhook Test Agency', $2, 'https://example.com')`, slug, status); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM agencies WHERE slug = $1`, slug)
	})
	return slug
}

func webhookInput(user store.User, slug, url string) store.WebhookSubscriptionInput {
	return store.WebhookSubscriptionInput{
		ID: uuid.New(), UserID: user.ID, AgencySlug: slug, URL: url,
		PayloadVersion: store.WebhookPayloadV1, SecretCiphertext: []byte("sealed-" + uuid.NewString()),
	}
}

func TestWebhookSubscriptionLifecycle(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	other := webhookUser(t, pool)
	slug := webhookAgency(t, pool, "live")

	in := webhookInput(user, slug, "https://hooks.example.com/a")
	before := databaseClock(t, pool)
	created, err := s.CreateWebhookSubscription(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != in.ID || created.UserID != user.ID || created.AgencySlug != slug || created.AgencyName != "Webhook Test Agency" ||
		created.URL != in.URL || created.PayloadVersion != "v1" || created.Format != store.WebhookFormatStandard || created.Status != store.WebhookActive {
		t.Errorf("created = %+v", created)
	}
	if created.CreatedAt.Before(before) || !created.SecretRotatedAt.Equal(created.CreatedAt) || !created.UpdatedAt.Equal(created.CreatedAt) {
		t.Errorf("times: created %v, rotated %v, updated %v (before %v)", created.CreatedAt, created.SecretRotatedAt, created.UpdatedAt, before)
	}
	sealed, err := s.WebhookSubscriptionSecret(ctx, in.ID)
	if err != nil || string(sealed) != string(in.SecretCiphertext) {
		t.Errorf("stored secret = %q, %v", sealed, err)
	}

	second, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, "https://hooks.example.com/b"))
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWebhookSubscriptions(ctx, user.ID)
	if err != nil || len(list) != 2 || list[0].ID != created.ID || list[1].ID != second.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, err := s.ListWebhookSubscriptions(ctx, other.ID); err != nil || len(list) != 0 {
		t.Errorf("another user's list = %+v, %v", list, err)
	}
	if got, err := s.GetWebhookSubscription(ctx, user.ID, created.ID); err != nil || got != created {
		t.Errorf("get = %+v, %v; want %+v", got, err, created)
	}

	paused := store.WebhookPaused
	updated, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Status: &paused})
	if err != nil || updated.Status != store.WebhookPaused || updated.URL != created.URL || !updated.UpdatedAt.After(created.UpdatedAt) || !updated.SecretRotatedAt.Equal(created.SecretRotatedAt) {
		t.Errorf("pause = %+v, %v", updated, err)
	}
	url := "https://hooks.example.com/c?x=1"
	active := store.WebhookActive
	updated, err = s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{URL: &url, Status: &active})
	if err != nil || updated.Status != store.WebhookActive || updated.URL != url || updated.PayloadVersion != "v1" {
		t.Errorf("resume and move = %+v, %v", updated, err)
	}
	slack := store.WebhookFormatSlack
	updated, err = s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Format: &slack})
	if err != nil || updated.Format != store.WebhookFormatSlack || updated.URL != url || updated.Status != store.WebhookActive {
		t.Errorf("format = %+v, %v", updated, err)
	}
	bogus := store.WebhookFormat("teams")
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Format: &bogus}); !errors.Is(err, store.ErrInvalidWebhookSubscription) {
		t.Errorf("unknown format: err = %v", err)
	}
	discord := webhookInput(user, slug, "https://hooks.example.com/discord")
	discord.Format = store.WebhookFormatDiscord
	if got, err := s.CreateWebhookSubscription(ctx, discord); err != nil || got.Format != store.WebhookFormatDiscord {
		t.Errorf("create with a preset = %+v, %v", got, err)
	}
	taken := second.URL
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{URL: &taken}); !errors.Is(err, store.ErrWebhookSubscriptionExists) {
		t.Errorf("update to a URL already subscribed: err = %v", err)
	}
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{}); !errors.Is(err, store.ErrInvalidWebhookSubscription) {
		t.Errorf("empty update: err = %v", err)
	}

	rotated, err := s.RotateWebhookSubscriptionSecret(ctx, user.ID, created.ID, []byte("new-sealed"))
	if err != nil || !rotated.SecretRotatedAt.After(created.SecretRotatedAt) {
		t.Errorf("rotate = %+v, %v", rotated, err)
	}
	if sealed, _ := s.WebhookSubscriptionSecret(ctx, created.ID); string(sealed) != "new-sealed" {
		t.Errorf("stored secret after rotation = %q", sealed)
	}

	if err := s.DeleteWebhookSubscription(ctx, user.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWebhookSubscription(ctx, user.ID, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: err = %v", err)
	}
	if _, err := s.GetWebhookSubscription(ctx, user.ID, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get after delete: err = %v", err)
	}
	if _, err := s.WebhookSubscriptionSecret(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("secret after delete: err = %v", err)
	}
}

func TestWebhookSubscriptionsBelongToTheirUser(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	owner := webhookUser(t, pool)
	stranger := webhookUser(t, pool)
	sub, err := s.CreateWebhookSubscription(ctx, webhookInput(owner, webhookAgency(t, pool, "live"), "https://hooks.example.com/mine"))
	if err != nil {
		t.Fatal(err)
	}
	paused := store.WebhookPaused
	if _, err := s.GetWebhookSubscription(ctx, stranger.ID, sub.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := s.UpdateWebhookSubscription(ctx, stranger.ID, sub.ID, store.WebhookSubscriptionUpdate{Status: &paused}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update: %v", err)
	}
	if _, err := s.RotateWebhookSubscriptionSecret(ctx, stranger.ID, sub.ID, []byte("x")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rotate: %v", err)
	}
	if err := s.DeleteWebhookSubscription(ctx, stranger.ID, sub.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
	got, err := s.GetWebhookSubscription(ctx, owner.ID, sub.ID)
	if err != nil || got != sub {
		t.Errorf("the owner's subscription changed: %+v, %v", got, err)
	}
}

func TestCreateWebhookSubscriptionRefuses(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	live := webhookAgency(t, pool, "live")

	for _, status := range []string{"pending", "rejected"} {
		if _, err := s.CreateWebhookSubscription(ctx, webhookInput(user, webhookAgency(t, pool, status), "https://hooks.example.com/x")); !errors.Is(err, store.ErrUnknownAgency) {
			t.Errorf("%s agency: err = %v", status, err)
		}
	}
	if _, err := s.CreateWebhookSubscription(ctx, webhookInput(user, "civ46-no-such-agency", "https://hooks.example.com/x")); !errors.Is(err, store.ErrUnknownAgency) {
		t.Errorf("unknown agency: err = %v", err)
	}
	if _, err := s.CreateWebhookSubscription(ctx, webhookInput(user, live, "https://hooks.example.com/dup")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWebhookSubscription(ctx, webhookInput(user, live, "https://hooks.example.com/dup")); !errors.Is(err, store.ErrWebhookSubscriptionExists) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := s.CreateWebhookSubscription(ctx, webhookInput(webhookUser(t, pool), live, "https://hooks.example.com/dup")); err != nil {
		t.Errorf("another user, same agency and URL: %v", err)
	}
	for name, mutate := range map[string]func(*store.WebhookSubscriptionInput){
		"no id":      func(in *store.WebhookSubscriptionInput) { in.ID = uuid.Nil },
		"no user":    func(in *store.WebhookSubscriptionInput) { in.UserID = uuid.Nil },
		"no agency":  func(in *store.WebhookSubscriptionInput) { in.AgencySlug = "" },
		"no url":     func(in *store.WebhookSubscriptionInput) { in.URL = "" },
		"no version": func(in *store.WebhookSubscriptionInput) { in.PayloadVersion = "" },
		"no secret":  func(in *store.WebhookSubscriptionInput) { in.SecretCiphertext = nil },
		"bad format": func(in *store.WebhookSubscriptionInput) { in.Format = "teams" },
	} {
		in := webhookInput(user, live, "https://hooks.example.com/"+name)
		mutate(&in)
		if _, err := s.CreateWebhookSubscription(ctx, in); !errors.Is(err, store.ErrInvalidWebhookSubscription) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, in := range map[string]store.WebhookSubscriptionInput{
		"http url": webhookInput(user, live, "http://hooks.example.com/x"),
		"unknown version": func() store.WebhookSubscriptionInput {
			in := webhookInput(user, live, "https://hooks.example.com/v9")
			in.PayloadVersion = "v9"
			return in
		}(),
	} {
		if _, err := s.CreateWebhookSubscription(ctx, in); err == nil || errors.Is(err, store.ErrInvalidWebhookSubscription) {
			t.Errorf("%s: err = %v, want a check constraint violation", name, err)
		}
	}
	bogus := store.WebhookStatus("disabled")
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM webhook_subscriptions WHERE user_id = $1 LIMIT 1`, user.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, id, store.WebhookSubscriptionUpdate{Status: &bogus}); err == nil {
		t.Error("an unknown status was stored")
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET format = 'teams' WHERE id = $1`, id); err == nil {
		t.Error("the database stored an unknown format")
	}
}

func TestWebhookSubscriptionLimitHoldsUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	slug := webhookAgency(t, pool, "live")

	attempts := store.MaxWebhookSubscriptionsPerUser + 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		limited int
		other   []error
	)
	for i := range attempts {
		wg.Go(func() {
			_, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, fmt.Sprintf("https://hooks.example.com/%d", i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrWebhookSubscriptionLimit):
				limited++
			default:
				other = append(other, err)
			}
		})
	}
	wg.Wait()
	if ok != store.MaxWebhookSubscriptionsPerUser || limited != attempts-ok || len(other) != 0 {
		t.Fatalf("ok %d, limited %d, other %v", ok, limited, other)
	}
	list, err := s.ListWebhookSubscriptions(ctx, user.ID)
	if err != nil || len(list) != store.MaxWebhookSubscriptionsPerUser {
		t.Fatalf("list has %d, %v", len(list), err)
	}
	if err := s.DeleteWebhookSubscription(ctx, user.ID, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, "https://hooks.example.com/after-delete")); err != nil {
		t.Errorf("after deleting one: %v", err)
	}
}

func TestWebhookSubscriptionsGoWithTheirUserAndAgency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	slug := webhookAgency(t, pool, "live")
	sub, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, "https://hooks.example.com/gone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agencies WHERE slug = $1`, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebhookSubscriptionSecret(ctx, sub.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after deleting the agency: %v", err)
	}

	sub, err = s.CreateWebhookSubscription(ctx, webhookInput(user, webhookAgency(t, pool, "live"), "https://hooks.example.com/gone"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WebhookSubscriptionSecret(ctx, sub.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after deleting the user: %v", err)
	}
}
