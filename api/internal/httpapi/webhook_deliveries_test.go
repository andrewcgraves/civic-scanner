package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks/webhookstest"
)

func (f fakeStore) WebhookSubscriptionSecret(_ context.Context, id uuid.UUID) ([]byte, error) {
	if f.webhooks == nil {
		return nil, errors.New("unexpected WebhookSubscriptionSecret")
	}
	if f.webhooks.secretErr != nil {
		return nil, f.webhooks.secretErr
	}
	sealed, ok := f.webhooks.secrets[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return sealed, nil
}

func (f fakeStore) ListWebhookDeliveries(_ context.Context, userID, subscriptionID uuid.UUID, params store.ListWebhookDeliveriesParams) (store.WebhookDeliveryPage, error) {
	if f.webhooks == nil {
		return store.WebhookDeliveryPage{}, errors.New("unexpected ListWebhookDeliveries")
	}
	f.webhooks.deliveryParams = append(f.webhooks.deliveryParams, params)
	if f.webhooks.deliveriesErr != nil {
		return store.WebhookDeliveryPage{}, f.webhooks.deliveriesErr
	}
	if _, err := f.webhooks.find(userID, subscriptionID); err != nil {
		return store.WebhookDeliveryPage{}, err
	}
	return f.webhooks.deliveries, nil
}

type fakeWebhookSender struct {
	mu       sync.Mutex
	requests []webhooks.Request
	result   webhooks.Result
}

func (s *fakeWebhookSender) Send(_ context.Context, req webhooks.Request) webhooks.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	return s.result
}

func (s *fakeWebhookSender) sent() []webhooks.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]webhooks.Request(nil), s.requests...)
}

var bothWebhookFlags = webhookFlags(FlagAccounts, FlagWebhookSubscriptions)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func sealFor(t *testing.T, env webhookEnv, sub store.WebhookSubscription) string {
	t.Helper()
	secret := webhooks.NewSecret()
	sealed, err := env.sealer.Seal(sub.ID, secret)
	if err != nil {
		t.Fatal(err)
	}
	if env.calls.secrets == nil {
		env.calls.secrets = map[uuid.UUID][]byte{}
	}
	env.calls.secrets[sub.ID] = sealed
	return secret.Reveal()
}

func decodeTestResult(t *testing.T, rec *httptest.ResponseRecorder) (v1.WebhookTestResult, map[string]any) {
	t.Helper()
	var result v1.WebhookTestResult
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	return result, raw
}

