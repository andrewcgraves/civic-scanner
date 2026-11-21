package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

type webhookCalls struct {
	subs      []store.WebhookSubscription
	created   []store.WebhookSubscriptionInput
	updates   []store.WebhookSubscriptionUpdate
	rotated   [][]byte
	deleted   []uuid.UUID
	err       error
	updateErr error
}

func (c *webhookCalls) find(userID, id uuid.UUID) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	for i, s := range c.subs {
		if s.ID == id && s.UserID == userID {
			return i, nil
		}
	}
	return 0, store.ErrNotFound
}

func (f fakeStore) ListWebhookSubscriptions(_ context.Context, userID uuid.UUID) ([]store.WebhookSubscription, error) {
	if f.webhooks == nil {
		return nil, errors.New("unexpected ListWebhookSubscriptions")
	}
	var out []store.WebhookSubscription
	for _, s := range f.webhooks.subs {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, f.webhooks.err
}

func (f fakeStore) CreateWebhookSubscription(_ context.Context, in store.WebhookSubscriptionInput) (store.WebhookSubscription, error) {
	if f.webhooks == nil {
		return store.WebhookSubscription{}, errors.New("unexpected CreateWebhookSubscription")
	}
	f.webhooks.created = append(f.webhooks.created, in)
	if f.webhooks.err != nil {
		return store.WebhookSubscription{}, f.webhooks.err
	}
	now := time.Date(2026, 11, 20, 1, 2, 3, 0, time.FixedZone("PST", -8*3600))
	sub := store.WebhookSubscription{
		ID: in.ID, UserID: in.UserID, AgencyID: uuid.New(), AgencySlug: in.AgencySlug, AgencyName: "Agency",
		URL: in.URL, PayloadVersion: in.PayloadVersion, Status: store.WebhookActive,
		SecretRotatedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	f.webhooks.subs = append(f.webhooks.subs, sub)
	return sub, nil
}

func (f fakeStore) GetWebhookSubscription(_ context.Context, userID, id uuid.UUID) (store.WebhookSubscription, error) {
	if f.webhooks == nil {
		return store.WebhookSubscription{}, errors.New("unexpected GetWebhookSubscription")
	}
	i, err := f.webhooks.find(userID, id)
	if err != nil {
		return store.WebhookSubscription{}, err
	}
	return f.webhooks.subs[i], nil
}

func (f fakeStore) UpdateWebhookSubscription(_ context.Context, userID, id uuid.UUID, update store.WebhookSubscriptionUpdate) (store.WebhookSubscription, error) {
	if f.webhooks == nil {
		return store.WebhookSubscription{}, errors.New("unexpected UpdateWebhookSubscription")
	}
	f.webhooks.updates = append(f.webhooks.updates, update)
	if f.webhooks.updateErr != nil {
		return store.WebhookSubscription{}, f.webhooks.updateErr
	}
	i, err := f.webhooks.find(userID, id)
	if err != nil {
		return store.WebhookSubscription{}, err
	}
	sub := &f.webhooks.subs[i]
	if update.URL != nil {
		sub.URL = *update.URL
	}
	if update.PayloadVersion != nil {
		sub.PayloadVersion = *update.PayloadVersion
	}
	if update.Status != nil {
		sub.Status = *update.Status
	}
	return *sub, nil
}

func (f fakeStore) RotateWebhookSubscriptionSecret(_ context.Context, userID, id uuid.UUID, ciphertext []byte) (store.WebhookSubscription, error) {
	if f.webhooks == nil {
		return store.WebhookSubscription{}, errors.New("unexpected RotateWebhookSubscriptionSecret")
	}
	f.webhooks.rotated = append(f.webhooks.rotated, ciphertext)
	i, err := f.webhooks.find(userID, id)
	if err != nil {
		return store.WebhookSubscription{}, err
	}
	return f.webhooks.subs[i], nil
}

func (f fakeStore) DeleteWebhookSubscription(_ context.Context, userID, id uuid.UUID) error {
	if f.webhooks == nil {
		return errors.New("unexpected DeleteWebhookSubscription")
	}
	f.webhooks.deleted = append(f.webhooks.deleted, id)
	i, err := f.webhooks.find(userID, id)
	if err != nil {
		return err
	}
	f.webhooks.subs = slices.Delete(f.webhooks.subs, i, i+1)
	return nil
}

func webhookFlags(flags ...string) func(context.Context, string) bool {
	return func(_ context.Context, flag string) bool { return slices.Contains(flags, flag) }
}

type webhookResolver struct {
	answers map[string][]string
	asked   *[]string
}

func (r webhookResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if r.asked != nil {
		*r.asked = append(*r.asked, host)
	}
	answer, ok := r.answers[host]
	if !ok {
		if strings.HasPrefix(host, "missing.") {
			return nil, errors.New("no such host")
		}
		answer = []string{"93.184.216.34"}
	}
	out := make([]netip.Addr, 0, len(answer))
	for _, a := range answer {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

var privateAnswers = map[string][]string{
	"internal.example.com": {"10.0.0.5"},
	"metadata.example.com": {"93.184.216.34", "169.254.169.254"},
}

func withResolver(r webhookResolver) HandlerOption {
	return WithWebhookAddressCheck(webhooks.AddressCheck{Resolver: r})
}

func testWebhookSealer(t *testing.T) *webhooks.Sealer {
	t.Helper()
	s, err := webhooks.NewSealer(config.NewSecret(strings.Repeat("k", config.WebhookSecretKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type webhookEnv struct {
	h      *Handler
	user   store.User
	users  *userCalls
	calls  *webhookCalls
	sealer *webhooks.Sealer
	logs   *bytes.Buffer
}

func newWebhookEnv(t *testing.T, plan store.Plan, flags func(context.Context, string) bool, opts ...HandlerOption) webhookEnv {
	t.Helper()
	user := store.User{ID: uuid.New(), WorkOSUserID: "user_01", Plan: plan}
	users := &userCalls{user: user}
	calls := &webhookCalls{}
	sealer := testWebhookSealer(t)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	all := append([]HandlerOption{WithAuthenticator(goodAuth()), WithFlags(flags), WithWebhookSealer(sealer), withResolver(webhookResolver{answers: privateAnswers})}, opts...)
	h := NewHandler(fakeStore{users: users, webhooks: calls}, logger, "test", all...)
	return webhookEnv{h: h, user: user, users: users, calls: calls, sealer: sealer, logs: logs}
}

func (e webhookEnv) add(t *testing.T, userID uuid.UUID) store.WebhookSubscription {
	t.Helper()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sub := store.WebhookSubscription{
		ID: uuid.New(), UserID: userID, AgencyID: uuid.New(), AgencySlug: "la-metro", AgencyName: "LA Metro",
		URL: "https://hooks.example.com/" + uuid.NewString(), PayloadVersion: "v1", Status: store.WebhookActive,
		SecretRotatedAt: at, CreatedAt: at, UpdatedAt: at.Add(time.Hour),
	}
	e.calls.subs = append(e.calls.subs, sub)
	return sub
}

func (e webhookEnv) do(t *testing.T, method, path, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	NewRouter(e.h, slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)
	return rec
}

type webhookRoute struct {
	name   string
	method string
	path   string
	body   string
}

func webhookRoutes(id uuid.UUID) []webhookRoute {
	one := "/v1/webhook-subscriptions/" + id.String()
	return []webhookRoute{
		{"list", http.MethodGet, "/v1/webhook-subscriptions", ""},
		{"create", http.MethodPost, "/v1/webhook-subscriptions", `{"agency":"la-metro","url":"https://hooks.example.com/x"}`},
		{"get", http.MethodGet, one, ""},
		{"update", http.MethodPatch, one, `{"status":"paused"}`},
		{"delete", http.MethodDelete, one, ""},
		{"rotate", http.MethodPost, one + "/secret", ""},
	}
}

func TestWebhookRoutesAreGated(t *testing.T) {
	both := webhookFlags(FlagAccounts, FlagWebhookSubscriptions)
	for _, tc := range []struct {
		name          string
		plan          store.Plan
		flags         func(context.Context, string) bool
		authorization string
		status        int
		code          string
		userLoaded    bool
	}{
		{"no token", store.PlanPaid, both, "", http.StatusUnauthorized, "unauthorized", false},
		{"bad token", store.PlanPaid, both, "Bearer nope", http.StatusUnauthorized, "unauthorized", false},
		{"accounts off", store.PlanPaid, webhookFlags(FlagWebhookSubscriptions), "Bearer good", http.StatusNotFound, "not_found", false},
		{"webhooks off", store.PlanPaid, webhookFlags(FlagAccounts), "Bearer good", http.StatusNotFound, "not_found", false},
		{"free plan", store.PlanFree, both, "Bearer good", http.StatusPaymentRequired, "payment_required", true},
	} {
		env := newWebhookEnv(t, tc.plan, tc.flags)
		sub := env.add(t, env.user.ID)
		for _, route := range webhookRoutes(sub.ID) {
			rec := env.do(t, route.method, route.path, tc.authorization, route.body)
			if rec.Code != tc.status || decodeError(t, rec).Code != tc.code {
				t.Errorf("%s, %s: status %d, body %s; want %d %s", tc.name, route.name, rec.Code, rec.Body, tc.status, tc.code)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s, %s: Cache-Control = %q", tc.name, route.name, rec.Header().Get("Cache-Control"))
			}
		}
		if (len(env.users.inputs) > 0) != tc.userLoaded {
			t.Errorf("%s: user loaded %d times", tc.name, len(env.users.inputs))
		}
		if len(env.calls.created)+len(env.calls.updates)+len(env.calls.rotated)+len(env.calls.deleted) != 0 || len(env.calls.subs) != 1 {
			t.Errorf("%s: the store was changed: %+v", tc.name, env.calls)
		}
	}
}

func TestWebhookMalformedBodiesAreGatedFirst(t *testing.T) {
	both := webhookFlags(FlagAccounts, FlagWebhookSubscriptions)
	for _, tc := range []struct {
		name          string
		plan          store.Plan
		flags         func(context.Context, string) bool
		authorization string
		status        int
		code          string
	}{
		{"no token", store.PlanPaid, both, "", http.StatusUnauthorized, "unauthorized"},
		{"flag off", store.PlanPaid, webhookFlags(FlagAccounts), "Bearer good", http.StatusNotFound, "not_found"},
		{"free plan", store.PlanFree, both, "Bearer good", http.StatusPaymentRequired, "payment_required"},
		{"paid", store.PlanPaid, both, "Bearer good", http.StatusBadRequest, "invalid_body"},
	} {
		env := newWebhookEnv(t, tc.plan, tc.flags)
		for _, route := range []webhookRoute{
			{"create", http.MethodPost, "/v1/webhook-subscriptions", `{"agency":`},
			{"update", http.MethodPatch, "/v1/webhook-subscriptions/" + uuid.NewString(), `["status"]`},
			{"create with a number", http.MethodPost, "/v1/webhook-subscriptions", `{"agency":1,"url":"https://x.example"}`},
		} {
			rec := env.do(t, route.method, route.path, tc.authorization, route.body)
			if rec.Code != tc.status || decodeError(t, rec).Code != tc.code || rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s, %s: status %d, body %s, Cache-Control %q; want %d %s", tc.name, route.name, rec.Code, rec.Body, rec.Header().Get("Cache-Control"), tc.status, tc.code)
			}
		}
	}
}

func decodeWebhookWithSecret(t *testing.T, rec *httptest.ResponseRecorder) (v1.WebhookSubscriptionWithSecret, map[string]any) {
	t.Helper()
	var sub v1.WebhookSubscriptionWithSecret
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &sub); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	return sub, raw
}

func TestCreateWebhookSubscription(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good",
		`{"agency":" la-metro ","url":" HTTPS://Hooks.Example.com/In?Token=AbC ","unknown":true}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q, body %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	got, raw := decodeWebhookWithSecret(t, rec)
	if len(env.calls.created) != 1 {
		t.Fatalf("created %d", len(env.calls.created))
	}
	in := env.calls.created[0]
	if in.UserID != env.user.ID || in.AgencySlug != "la-metro" || in.URL != "https://hooks.example.com/In?Token=AbC" || in.PayloadVersion != "v1" || in.ID == uuid.Nil {
		t.Errorf("store input = %+v", in)
	}
	if got.Id != in.ID || got.Agency != "la-metro" || got.Url != in.URL || got.PayloadVersion != v1.WebhookPayloadV1 || got.Status != v1.WebhookSubscriptionActive {
		t.Errorf("response = %+v", got)
	}
	if got.CreatedAt.Location() != time.UTC || !got.CreatedAt.Equal(time.Date(2026, 11, 20, 9, 2, 3, 0, time.UTC)) {
		t.Errorf("createdAt = %v, want UTC", got.CreatedAt)
	}
	if !strings.HasPrefix(got.Secret, webhooks.SecretPrefix) {
		t.Errorf("secret = %q", got.Secret)
	}
	if bytes.Contains(in.SecretCiphertext, []byte(got.Secret)) {
		t.Error("the secret was stored in plain text")
	}
	opened, err := env.sealer.Open(in.ID, in.SecretCiphertext)
	if err != nil || opened.Reveal() != got.Secret {
		t.Errorf("the stored secret does not open to the returned one: %v", err)
	}
	keys := []string{
		"id", "agency", "url", "payloadVersion", "status", "pausedReason", "pausedAt", "consecutiveFailedEvents",
		"createdAt", "updatedAt", "secretRotatedAt", "secret",
	}
	for _, key := range keys {
		if _, ok := raw[key]; !ok {
			t.Errorf("response has no %q", key)
		}
	}
	if raw["pausedReason"] != nil || raw["pausedAt"] != nil || raw["consecutiveFailedEvents"] != float64(0) {
		t.Errorf("a new subscription is not paused: %v", raw)
	}
	if len(raw) != 12 {
		t.Errorf("response has extra members: %v", raw)
	}
	if strings.Contains(env.logs.String(), got.Secret) || strings.Contains(env.logs.String(), "Token=AbC") {
		t.Errorf("the logs hold the secret or the URL: %s", env.logs)
	}
	if !strings.Contains(env.logs.String(), "webhook subscription created") {
		t.Errorf("logs = %s", env.logs)
	}
}

func TestCreateWebhookSubscriptionRefuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		storeErr error
		status   int
		code     string
		stored   bool
	}{
		{"no agency", `{"url":"https://hooks.example.com"}`, nil, http.StatusBadRequest, "unknown_agency", false},
		{"blank agency", `{"agency":"  ","url":"https://hooks.example.com"}`, nil, http.StatusBadRequest, "unknown_agency", false},
		{"no url", `{"agency":"la-metro"}`, nil, http.StatusBadRequest, "required", false},
		{"http url", `{"agency":"la-metro","url":"http://hooks.example.com"}`, nil, http.StatusBadRequest, "unsupported_scheme", false},
		{"relative url", `{"agency":"la-metro","url":"hooks.example.com"}`, nil, http.StatusBadRequest, "invalid_url", false},
		{"credentials", `{"agency":"la-metro","url":"https://a:b@hooks.example.com"}`, nil, http.StatusBadRequest, "credentials", false},
		{"fragment", `{"agency":"la-metro","url":"https://hooks.example.com/#x"}`, nil, http.StatusBadRequest, "fragment", false},
		{"too long", `{"agency":"la-metro","url":"https://hooks.example.com/` + strings.Repeat("a", 2048) + `"}`, nil, http.StatusBadRequest, "too_long", false},
		{"version", `{"agency":"la-metro","url":"https://hooks.example.com","payloadVersion":"v2"}`, nil, http.StatusBadRequest, "invalid_payload_version", false},
		{"empty version", `{"agency":"la-metro","url":"https://hooks.example.com","payloadVersion":""}`, nil, http.StatusBadRequest, "invalid_payload_version", false},
		{"unknown agency", `{"agency":"nowhere","url":"https://hooks.example.com"}`, store.ErrUnknownAgency, http.StatusBadRequest, "unknown_agency", true},
		{"duplicate", `{"agency":"la-metro","url":"https://hooks.example.com"}`, store.ErrWebhookSubscriptionExists, http.StatusConflict, "already_subscribed", true},
		{"limit", `{"agency":"la-metro","url":"https://hooks.example.com"}`, store.ErrWebhookSubscriptionLimit, http.StatusTooManyRequests, "subscription_limit_reached", true},
		{"database", `{"agency":"la-metro","url":"https://hooks.example.com"}`, errors.New("database down"), http.StatusInternalServerError, "internal_error", true},
	} {
		env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
		env.calls.err = tc.storeErr
		rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", tc.body)
		if rec.Code != tc.status || decodeError(t, rec).Code != tc.code || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status %d, body %s; want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
		if (len(env.calls.created) > 0) != tc.stored {
			t.Errorf("%s: store called %d times", tc.name, len(env.calls.created))
		}
		if strings.Contains(rec.Body.String(), webhooks.SecretPrefix) {
			t.Errorf("%s: a refusal carries a secret: %s", tc.name, rec.Body)
		}
	}
}

func TestCreateWebhookSubscriptionRefusesNonPublicEndpoints(t *testing.T) {
	for url, code := range map[string]string{
		"https://127.0.0.1/hook":            "not_public",
		"https://10.0.0.1:8443/hook":        "not_public",
		"https://169.254.169.254/latest":    "not_public",
		"https://[::1]/hook":                "not_public",
		"https://[fd12::1]/hook":            "not_public",
		"https://localhost/hook":            "not_public",
		"https://2130706433/hook":           "not_public",
		"https://internal.example.com/hook": "not_public",
		"https://metadata.example.com/hook": "not_public",
		"https://missing.example.com/hook":  "unresolvable_host",
	} {
		env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
		rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", `{"agency":"la-metro","url":"`+url+`?token=SeCrEt"}`)
		problem := decodeError(t, rec)
		if rec.Code != http.StatusBadRequest || problem.Code != code || problem.Message == "" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status %d, body %s; want 400 %s", url, rec.Code, rec.Body, code)
		}
		if len(env.calls.created) != 0 {
			t.Errorf("%s: stored", url)
		}
		if !strings.Contains(env.logs.String(), "webhook endpoint refused") || strings.Contains(env.logs.String(), "SeCrEt") {
			t.Errorf("%s: logs = %s", url, env.logs)
		}
	}
}

func TestCreateWebhookSubscriptionChecksTheAddressAfterTheBody(t *testing.T) {
	var asked []string
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions), withResolver(webhookResolver{asked: &asked}))
	for _, body := range []string{
		`{"agency":"","url":"https://hooks.example.com"}`,
		`{"agency":"la-metro","url":"http://hooks.example.com"}`,
		`{"agency":"la-metro","url":"https://hooks.example.com","payloadVersion":"v9"}`,
	} {
		if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
	if len(asked) != 0 {
		t.Errorf("resolved %v for bodies that were refused anyway", asked)
	}
	if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", `{"agency":"la-metro","url":"https://Hooks.Example.com/x"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body)
	}
	if !slices.Equal(asked, []string{"hooks.example.com"}) {
		t.Errorf("asked = %v", asked)
	}
}

func TestUpdateWebhookSubscriptionChecksOnlyANewURL(t *testing.T) {
	var asked []string
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions), withResolver(webhookResolver{asked: &asked, answers: map[string][]string{"hooks.example.com": {"10.0.0.9"}}}))
	mine := env.add(t, env.user.ID)
	path := "/v1/webhook-subscriptions/" + mine.ID.String()

	for _, body := range []string{`{"status":"paused"}`, `{"status":"paused","url":"` + mine.URL + `"}`, `{"url":" ` + strings.Replace(mine.URL, "hooks.example.com", "HOOKS.example.com", 1) + `"}`} {
		if rec := env.do(t, http.MethodPatch, path, "Bearer good", body); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, body %s", body, rec.Code, rec.Body)
		}
	}
	if len(asked) != 0 {
		t.Errorf("checked an unchanged URL: %v", asked)
	}
	rec := env.do(t, http.MethodPatch, path, "Bearer good", `{"status":"active","url":"https://hooks.example.com/other"}`)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "not_public" {
		t.Errorf("move to a private host: status %d, body %s", rec.Code, rec.Body)
	}
	if len(env.calls.updates) != 3 || env.calls.subs[0].URL != mine.URL || env.calls.subs[0].Status != store.WebhookPaused {
		t.Errorf("a refused update changed the subscription: %+v", env.calls.subs[0])
	}
}

func TestWebhookSecretsNeedAKey(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions), WithWebhookSealer(nil))
	sub := env.add(t, env.user.ID)
	for _, route := range []webhookRoute{webhookRoutes(sub.ID)[1], webhookRoutes(sub.ID)[5]} {
		rec := env.do(t, route.method, route.path, "Bearer good", route.body)
		if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Code != "webhooks_not_configured" {
			t.Errorf("%s: status %d, body %s", route.name, rec.Code, rec.Body)
		}
	}
	if len(env.calls.created)+len(env.calls.rotated) != 0 {
		t.Error("the store was called without a key")
	}
	for _, route := range []webhookRoute{webhookRoutes(sub.ID)[0], webhookRoutes(sub.ID)[2], webhookRoutes(sub.ID)[3], webhookRoutes(sub.ID)[4]} {
		if rec := env.do(t, route.method, route.path, "Bearer good", route.body); rec.Code >= 300 {
			t.Errorf("%s without a key: status %d, body %s", route.name, rec.Code, rec.Body)
		}
	}
}

