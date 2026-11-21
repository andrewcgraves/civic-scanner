//go:build integration

package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestWebhookSubscriptionTemplate(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	f := seedDeliveryFixture(t, pool)
	slug := f.slug

	template := "{\n  \"text\": \"{{meeting.title}}\"\n}"
	in := webhookInput(user, slug, "https://hooks.example.com/custom")
	in.Format, in.Template = store.WebhookFormatCustom, &template
	created, err := s.CreateWebhookSubscription(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Format != store.WebhookFormatCustom || created.Template == nil || *created.Template != template {
		t.Errorf("created = %+v", created)
	}
	got, err := s.GetWebhookSubscription(ctx, user.ID, created.ID)
	if err != nil || got.Template == nil || *got.Template != template {
		t.Errorf("get = %+v, %v", got, err)
	}

	slack := store.WebhookFormatSlack
	updated, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Format: &slack})
	if err != nil || updated.Format != slack || updated.Template == nil || *updated.Template != template {
		t.Errorf("to slack = %+v, %v", updated, err)
	}
	next := `{"b":"{{agency.name}}"}`
	custom := store.WebhookFormatCustom
	updated, err = s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Format: &custom, Template: &next})
	if err != nil || updated.Format != custom || *updated.Template != next {
		t.Errorf("back to custom = %+v, %v", updated, err)
	}

	delivery, err := s.WebhookDelivery(ctx, created.ID, f.event.ID)
	if err != nil || delivery.Format != custom || delivery.Template == nil || *delivery.Template != next {
		t.Errorf("delivery = %+v, %v", delivery, err)
	}

	plain, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, "https://hooks.example.com/plain"))
	if err != nil || plain.Template != nil {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, plain.ID, store.WebhookSubscriptionUpdate{Format: &custom}); !errors.Is(err, store.ErrWebhookTemplateRequired) {
		t.Errorf("custom without a template = %v", err)
	}
	if after, _ := s.GetWebhookSubscription(ctx, user.ID, plain.ID); after.Format != store.WebhookFormatStandard {
		t.Errorf("the refused update changed the format: %+v", after)
	}

	noTemplate := webhookInput(user, slug, "https://hooks.example.com/none")
	noTemplate.Format = custom
	if _, err := s.CreateWebhookSubscription(ctx, noTemplate); !errors.Is(err, store.ErrInvalidWebhookSubscription) {
		t.Errorf("create custom without a template = %v", err)
	}
}

func TestWebhookSubscriptionTemplateConstraints(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	slug := webhookAgency(t, pool, "live")
	sub, err := s.CreateWebhookSubscription(ctx, webhookInput(user, slug, "https://hooks.example.com/constraints"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ sql, constraint string }{
		"custom without a template": {`UPDATE webhook_subscriptions SET format = 'custom' WHERE id = $1`, "webhook_subscriptions_custom_template_check"},
		"unknown format":            {`UPDATE webhook_subscriptions SET format = 'teams' WHERE id = $1`, "webhook_subscriptions_format_v2_check"},
		"oversized template":        {`UPDATE webhook_subscriptions SET template = '` + strings.Repeat("x", 8193) + `' WHERE id = $1`, "webhook_subscriptions_template_size_check"},
	} {
		_, err := pool.Exec(ctx, tc.sql, sub.ID)
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET template = '`+strings.Repeat("é", 4096)+`' WHERE id = $1`, sub.ID); err != nil {
		t.Errorf("a template of exactly 8192 bytes: %v", err)
	}
}
