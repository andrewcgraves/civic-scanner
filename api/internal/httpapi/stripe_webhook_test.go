package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const testWebhookSecret = "whsec_httpapi_test" //nolint:gosec

type recordingEvents struct {
	events []store.StripeEvent
	result store.StripeEventResult
	err    error
}

func (r *recordingEvents) ApplyStripeEvent(_ context.Context, event store.StripeEvent, _ *store.SubscriptionState) (store.StripeEventResult, error) {
	r.events = append(r.events, event)
	return r.result, r.err
}

func stripeEventBody(t *testing.T, padding int) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": "evt_httpapi", "object": "event", "type": billing.EventSubscriptionUpdated, "created": 1_800_000_000,
		"data": map[string]any{"object": map[string]any{
			"id": "sub_1", "object": "subscription", "customer": "cus_1", "status": "active",
			"metadata": map[string]string{"padding": strings.Repeat("x", padding)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signedHeader(body []byte, at time.Time) string {
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: body, Secret: testWebhookSecret, Timestamp: at}).Header
}

func webhookHandler(events billing.EventStore) *Handler {
	hook := billing.NewWebhook(config.Stripe{WebhookSecret: config.NewSecret(testWebhookSecret)}, events)
	return NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithStripeEvents(hook), WithAuthenticator(goodAuth()), WithFlags(flagsOn(false)))
}

func postStripeEvent(t *testing.T, h *Handler, body []byte, signature string, extra ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, stripeWebhookPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if signature != "" {
		req.Header.Set("Stripe-Signature", signature)
	}
	for _, f := range extra {
		f(req)
	}
	rec := httptest.NewRecorder()
	NewRouter(h, slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)
	return rec
}

func TestStripeWebhookProcessesASignedEventWithoutABearerToken(t *testing.T) {
	events := &recordingEvents{result: store.StripeEventResult{Outcome: store.StripeEventApplied, Plan: store.PlanPaid}}
	body := stripeEventBody(t, 0)

	rec := postStripeEvent(t, webhookHandler(events), body, signedHeader(body, time.Now()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var receipt v1.StripeEventReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil || receipt.Outcome != v1.Applied {
		t.Errorf("body %s, err %v", rec.Body, err)
	}
	if len(events.events) != 1 || events.events[0].ID != "evt_httpapi" {
		t.Errorf("events = %+v", events.events)
	}
}

func TestStripeWebhookOutcomes(t *testing.T) {
	for _, outcome := range []store.StripeEventOutcome{store.StripeEventDuplicate, store.StripeEventStale, store.StripeEventUnknownCustomer, store.StripeEventIgnored} {
		body := stripeEventBody(t, 0)
		rec := postStripeEvent(t, webhookHandler(&recordingEvents{result: store.StripeEventResult{Outcome: outcome}}), body, signedHeader(body, time.Now()))
		var receipt v1.StripeEventReceipt
		if err := json.Unmarshal(rec.Body.Bytes(), &receipt); rec.Code != http.StatusOK || err != nil || string(receipt.Outcome) != string(outcome) || !receipt.Outcome.Valid() {
			t.Errorf("%s: status %d, body %s", outcome, rec.Code, rec.Body)
		}
	}
}

func TestStripeWebhookIgnoresABearerToken(t *testing.T) {
	events := &recordingEvents{result: store.StripeEventResult{Outcome: store.StripeEventApplied}}
	body := stripeEventBody(t, 0)
	for _, authorization := range []string{"Bearer good", "Bearer garbage"} {
		rec := postStripeEvent(t, webhookHandler(events), body, signedHeader(body, time.Now()), func(r *http.Request) { r.Header.Set("Authorization", authorization) })
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, body %s", authorization, rec.Code, rec.Body)
		}
	}
}

func TestStripeWebhookRefusesBadSignatures(t *testing.T) {
	body := stripeEventBody(t, 0)
	for name, header := range map[string]string{
		"missing":       "",
		"malformed":     "v1=abc",
		"wrong secret":  webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: body, Secret: "whsec_other"}).Header,
		"stale":         signedHeader(body, time.Now().Add(-billing.SignatureTolerance-time.Second)),
		"other payload": signedHeader(stripeEventBody(t, 1), time.Now()),
	} {
		events := &recordingEvents{}
		rec := postStripeEvent(t, webhookHandler(events), body, header)
		if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_signature" {
			t.Errorf("%s: status %d, body %s, want 400 invalid_signature", name, rec.Code, rec.Body)
		}
		if len(events.events) != 0 {
			t.Errorf("%s: the event reached the store", name)
		}
	}
}