func TestListWebhookSubscriptionsShowsOnlyTheUsersOwn(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	mine := env.add(t, env.user.ID)
	env.add(t, uuid.New())
	second := env.add(t, env.user.ID)

	rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions", "Bearer good", "")
	var list v1.WebhookSubscriptionList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != http.StatusOK || err != nil || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if len(list.Subscriptions) != 2 || list.Subscriptions[0].Id != mine.ID || list.Subscriptions[1].Id != second.ID {
		t.Fatalf("list = %+v", list)
	}
	got := list.Subscriptions[0]
	if got.Agency != "la-metro" || got.Url != mine.URL || got.Status != v1.WebhookSubscriptionActive || !got.UpdatedAt.Equal(mine.UpdatedAt) || !got.SecretRotatedAt.Equal(mine.SecretRotatedAt) {
		t.Errorf("item = %+v", got)
	}
	if strings.Contains(rec.Body.String(), "secret\"") {
		t.Errorf("the list carries a secret: %s", rec.Body)
	}

	env.calls.subs = nil
	rec = env.do(t, http.MethodGet, "/v1/webhook-subscriptions", "Bearer good", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"subscriptions":[]}` {
		t.Errorf("empty list: status %d, body %s", rec.Code, rec.Body)
	}
	env.calls.err = errors.New("database down")
	if rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions", "Bearer good", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("database error: status %d", rec.Code)
	}
}

func TestGetWebhookSubscription(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	mine := env.add(t, env.user.ID)
	theirs := env.add(t, uuid.New())

	rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+mine.ID.String(), "Bearer good", "")
	var got v1.WebhookSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Id != mine.ID || strings.Contains(rec.Body.String(), "secret\"") {
		t.Errorf("own: status %d, body %s", rec.Code, rec.Body)
	}
	for name, path := range map[string]string{"another user's": theirs.ID.String(), "unknown": uuid.NewString()} {
		rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+path, "Bearer good", "")
		if rec.Code != http.StatusNotFound || decodeError(t, rec).Code != "not_found" {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/not-a-uuid", "Bearer good", ""); rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_parameter" {
		t.Errorf("bad id: status %d, body %s", rec.Code, rec.Body)
	}
	env.calls.err = errors.New("database down")
	if rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+mine.ID.String(), "Bearer good", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("database error: status %d", rec.Code)
	}
}

func TestUpdateWebhookSubscription(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	mine := env.add(t, env.user.ID)
	path := "/v1/webhook-subscriptions/" + mine.ID.String()

	rec := env.do(t, http.MethodPatch, path, "Bearer good", `{"status":"paused"}`)
	var got v1.WebhookSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Status != v1.WebhookSubscriptionPaused || got.Url != mine.URL {
		t.Fatalf("pause: status %d, body %s", rec.Code, rec.Body)
	}
	update := env.calls.updates[0]
	if update.Status == nil || *update.Status != store.WebhookPaused || update.URL != nil || update.PayloadVersion != nil {
		t.Errorf("pause update = %+v", update)
	}

	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"status":"active","url":"HTTPS://New.Example.com/h","payloadVersion":"v1"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Status != v1.WebhookSubscriptionActive || got.Url != "https://new.example.com/h" {
		t.Fatalf("resume and move: status %d, body %s", rec.Code, rec.Body)
	}
	if update := env.calls.updates[1]; *update.URL != "https://new.example.com/h" || *update.PayloadVersion != "v1" {
		t.Errorf("update = %+v", update)
	}

	for _, tc := range []struct {
		name      string
		path      string
		body      string
		storeErr  error
		updateErr error
		status    int
		code      string
	}{
		{"empty", path, `{}`, nil, nil, http.StatusBadRequest, "nothing_to_update"},
		{"nulls", path, `{"status":null,"url":null}`, nil, nil, http.StatusBadRequest, "nothing_to_update"},
		{"bad status", path, `{"status":"disabled"}`, nil, nil, http.StatusBadRequest, "invalid_status"},
		{"bad version", path, `{"payloadVersion":"v0"}`, nil, nil, http.StatusBadRequest, "invalid_payload_version"},
		{"bad url", path, `{"url":"http://x.example"}`, nil, nil, http.StatusBadRequest, "unsupported_scheme"},
		{"empty url", path, `{"url":""}`, nil, nil, http.StatusBadRequest, "required"},
		{"private ip", path, `{"url":"https://10.0.0.1/h"}`, nil, nil, http.StatusBadRequest, "not_public"},
		{"private name", path, `{"url":"https://internal.example.com/h"}`, nil, nil, http.StatusBadRequest, "not_public"},
		{"unresolvable", path, `{"url":"https://missing.example.com/h"}`, nil, nil, http.StatusBadRequest, "unresolvable_host"},
		{"another user's", "/v1/webhook-subscriptions/" + env.add(t, uuid.New()).ID.String(), `{"status":"paused"}`, nil, nil, http.StatusNotFound, "not_found"},
		{"another user's url", "/v1/webhook-subscriptions/" + env.add(t, uuid.New()).ID.String(), `{"url":"https://new.example.com/x"}`, nil, nil, http.StatusNotFound, "not_found"},
		{"duplicate", path, `{"url":"https://dup.example"}`, nil, store.ErrWebhookSubscriptionExists, http.StatusConflict, "already_subscribed"},
		{"database", path, `{"status":"paused"}`, errors.New("database down"), nil, http.StatusInternalServerError, "internal_error"},
		{"database reading the url", path, `{"url":"https://new.example.com/x"}`, errors.New("database down"), nil, http.StatusInternalServerError, "internal_error"},
	} {
		before := len(env.calls.updates)
		env.calls.err = tc.storeErr
		env.calls.updateErr = tc.updateErr
		rec := env.do(t, http.MethodPatch, tc.path, "Bearer good", tc.body)
		env.calls.err = nil
		env.calls.updateErr = nil
		if rec.Code != tc.status || decodeError(t, rec).Code != tc.code {
			t.Errorf("%s: status %d, body %s; want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
		wantCalled := tc.status != http.StatusBadRequest && tc.name != "another user's url" && tc.name != "database reading the url"
		if called := len(env.calls.updates) > before; called != wantCalled {
			t.Errorf("%s: store called %v", tc.name, called)
		}
	}
	if env.calls.subs[0].Status != store.WebhookActive {
		t.Errorf("a refused update changed the subscription: %+v", env.calls.subs[0])
	}
}

func TestDeleteWebhookSubscription(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	mine := env.add(t, env.user.ID)
	theirs := env.add(t, uuid.New())

	if rec := env.do(t, http.MethodDelete, "/v1/webhook-subscriptions/"+theirs.ID.String(), "Bearer good", ""); rec.Code != http.StatusNotFound {
		t.Errorf("another user's: status %d", rec.Code)
	}
	rec := env.do(t, http.MethodDelete, "/v1/webhook-subscriptions/"+mine.ID.String(), "Bearer good", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("delete: status %d, body %q", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodDelete, "/v1/webhook-subscriptions/"+mine.ID.String(), "Bearer good", ""); rec.Code != http.StatusNotFound || decodeError(t, rec).Code != "not_found" {
		t.Errorf("again: status %d, body %s", rec.Code, rec.Body)
	}
	if len(env.calls.subs) != 1 || env.calls.subs[0].ID != theirs.ID {
		t.Errorf("subs = %+v", env.calls.subs)
	}
	env.calls.err = errors.New("database down")
	if rec := env.do(t, http.MethodDelete, "/v1/webhook-subscriptions/"+theirs.ID.String(), "Bearer good", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("database error: status %d", rec.Code)
	}
}

func TestRotateWebhookSubscriptionSecret(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	mine := env.add(t, env.user.ID)
	theirs := env.add(t, uuid.New())
	path := "/v1/webhook-subscriptions/" + mine.ID.String() + "/secret"

	first, _ := decodeWebhookWithSecret(t, env.do(t, http.MethodPost, path, "Bearer good", ""))
	rec := env.do(t, http.MethodPost, path, "Bearer good", "")
	second, _ := decodeWebhookWithSecret(t, rec)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || second.Id != mine.ID {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(second.Secret, webhooks.SecretPrefix) || second.Secret == first.Secret {
		t.Errorf("secrets %q then %q", first.Secret, second.Secret)
	}
	opened, err := env.sealer.Open(mine.ID, env.calls.rotated[1])
	if err != nil || opened.Reveal() != second.Secret {
		t.Errorf("the stored secret does not open to the returned one: %v", err)
	}
	if strings.Contains(env.logs.String(), second.Secret) || !strings.Contains(env.logs.String(), "webhook secret rotated") {
		t.Errorf("logs = %s", env.logs)
	}
	for name, id := range map[string]uuid.UUID{"another user's": theirs.ID, "unknown": uuid.New()} {
		rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions/"+id.String()+"/secret", "Bearer good", "")
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), webhooks.SecretPrefix) {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	env.calls.err = errors.New("database down")
	if rec := env.do(t, http.MethodPost, path, "Bearer good", ""); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), webhooks.SecretPrefix) {
		t.Errorf("database error: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestWebhookHandlersFailClosedWithoutThePaidGate(t *testing.T) {
	h := NewHandler(fakeStore{webhooks: &webhookCalls{}}, slog.New(slog.DiscardHandler), "test", WithWebhookSealer(testWebhookSealer(t)))
	ctx := context.Background()
	id := uuid.New()
	body := v1.WebhookSubscriptionInput{Agency: "la-metro", Url: "https://hooks.example.com"}
	status := v1.WebhookSubscriptionPaused
	checks := map[string]func() (any, error){
		"list": func() (any, error) {
			return h.ListWebhookSubscriptions(ctx, v1.ListWebhookSubscriptionsRequestObject{})
		},
		"create": func() (any, error) {
			return h.CreateWebhookSubscription(ctx, v1.CreateWebhookSubscriptionRequestObject{Body: &body})
		},
		"get": func() (any, error) {
			return h.GetWebhookSubscription(ctx, v1.GetWebhookSubscriptionRequestObject{Id: id})
		},
		"update": func() (any, error) {
			return h.UpdateWebhookSubscription(ctx, v1.UpdateWebhookSubscriptionRequestObject{Id: id, Body: &v1.WebhookSubscriptionUpdate{Status: &status}})
		},
		"delete": func() (any, error) {
			return h.DeleteWebhookSubscription(ctx, v1.DeleteWebhookSubscriptionRequestObject{Id: id})
		},
		"rotate": func() (any, error) {
			return h.RotateWebhookSubscriptionSecret(ctx, v1.RotateWebhookSubscriptionSecretRequestObject{Id: id})
		},
	}
	for name, call := range checks {
		resp, err := call()
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		visit := map[string]func() error{
			"list": func() error {
				return resp.(v1.ListWebhookSubscriptionsResponseObject).VisitListWebhookSubscriptionsResponse(rec)
			},
			"create": func() error {
				return resp.(v1.CreateWebhookSubscriptionResponseObject).VisitCreateWebhookSubscriptionResponse(rec)
			},
			"get": func() error {
				return resp.(v1.GetWebhookSubscriptionResponseObject).VisitGetWebhookSubscriptionResponse(rec)
			},
			"update": func() error {
				return resp.(v1.UpdateWebhookSubscriptionResponseObject).VisitUpdateWebhookSubscriptionResponse(rec)
			},
			"delete": func() error {
				return resp.(v1.DeleteWebhookSubscriptionResponseObject).VisitDeleteWebhookSubscriptionResponse(rec)
			},
			"rotate": func() error {
				return resp.(v1.RotateWebhookSubscriptionSecretResponseObject).VisitRotateWebhookSubscriptionSecretResponse(rec)
			},
		}[name]
		if err := visit(); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusPaymentRequired || decodeError(t, rec).Code != "payment_required" {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestEveryWebhookOperationIsPaidAndFlagged(t *testing.T) {
	for _, op := range []string{"ListWebhookSubscriptions", "CreateWebhookSubscription", "GetWebhookSubscription", "UpdateWebhookSubscription", "DeleteWebhookSubscription", "RotateWebhookSubscriptionSecret"} {
		if flag, ok := paidOperations[op]; !ok || flag != FlagWebhookSubscriptions {
			t.Errorf("%s: paidOperations flag = %q, %v", op, flag, ok)
		}
	}
}

func TestAPIWebhookSubscriptionShowsWhyItIsPaused(t *testing.T) {
	pausedAt := time.Date(2026, 11, 19, 8, 0, 0, 0, time.FixedZone("PST", -8*3600))
	reason := store.WebhookPausedFailedDeliveries
	got := apiWebhookSubscription(store.WebhookSubscription{
		ID: uuid.New(), Status: store.WebhookPaused, PausedReason: &reason, PausedAt: &pausedAt, ConsecutiveFailedEvents: 5,
	})
	if got.Status != v1.WebhookSubscriptionPaused || got.PausedReason == nil || *got.PausedReason != v1.WebhookPausedFailedDeliveries ||
		got.PausedAt == nil || !got.PausedAt.Equal(pausedAt) || got.PausedAt.Location() != time.UTC || got.ConsecutiveFailedEvents != 5 {
		t.Errorf("paused subscription = %+v", got)
	}
	with := apiWebhookWithSecret(store.WebhookSubscription{Status: store.WebhookPaused, PausedReason: &reason, PausedAt: &pausedAt, ConsecutiveFailedEvents: 5}, "s")
	if with.PausedReason == nil || with.PausedAt == nil || with.ConsecutiveFailedEvents != 5 {
		t.Errorf("with secret = %+v", with)
	}
	if active := apiWebhookSubscription(store.WebhookSubscription{Status: store.WebhookActive}); active.PausedReason != nil || active.PausedAt != nil {
		t.Errorf("active subscription = %+v", active)
	}
}