func TestListWebhookDeliveriesMapsTheLog(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	sub := env.add(t, env.user.ID)
	pst := time.FixedZone("PST", -8*3600)
	first := time.Date(2026, 11, 20, 1, 0, 0, 0, pst)
	status500, status204 := 500, 204
	timeout := "the endpoint did not answer in time"
	eventID, meetingID := uuid.New(), uuid.New()
	env.calls.deliveries = store.WebhookDeliveryPage{
		Deliveries: []store.WebhookDeliveryLogEntry{{
			EventID: eventID, EventType: store.EventAgendaChanged,
			Meeting:        store.Meeting{ID: meetingID, Title: "Board", StartsAt: time.Date(2026, 12, 1, 10, 0, 0, 0, pst), SourceURL: "https://agency.example/m"},
			FirstAttemptAt: first, LastAttemptAt: first.Add(time.Minute), Succeeded: true,
			Attempts: []store.WebhookAttempt{
				{Attempt: 1, StartedAt: first, Duration: 1500 * time.Millisecond, StatusCode: &status500},
				{Attempt: 2, StartedAt: first.Add(16 * time.Second), Duration: 15 * time.Second, Error: &timeout},
				{Attempt: 3, StartedAt: first.Add(time.Minute), Duration: 40 * time.Millisecond, StatusCode: &status204, Succeeded: true},
			},
		}, {
			EventID: uuid.New(), EventType: store.EventTranscriptReady, Meeting: store.Meeting{ID: uuid.New(), Title: "Committee", StartsAt: first},
			FirstAttemptAt: first, LastAttemptAt: first, Attempts: []store.WebhookAttempt{{Attempt: 1, StartedAt: first, StatusCode: &status500}},
		}},
		NextCursor: "next-page",
	}

	rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+sub.ID.String()+"/deliveries?limit=2&cursor=abc", "Bearer good", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q, body %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	if got := env.calls.deliveryParams; len(got) != 1 || got[0] != (store.ListWebhookDeliveriesParams{Cursor: "abc", Limit: 2}) {
		t.Errorf("store params = %+v", got)
	}
	var list v1.WebhookDeliveryList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.NextCursor == nil || *list.NextCursor != "next-page" || len(list.Deliveries) != 2 {
		t.Fatalf("list = %+v", list)
	}
	d := list.Deliveries[0]
	if d.EventId != eventID || d.Type != v1.WebhookAgendaChanged || !d.Succeeded || d.Meeting.Id != meetingID || d.Meeting.Title != "Board" ||
		d.Meeting.SourceUrl == nil || *d.Meeting.SourceUrl != "https://agency.example/m" || d.Meeting.StartsAt.Location() != time.UTC {
		t.Errorf("delivery = %+v", d)
	}
	if d.FirstAttemptAt.Location() != time.UTC || !d.FirstAttemptAt.Equal(first) || !d.LastAttemptAt.Equal(first.Add(time.Minute)) || len(d.Attempts) != 3 {
		t.Errorf("delivery times = %+v", d)
	}
	if a := d.Attempts[0]; a.Attempt != 1 || a.DurationMs != 1500 || a.StatusCode == nil || *a.StatusCode != 500 || a.Error != nil || a.Succeeded {
		t.Errorf("attempt 1 = %+v", a)
	}
	if a := d.Attempts[1]; a.StatusCode != nil || a.Error == nil || *a.Error != timeout || a.DurationMs != 15000 {
		t.Errorf("attempt 2 = %+v", a)
	}
	if a := d.Attempts[2]; !a.Succeeded || a.StartedAt.Location() != time.UTC {
		t.Errorf("attempt 3 = %+v", a)
	}
	if list.Deliveries[1].Meeting.SourceUrl != nil {
		t.Errorf("a meeting without a source URL has one: %+v", list.Deliveries[1].Meeting)
	}
	var raw struct {
		Deliveries []struct {
			Attempts []map[string]any `json:"attempts"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	attempt := raw.Deliveries[0].Attempts[0]
	if v, ok := attempt["error"]; !ok || v != nil {
		t.Errorf("error is not an explicit null: %v", attempt)
	}
}

func TestListWebhookDeliveriesLastPageAndEmptyLog(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	sub := env.add(t, env.user.ID)
	rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+sub.ID.String()+"/deliveries", "Bearer good", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deliveries":[],"nextCursor":null}` {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if got := env.calls.deliveryParams; len(got) != 1 || got[0] != (store.ListWebhookDeliveriesParams{}) {
		t.Errorf("store params = %+v", got)
	}
}

func TestListWebhookDeliveriesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		other  bool
		err    error
		status int
		code   string
		stored bool
	}{
		{"limit 0", "?limit=0", false, nil, http.StatusBadRequest, "invalid_parameter", false},
		{"limit too big", "?limit=101", false, nil, http.StatusBadRequest, "invalid_parameter", false},
		{"limit not a number", "?limit=ten", false, nil, http.StatusBadRequest, "invalid_parameter", false},
		{"bad cursor", "?cursor=x", false, store.ErrInvalidCursor, http.StatusBadRequest, "invalid_cursor", true},
		{"another user's", "", true, nil, http.StatusNotFound, "not_found", true},
		{"store error", "", false, errors.New("boom"), http.StatusInternalServerError, "internal_error", true},
	} {
		env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
		owner := env.user.ID
		if tc.other {
			owner = uuid.New()
		}
		sub := env.add(t, owner)
		env.calls.deliveriesErr = tc.err
		rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/"+sub.ID.String()+"/deliveries"+tc.query, "Bearer good", "")
		if rec.Code != tc.status || decodeError(t, rec).Code != tc.code {
			t.Errorf("%s: status %d, body %s; want %d %s", tc.name, rec.Code, rec.Body, tc.status, tc.code)
		}
		if tc.name != "limit not a number" && rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q", tc.name, rec.Header().Get("Cache-Control"))
		}
		if (len(env.calls.deliveryParams) > 0) != tc.stored {
			t.Errorf("%s: store called %d times", tc.name, len(env.calls.deliveryParams))
		}
	}
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	if rec := env.do(t, http.MethodGet, "/v1/webhook-subscriptions/not-a-uuid/deliveries", "Bearer good", ""); rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_parameter" {
		t.Errorf("bad id: %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions/"+uuid.NewString()+"/deliveries", "Bearer good", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST deliveries: %d %s", rec.Code, rec.Body)
	}
}