func TestStripeWebhookRefusesASignedUnreadableEvent(t *testing.T) {
	body := []byte(`{"id":"evt_1","object":"event","type":"customer.subscription.updated","created":1800000000,"data":{"object":{"object":"invoice"}}}`)
	rec := postStripeEvent(t, webhookHandler(&recordingEvents{}), body, signedHeader(body, time.Now()))
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_event" {
		t.Errorf("status %d, body %s, want 400 invalid_event", rec.Code, rec.Body)
	}
}

func TestStripeWebhookRefusesABodyThatIsNotJSON(t *testing.T) {
	body := []byte("not json")
	rec := postStripeEvent(t, webhookHandler(&recordingEvents{}), body, signedHeader(body, time.Now()))
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "invalid_body" {
		t.Errorf("status %d, body %s, want 400 invalid_body", rec.Code, rec.Body)
	}
}

func TestStripeWebhookTakesEventsLargerThanOtherBodies(t *testing.T) {
	events := &recordingEvents{result: store.StripeEventResult{Outcome: store.StripeEventApplied}}
	body := stripeEventBody(t, 4*maxRequestBodyBytes)
	if rec := postStripeEvent(t, webhookHandler(events), body, signedHeader(body, time.Now())); rec.Code != http.StatusOK {
		t.Errorf("a %d-byte event: status %d, body %s", len(body), rec.Code, rec.Body)
	}

	tooLarge := stripeEventBody(t, maxStripeEventBytes)
	rec := postStripeEvent(t, webhookHandler(events), tooLarge, signedHeader(tooLarge, time.Now()))
	if rec.Code != http.StatusRequestEntityTooLarge || decodeError(t, rec).Code != "body_too_large" {
		t.Errorf("over 1 MiB: status %d, body %s, want 413", rec.Code, rec.Body)
	}
	if len(events.events) != 1 {
		t.Errorf("events = %d, want only the first", len(events.events))
	}
}

func TestStripeWebhookNotConfigured(t *testing.T) {
	body := stripeEventBody(t, 0)
	for name, h := range map[string]*Handler{
		"default":   NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test"),
		"no secret": NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithStripeEvents(billing.NewWebhook(config.Stripe{}, &recordingEvents{}))),
	} {
		rec := postStripeEvent(t, h, body, signedHeader(body, time.Now()))
		if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Code != "billing_not_configured" {
			t.Errorf("%s: status %d, body %s, want 503 billing_not_configured", name, rec.Code, rec.Body)
		}
	}
}

func TestStripeWebhookStoreErrorIsA500SoStripeRetries(t *testing.T) {
	body := stripeEventBody(t, 0)
	rec := postStripeEvent(t, webhookHandler(&recordingEvents{err: errors.New("database down")}), body, signedHeader(body, time.Now()))
	if rec.Code != http.StatusInternalServerError || decodeError(t, rec).Code != "internal_error" || strings.Contains(rec.Body.String(), "database") {
		t.Errorf("status %d, body %s, want 500 internal_error", rec.Code, rec.Body)
	}
}

type stubReceiver struct {
	configured bool
	err        error
}

func (s stubReceiver) Configured() bool { return s.configured }

func (s stubReceiver) Receive(context.Context, []byte, string) (billing.Receipt, error) {
	return billing.Receipt{}, s.err
}

func TestStripeWebhookMapsReceiverErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{billing.ErrNotConfigured, http.StatusServiceUnavailable, "billing_not_configured"},
		{fmt.Errorf("%w: x", billing.ErrInvalidSignature), http.StatusBadRequest, "invalid_signature"},
		{fmt.Errorf("%w: x", billing.ErrInvalidEvent), http.StatusBadRequest, "invalid_event"},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	} {
		h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithStripeEvents(stubReceiver{configured: true, err: tc.err}))
		rec := postStripeEvent(t, h, []byte(`{"id":"evt_1","type":"x"}`), "t=1,v1=00")
		if rec.Code != tc.status || decodeError(t, rec).Code != tc.code {
			t.Errorf("%v: status %d, body %s, want %d %s", tc.err, rec.Code, rec.Body, tc.status, tc.code)
		}
	}
}

func TestStripeWebhookOnlyTakesPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, stripeWebhookPath, nil)
	rec := httptest.NewRecorder()
	NewRouter(webhookHandler(&recordingEvents{}), slog.New(slog.DiscardHandler)).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("status %d, Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
}
