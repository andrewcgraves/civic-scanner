//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

func TestCustomWebhookTemplatesAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	sealer, err := webhooks.NewSealer(config.NewSecret(string(bytes.Repeat([]byte{7}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagWebhookSubscriptions}, flag)
	}
	s := newAuthServer(t, flags, httpapi.WithWebhookSealer(sealer), webhookAddresses)
	owner := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))
	slug := "civ55-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ($1, 'Metro "Transit" Ü', 'live', 'https://example.com')`, slug); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM agencies WHERE slug = $1`, slug)
	})
	if status, _, raw := callJSON(t, http.MethodGet, s.url+"/v1/webhook-subscriptions", owner, ""); status != http.StatusPaymentRequired {
		t.Fatalf("free user: %d %s", status, raw)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'paid' WHERE workos_user_id = $1`, s.users); err != nil {
		t.Fatal(err)
	}

	template := `{"text": "{{event.label}} at {{agency.name}}", "n": 3}`
	encoded, _ := json.Marshal(template)
	base := s.url + "/v1/webhook-subscriptions"
	status, _, raw := callJSON(t, http.MethodPost, base, owner, `{"agency":"`+slug+`","url":"https://hooks.example.com/custom","format":"custom","template":`+string(encoded)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var created v1.WebhookSubscriptionWithSecret
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Format != v1.WebhookFormatCustom || created.Template == nil || *created.Template != template {
		t.Errorf("created = %+v", created)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT template FROM webhook_subscriptions WHERE id = $1`, created.Id).Scan(&stored); err != nil || stored != template {
		t.Errorf("stored template = %q, %v", stored, err)
	}

	status, _, raw = callJSON(t, http.MethodPost, base, owner, `{"agency":"`+slug+`","url":"https://hooks.example.com/other","format":"custom","template":"{\"a\":\"{{agency}}\"}"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "unknown_placeholder") || !strings.Contains(string(raw), "{{agency}}") {
		t.Errorf("unknown placeholder: %d %s", status, raw)
	}
	status, _, raw = callJSON(t, http.MethodPatch, base+"/"+created.Id.String(), owner, `{"template":"{\"a\":"}`)
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid_template_json") {
		t.Errorf("invalid JSON: %d %s", status, raw)
	}
	if err := pool.QueryRow(ctx, `SELECT template FROM webhook_subscriptions WHERE id = $1`, created.Id).Scan(&stored); err != nil || stored != template {
		t.Errorf("a refused update changed the template: %q, %v", stored, err)
	}

	status, _, raw = callJSON(t, http.MethodPost, s.url+"/v1/webhook-templates/preview", owner,
		`{"agency":"`+slug+`","type":"transcript_ready","template":`+string(encoded)+`}`)
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, raw)
	}
	var preview v1.WebhookTemplatePreview
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Type != v1.WebhookTranscriptReady || preview.Body != `{"text":"Test: Transcript ready at Metro \"Transit\" Ü","n":3}` {
		t.Errorf("preview = %+v", preview)
	}
}