func TestSendWebhookTestSignsAMarkedSampleAsARealDelivery(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 11, 20, 12, 0, 0, 700_000_000, time.UTC)}
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags, WithClock(clock.Now))
	sub := env.add(t, env.user.ID)
	secret := sealFor(t, env, sub)
	receiver := webhookstest.NewReceiver(t, secret, func(int) int { return http.StatusAccepted })
	env.calls.subs[0].URL = receiver.URL()
	env.h.webhookSender = webhooks.NewSender("civic-scanner-test", receiver.SenderOptions()...)
	env.h.webhookAddresses = webhooks.AddressCheck{Allow: netip.Addr.IsLoopback}

	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions/"+sub.ID.String()+"/test", "Bearer good", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q, body %s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}
	result, raw := decodeTestResult(t, rec)
	if !result.Succeeded || result.StatusCode == nil || *result.StatusCode != http.StatusAccepted || result.Error != nil || result.Type != v1.WebhookAgendaPublished {
		t.Errorf("result = %+v", result)
	}
	if !result.SentAt.Equal(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("sentAt = %s", result.SentAt)
	}
	if v, ok := raw["error"]; !ok || v != nil {
		t.Errorf("error is not an explicit null: %v", raw)
	}

	deliveries := receiver.Deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("receiver got %d requests", len(deliveries))
	}
	got := deliveries[0]
	if got.VerifyErr != nil || !got.Timestamp.Equal(result.SentAt) || got.Header.Get("webhook-id") != result.Id.String() {
		t.Errorf("delivery = %+v, verify %v", got.Header, got.VerifyErr)
	}
	if got.Header.Get("Content-Type") != "application/json" || got.Header.Get("User-Agent") != "civic-scanner-test" {
		t.Errorf("headers = %v", got.Header)
	}
	var body v1.WebhookEventV1
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Test == nil || !*body.Test || body.Id != result.Id || body.Type != v1.WebhookAgendaPublished || body.Agency.Slug != "la-metro" ||
		body.Agency.Name != "LA Metro" || body.Agenda == nil || body.Meeting.Title != webhooks.SampleMeetingTitle {
		t.Errorf("body = %s", got.Body)
	}
	if strings.Contains(env.logs.String(), receiver.URL()) || strings.Contains(env.logs.String(), secret) {
		t.Errorf("the URL or the secret was logged: %s", env.logs)
	}
	if !strings.Contains(env.logs.String(), `"msg":"webhook test sent"`) {
		t.Errorf("no log line: %s", env.logs)
	}
}

func newTestSendEnv(t *testing.T, opts ...HandlerOption) (webhookEnv, store.WebhookSubscription, *fakeWebhookSender, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	sender := &fakeWebhookSender{result: webhooks.Result{StatusCode: http.StatusNoContent, Duration: 42 * time.Millisecond}}
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags, append([]HandlerOption{WithClock(clock.Now), WithWebhookSender(sender)}, opts...)...)
	sub := env.add(t, env.user.ID)
	sealFor(t, env, sub)
	return env, sub, sender, clock
}

