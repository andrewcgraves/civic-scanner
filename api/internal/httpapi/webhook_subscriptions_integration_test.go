//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

func callJSON(t *testing.T, method, url, token, body string) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

func TestWebhookSubscriptionsAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	sealer, err := webhooks.NewSealer(config.NewSecret(string(bytes.Repeat([]byte{7}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagWebhookSubscriptions}, flag)
	}
	s := newAuthServer(t, flags, httpapi.WithWebhookSealer(sealer))
	strangerID := "user_integration_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, strangerID)
	})
	owner := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))
	stranger := signedToken(t, s.key, "k1", s.claims(strangerID, 5*time.Minute))

	slug := "civ46-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ($1, 'Webhook Agency', 'live', 'https://example.com')`, slug); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug = $1`, slug)
	})
	base := s.url + "/v1/webhook-subscriptions"
	create := `{"agency":"` + slug + `","url":"https://hooks.example.com/in"}`

	status, header, raw := callJSON(t, http.MethodPost, base, owner, create)
	if status != http.StatusPaymentRequired || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("free user: status %d, body %s", status, raw)
	}
	if status, _, raw := callJSON(t, http.MethodGet, base, stranger, ""); status != http.StatusPaymentRequired {
		t.Fatalf("free stranger: %d %s", status, raw)
	}
	for _, workosID := range []string{s.users, strangerID} {
		if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'paid' WHERE workos_user_id = $1`, workosID); err != nil {
			t.Fatal(err)
		}
	}

	status, header, raw = callJSON(t, http.MethodPost, base, owner, create)
	if status != http.StatusCreated || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: status %d, body %s", status, raw)
	}
	var created v1.WebhookSubscriptionWithSecret
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Agency != slug || created.Url != "https://hooks.example.com/in" || created.Status != v1.WebhookSubscriptionActive || !strings.HasPrefix(created.Secret, webhooks.SecretPrefix) {
		t.Errorf("created = %+v", created)
	}

	var stored []byte
	var ownerID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT w.secret_ciphertext, w.user_id FROM webhook_subscriptions w JOIN users u ON u.id = w.user_id WHERE w.id = $1 AND u.workos_user_id = $2`, created.Id, s.users).Scan(&stored, &ownerID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(created.Secret)) || bytes.Contains(stored, []byte(strings.TrimPrefix(created.Secret, webhooks.SecretPrefix))) {
		t.Error("the secret is stored in plain text")
	}
	if opened, err := sealer.Open(created.Id, stored); err != nil || opened.Reveal() != created.Secret {
		t.Errorf("stored secret does not open to the returned one: %v", err)
	}

	if status, _, raw := callJSON(t, http.MethodPost, base, owner, create); status != http.StatusConflict {
		t.Errorf("duplicate: %d %s", status, raw)
	}
	if status, _, raw := callJSON(t, http.MethodPost, base, owner, `{"agency":"civ46-nowhere","url":"https://hooks.example.com/in"}`); status != http.StatusBadRequest || !strings.Contains(string(raw), "unknown_agency") {
		t.Errorf("unknown agency: %d %s", status, raw)
	}

	one := base + "/" + created.Id.String()
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if status, _, raw := callJSON(t, method, one, stranger, ""); status != http.StatusNotFound {
			t.Errorf("stranger %s: %d %s", method, status, raw)
		}
	}
	if status, _, raw := callJSON(t, http.MethodPatch, one, stranger, `{"status":"paused"}`); status != http.StatusNotFound {
		t.Errorf("stranger PATCH: %d %s", status, raw)
	}
	if status, _, raw := callJSON(t, http.MethodPost, one+"/secret", stranger, ""); status != http.StatusNotFound {
		t.Errorf("stranger rotate: %d %s", status, raw)
	}
	status, _, raw = callJSON(t, http.MethodGet, base, stranger, "")
	var list v1.WebhookSubscriptionList
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil || len(list.Subscriptions) != 0 {
		t.Errorf("stranger list: %d %s", status, raw)
	}

	status, _, raw = callJSON(t, http.MethodPatch, one, owner, `{"status":"paused"}`)
	var updated v1.WebhookSubscription
	if err := json.Unmarshal(raw, &updated); status != http.StatusOK || err != nil || updated.Status != v1.WebhookSubscriptionPaused {
		t.Errorf("pause: %d %s", status, raw)
	}
	status, _, raw = callJSON(t, http.MethodPost, one+"/secret", owner, "")
	var rotated v1.WebhookSubscriptionWithSecret
	if err := json.Unmarshal(raw, &rotated); status != http.StatusOK || err != nil || rotated.Secret == created.Secret || !rotated.SecretRotatedAt.After(created.SecretRotatedAt) {
		t.Errorf("rotate: %d %s", status, raw)
	}
	sealed, err := store.New(pool).WebhookSubscriptionSecret(ctx, created.Id)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := sealer.Open(created.Id, sealed); err != nil || opened.Reveal() != rotated.Secret {
		t.Errorf("rotated secret does not open to the returned one: %v", err)
	}

	status, _, raw = callJSON(t, http.MethodGet, base, owner, "")
	if err := json.Unmarshal(raw, &list); status != http.StatusOK || err != nil || len(list.Subscriptions) != 1 || list.Subscriptions[0].Status != v1.WebhookSubscriptionPaused || strings.Contains(string(raw), "secret\"") {
		t.Errorf("owner list: %d %s", status, raw)
	}

	if status, _, raw := callJSON(t, http.MethodDelete, one, owner, ""); status != http.StatusNoContent {
		t.Errorf("delete: %d %s", status, raw)
	}
	if status, _, _ := callJSON(t, http.MethodGet, one, owner, ""); status != http.StatusNotFound {
		t.Errorf("get after delete: %d", status)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'free' WHERE id = $1`, ownerID); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := callJSON(t, http.MethodGet, base, owner, ""); status != http.StatusPaymentRequired {
		t.Errorf("after downgrading: %d, want 402", status)
	}
}