func testPath(id uuid.UUID) string { return "/v1/webhook-subscriptions/" + id.String() + "/test" }

func TestSendWebhookTestOfEachType(t *testing.T) {
	for _, tc := range []struct {
		body string
		want v1.WebhookEventType
	}{
		{"", v1.WebhookAgendaPublished},
		{`{}`, v1.WebhookAgendaPublished},
		{`null`, v1.WebhookAgendaPublished},
		{`{"type":"agenda_published"}`, v1.WebhookAgendaPublished},
		{`{"type":"agenda_changed"}`, v1.WebhookAgendaChanged},
		{`{"type":"transcript_ready","other":1}`, v1.WebhookTranscriptReady},
	} {
		env, sub, sender, _ := newTestSendEnv(t)
		rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status %d, body %s", tc.body, rec.Code, rec.Body)
		}
		result, _ := decodeTestResult(t, rec)
		sent := sender.sent()
		if result.Type != tc.want || len(sent) != 1 || !result.Succeeded || result.DurationMs != 42 {
			t.Fatalf("%q: result %+v, %d sent", tc.body, result, len(sent))
		}
		var body v1.WebhookEventV1
		if err := json.Unmarshal(sent[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Type != tc.want || body.Test == nil || !*body.Test || sent[0].ID != result.Id.String() || sent[0].URL != sub.URL {
			t.Errorf("%q: sent %+v, body %s", tc.body, sent[0], sent[0].Body)
		}
		if sent[0].Secret.Reveal() == "" || !sent[0].At.Equal(result.SentAt) {
			t.Errorf("%q: secret or time missing: %+v", tc.body, sent[0])
		}
	}
}

func TestSendWebhookTestGivesEachTestItsOwnID(t *testing.T) {
	env, sub, sender, _ := newTestSendEnv(t)
	for range 2 {
		if rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", ""); rec.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", rec.Code, rec.Body)
		}
	}
	if sent := sender.sent(); len(sent) != 2 || sent[0].ID == sent[1].ID {
		t.Errorf("sent = %+v", sent)
	}
}

func TestSendWebhookTestToAPausedSubscription(t *testing.T) {
	env, sub, sender, _ := newTestSendEnv(t)
	env.calls.subs[0].Status = store.WebhookPaused
	if rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", ""); rec.Code != http.StatusOK || len(sender.sent()) != 1 {
		t.Errorf("status %d, body %s, %d sent", rec.Code, rec.Body, len(sender.sent()))
	}
	if env.calls.subs[0].Status != store.WebhookPaused || len(env.calls.updates) != 0 {
		t.Errorf("the subscription changed: %+v", env.calls)
	}
}

func TestSendWebhookTestReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		result    webhooks.Result
		sealed    []byte
		sends     int
		status    *int
		errorText string
		duration  int
	}{
		{"receiver error", "", webhooks.Result{StatusCode: 500, Duration: 30 * time.Millisecond}, nil, 1, ptr(500), "", 30},
		{"redirect", "", webhooks.Result{StatusCode: 302, Duration: time.Millisecond}, nil, 1, ptr(302), "", 1},
		{"timeout", "", webhooks.Result{Err: errors.New("the endpoint did not answer in time: context deadline exceeded"), Duration: 15 * time.Second}, nil, 1, nil, "the endpoint did not answer in time: context deadline exceeded", 15000},
		{"private address", "https://internal.example.com/hook", webhooks.Result{}, nil, 0, nil, "the URL's host resolves to an address that is not on the public internet", 0},
		{"private literal", "https://10.1.2.3/hook", webhooks.Result{}, nil, 0, nil, "the URL's host must be a public internet address, not a private, loopback, link-local or reserved one", 0},
		{"unresolvable", "https://missing.example.com/hook", webhooks.Result{}, nil, 0, nil, "the URL's host name could not be resolved to an address; check it and try again", 0},
		{"unreadable secret", "", webhooks.Result{}, []byte("not sealed"), 0, nil, "the signing secret cannot be read; rotate the secret", 0},
	} {
		env, sub, sender, _ := newTestSendEnv(t)
		sender.result = tc.result
		if tc.url != "" {
			env.calls.subs[0].URL = tc.url
		}
		if tc.sealed != nil {
			env.calls.secrets[sub.ID] = tc.sealed
		}
		rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", tc.name, rec.Code, rec.Body)
		}
		result, raw := decodeTestResult(t, rec)
		if result.Succeeded || len(sender.sent()) != tc.sends || result.DurationMs != tc.duration {
			t.Errorf("%s: result %+v, %d sent", tc.name, result, len(sender.sent()))
		}
		if (result.StatusCode == nil) != (tc.status == nil) || (tc.status != nil && *result.StatusCode != *tc.status) {
			t.Errorf("%s: statusCode = %v", tc.name, raw["statusCode"])
		}
		if (tc.errorText == "") != (result.Error == nil) || (result.Error != nil && *result.Error != tc.errorText) {
			t.Errorf("%s: error = %v", tc.name, raw["error"])
		}
		if tc.url != "" && strings.Contains(env.logs.String(), tc.url) {
			t.Errorf("%s: the URL was logged", tc.name)
		}
	}
}

func TestSendWebhookTestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(env webhookEnv, sub *store.WebhookSubscription)
		opts   []HandlerOption
		body   string
		status int
		code   string
	}{
		{"unknown type", nil, nil, `{"type":"meeting_cancelled"}`, http.StatusBadRequest, "invalid_event_type"},
		{"empty type", nil, nil, `{"type":""}`, http.StatusBadRequest, "invalid_event_type"},
		{"not an object", nil, nil, `[1]`, http.StatusBadRequest, "invalid_body"},
		{"no sealer", nil, []HandlerOption{WithWebhookSealer(nil)}, "", http.StatusServiceUnavailable, "webhooks_not_configured"},
		{"no sender", nil, []HandlerOption{WithWebhookSender(nil)}, "", http.StatusServiceUnavailable, "webhooks_not_configured"},
		{"another user's", func(env webhookEnv, _ *store.WebhookSubscription) { env.calls.subs[0].UserID = uuid.New() }, nil, "", http.StatusNotFound, "not_found"},
		{"unknown id", func(_ webhookEnv, sub *store.WebhookSubscription) { sub.ID = uuid.New() }, nil, "", http.StatusNotFound, "not_found"},
		{"lookup error", func(env webhookEnv, _ *store.WebhookSubscription) { env.calls.err = errors.New("boom") }, nil, "", http.StatusInternalServerError, "internal_error"},
		{"secret gone", func(env webhookEnv, sub *store.WebhookSubscription) { delete(env.calls.secrets, sub.ID) }, nil, "", http.StatusNotFound, "not_found"},
		{"secret error", func(env webhookEnv, _ *store.WebhookSubscription) { env.calls.secretErr = errors.New("boom") }, nil, "", http.StatusInternalServerError, "internal_error"},
		{"unknown payload version", func(env webhookEnv, _ *store.WebhookSubscription) { env.calls.subs[0].PayloadVersion = "v9" }, nil, "", http.StatusInternalServerError, "internal_error"},
	} {
		env, sub, sender, _ := newTestSendEnv(t, tc.opts...)
		if tc.setup != nil {
			tc.setup(env, &sub)
		}
		rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", tc.body)
		if rec.Code != tc.status || decodeError(t, rec).Code != tc.code || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status %d, body %s, Cache-Control %q; want %d %s", tc.name, rec.Code, rec.Body, rec.Header().Get("Cache-Control"), tc.status, tc.code)
		}
		if len(sender.sent()) != 0 {
			t.Errorf("%s: a test was sent", tc.name)
		}
	}
}

func TestSendWebhookTestIsRateLimitedPerUser(t *testing.T) {
	env, sub, sender, clock := newTestSendEnv(t)
	second := env.add(t, env.user.ID)
	sealFor(t, env, second)
	for i := range maxWebhookTestsPerUser {
		target := sub
		if i%2 == 1 {
			target = second
		}
		if rec := env.do(t, http.MethodPost, testPath(target.ID), "Bearer good", ""); rec.Code != http.StatusOK {
			t.Fatalf("test %d: status %d, body %s", i+1, rec.Code, rec.Body)
		}
		clock.advance(time.Second)
	}
	rec := env.do(t, http.MethodPost, testPath(second.ID), "Bearer good", "")
	if rec.Code != http.StatusTooManyRequests || decodeError(t, rec).Code != "test_limit_reached" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("over the limit: status %d, body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Retry-After"); got != "50" {
		t.Errorf("Retry-After = %q, want 50", got)
	}
	if len(sender.sent()) != maxWebhookTestsPerUser {
		t.Errorf("%d sent", len(sender.sent()))
	}
	if rec := env.do(t, http.MethodPost, testPath(uuid.New()), "Bearer good", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown id over the limit: %d", rec.Code)
	}
	clock.advance(50 * time.Second)
	if rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", ""); rec.Code != http.StatusOK {
		t.Errorf("after the window: status %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(env.logs.String(), `"reason":"rate_limited"`) {
		t.Errorf("refusal not logged: %s", env.logs)
	}
}

func TestWebhookTestLimiter(t *testing.T) {
	l := newWebhookTestLimiter()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	alice, bob := uuid.New(), uuid.New()
	for i := range maxWebhookTestsPerUser {
		if _, ok := l.allow(alice, now.Add(time.Duration(i)*time.Millisecond)); !ok {
			t.Fatalf("test %d refused", i+1)
		}
	}
	wait, ok := l.allow(alice, now.Add(time.Second))
	if ok || wait != webhookTestWindow-time.Second {
		t.Errorf("over the limit: wait %s, ok %v", wait, ok)
	}
	if _, ok := l.allow(bob, now.Add(time.Second)); !ok {
		t.Error("another user was refused")
	}
	if _, ok := l.allow(alice, now.Add(webhookTestWindow)); !ok {
		t.Error("refused once the first test left the window")
	}
	if got := retryAfterSeconds(1500 * time.Millisecond); got != 2 {
		t.Errorf("retryAfterSeconds(1.5s) = %d", got)
	}
	if got := retryAfterSeconds(0); got != 1 {
		t.Errorf("retryAfterSeconds(0) = %d", got)
	}

	sweep := newWebhookTestLimiter()
	for range webhookTestLimiterSweep {
		sweep.allow(uuid.New(), now)
	}
	sweep.allow(alice, now.Add(2*webhookTestWindow))
	if len(sweep.sent) != 1 {
		t.Errorf("%d users kept after a sweep", len(sweep.sent))
	}
}

func TestSendWebhookTestUsesTheSubscriptionsFormat(t *testing.T) {
	env, sub, sender, _ := newTestSendEnv(t)
	env.calls.subs[0].Format = store.WebhookFormatSlack
	rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", `{"type":"transcript_ready"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	result, _ := decodeTestResult(t, rec)
	sent := sender.sent()
	if len(sent) != 1 || sent[0].ID != result.Id.String() {
		t.Fatalf("sent = %+v", sent)
	}
	var msg webhooks.SlackMessage
	if err := json.Unmarshal(sent[0].Body, &msg); err != nil || !strings.HasPrefix(msg.Text, "Test: Transcript ready: ") {
		t.Errorf("body = %s, %v", sent[0].Body, err)
	}
}
