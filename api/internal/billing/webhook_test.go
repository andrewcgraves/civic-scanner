package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	webhookSecret = "whsec_test_webhook_secret" //nolint:gosec
	testCustomer  = "cus_test_webhook"
	testSub       = "sub_test_webhook"
)

type appliedEvent struct {
	event store.StripeEvent
	sub   *store.SubscriptionState
}

type eventStore struct {
	mu        sync.Mutex
	customers map[string]uuid.UUID
	seen      map[string]bool
	subs      map[string]store.SubscriptionState
	applied   []appliedEvent
	err       error
}

func newEventStore() *eventStore {
	return &eventStore{
		customers: map[string]uuid.UUID{testCustomer: uuid.New()},
		seen:      map[string]bool{},
		subs:      map[string]store.SubscriptionState{},
	}
}

func (s *eventStore) ApplyStripeEvent(_ context.Context, event store.StripeEvent, sub *store.SubscriptionState) (store.StripeEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applied = append(s.applied, appliedEvent{event: event, sub: sub})
	if s.err != nil {
		return store.StripeEventResult{}, s.err
	}
	if s.seen[event.ID] {
		return store.StripeEventResult{Outcome: store.StripeEventDuplicate}, nil
	}
	s.seen[event.ID] = true
	if sub == nil {
		return store.StripeEventResult{Outcome: store.StripeEventIgnored}, nil
	}
	userID, ok := s.customers[sub.CustomerID]
	if !ok {
		return store.StripeEventResult{Outcome: store.StripeEventUnknownCustomer}, nil
	}
	previous := s.plan()
	s.subs[sub.ID] = *sub
	paid := 0
	for _, state := range s.subs {
		if state.Paid {
			paid++
		}
	}
	return store.StripeEventResult{Outcome: store.StripeEventApplied, UserID: userID, PreviousPlan: previous, Plan: s.plan(), PaidSubscriptions: paid}, nil
}

func (s *eventStore) plan() store.Plan {
	for _, state := range s.subs {
		if state.Paid {
			return store.PlanPaid
		}
	}
	return store.PlanFree
}

func (s *eventStore) Plan() store.Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plan()
}

func (s *eventStore) Applied() []appliedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]appliedEvent(nil), s.applied...)
}

type webhookFixture struct {
	hook  *billing.Webhook
	store *eventStore
	logs  *bytes.Buffer
}

func newWebhookFixture(t *testing.T) *webhookFixture {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(&syncBuffer{buf: logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	events := newEventStore()
	cfg := config.Stripe{WebhookSecret: config.NewSecret(webhookSecret)}
	return &webhookFixture{hook: billing.NewWebhook(cfg, events, billing.WithLogger(logger)), store: events, logs: logs}
}

func (f *webhookFixture) event(eventType string, created int64, object map[string]any) []byte {
	return eventPayload("evt_test_"+uuid.NewString()[:8], eventType, created, object)
}

func eventPayload(id, eventType string, created int64, object map[string]any) []byte {
	body, err := json.Marshal(map[string]any{
		"id":          id,
		"object":      "event",
		"type":        eventType,
		"created":     created,
		"api_version": stripe.APIVersion,
		"livemode":    false,
		"data":        map[string]any{"object": object},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func subscription(status string) map[string]any {
	return map[string]any{"id": testSub, "object": "subscription", "customer": testCustomer, "status": status}
}

func checkoutSession(mode, paymentStatus string) map[string]any {
	return map[string]any{
		"id": "cs_test_webhook", "object": "checkout.session", "mode": mode, "payment_status": paymentStatus,
		"customer": testCustomer, "subscription": testSub, "client_reference_id": uuid.NewString(),
	}
}

func sign(payload []byte, secret string, at time.Time) string {
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret, Timestamp: at}).Header
}

func (f *webhookFixture) receive(t *testing.T, payload []byte) (billing.Receipt, error) {
	t.Helper()
	return f.hook.Receive(t.Context(), payload, sign(payload, webhookSecret, time.Now()))
}

func TestWebhookAcceptsAValidSignature(t *testing.T) {
	f := newWebhookFixture(t)
	payload := f.event(billing.EventSubscriptionCreated, 1_800_000_000, subscription("active"))

	receipt, err := f.receive(t, payload)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != store.StripeEventApplied || receipt.EventType != billing.EventSubscriptionCreated || !strings.HasPrefix(receipt.EventID, "evt_test_") {
		t.Errorf("receipt = %+v", receipt)
	}
	applied := f.store.Applied()
	if len(applied) != 1 {
		t.Fatalf("applied %d events, want 1", len(applied))
	}
	got := applied[0]
	if got.event.ID != receipt.EventID || got.event.Type != billing.EventSubscriptionCreated || !got.event.CreatedAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Errorf("event = %+v", got.event)
	}
	if got.sub == nil || *got.sub != (store.SubscriptionState{ID: testSub, CustomerID: testCustomer, Status: "active", Paid: true}) {
		t.Errorf("subscription = %+v", got.sub)
	}
	if f.store.Plan() != store.PlanPaid {
		t.Errorf("plan = %s, want paid", f.store.Plan())
	}
	if !strings.Contains(f.logs.String(), `"msg":"stripe event processed"`) || strings.Contains(f.logs.String(), webhookSecret) {
		t.Errorf("logs = %s", f.logs)
	}
}

func TestWebhookAcceptsARolledSecretAmongSeveralSignatures(t *testing.T) {
	f := newWebhookFixture(t)
	payload := f.event(billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active"))
	now := time.Now()
	old := sign(payload, "whsec_the_old_secret", now)
	current := sign(payload, webhookSecret, now)
	header := old + ",v1=" + current[strings.Index(current, "v1=")+3:]

	if _, err := f.hook.Receive(t.Context(), payload, header); err != nil {
		t.Errorf("err = %v, want the current secret's signature accepted", err)
	}
}

func TestWebhookRefusesBadSignatures(t *testing.T) {
	f := newWebhookFixture(t)
	payload := f.event(billing.EventSubscriptionCreated, 1_800_000_000, subscription("active"))
	now := time.Now()
	tampered := bytes.Replace(payload, []byte(`"active"`), []byte(`"trialing"`), 1)

	for name, tc := range map[string]struct {
		payload []byte
		header  string
		want    error
	}{
		"no header":          {payload, "", stripe.ErrWebhookNotSigned},
		"garbage header":     {payload, "nonsense", stripe.ErrWebhookInvalidHeader},
		"no v1 signature":    {payload, "t=" + strconv.FormatInt(now.Unix(), 10), stripe.ErrWebhookNoValidSignature},
		"other secret":       {payload, sign(payload, "whsec_someone_else", now), stripe.ErrWebhookNoValidSignature},
		"tampered body":      {tampered, sign(payload, webhookSecret, now), stripe.ErrWebhookNoValidSignature},
		"stale timestamp":    {payload, sign(payload, webhookSecret, now.Add(-billing.SignatureTolerance-time.Minute)), stripe.ErrWebhookTooOld},
		"secret key as hmac": {payload, sign(payload, "sk_test_stripetest", now), stripe.ErrWebhookNoValidSignature},
	} {
		_, err := f.hook.Receive(t.Context(), tc.payload, tc.header)
		if !errors.Is(err, billing.ErrInvalidSignature) || !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalidSignature wrapping %v", name, err, tc.want)
		}
	}
	if applied := f.store.Applied(); len(applied) != 0 {
		t.Errorf("a refused event reached the store: %+v", applied)
	}
}

func TestWebhookAcceptsATimestampJustInsideTheTolerance(t *testing.T) {
	f := newWebhookFixture(t)
	payload := f.event(billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active"))
	header := sign(payload, webhookSecret, time.Now().Add(-billing.SignatureTolerance+30*time.Second))
	if _, err := f.hook.Receive(t.Context(), payload, header); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestWebhookIgnoresAReplayedEvent(t *testing.T) {
	f := newWebhookFixture(t)
	payload := f.event(billing.EventSubscriptionCreated, 1_800_000_000, subscription("active"))

	if receipt, err := f.receive(t, payload); err != nil || receipt.Outcome != store.StripeEventApplied {
		t.Fatalf("first delivery: %+v, %v", receipt, err)
	}
	receipt, err := f.hook.Receive(t.Context(), payload, sign(payload, webhookSecret, time.Now().Add(time.Second)))
	if err != nil || receipt.Outcome != store.StripeEventDuplicate {
		t.Errorf("redelivery: %+v, %v; want duplicate", receipt, err)
	}
}

func TestWebhookSubscriptionStatusToPlan(t *testing.T) {
	for status, paid := range map[string]bool{
		"active":             true,
		"trialing":           true,
		"past_due":           true,
		"unpaid":             false,
		"paused":             false,
		"canceled":           false,
		"incomplete":         false,
		"incomplete_expired": false,
		"some_future_status": false,
	} {
		if got := billing.PaidStatus(status); got != paid {
			t.Errorf("PaidStatus(%q) = %v, want %v", status, got, paid)
		}
	}
}

func TestWebhookSubscriptionEvents(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		status    string
		plan      store.Plan
	}{
		{billing.EventSubscriptionCreated, "incomplete", store.PlanFree},
		{billing.EventSubscriptionCreated, "trialing", store.PlanPaid},
		{billing.EventSubscriptionUpdated, "active", store.PlanPaid},
		{billing.EventSubscriptionUpdated, "past_due", store.PlanPaid},
		{billing.EventSubscriptionUpdated, "unpaid", store.PlanFree},
		{billing.EventSubscriptionPaused, "paused", store.PlanFree},
		{billing.EventSubscriptionResumed, "active", store.PlanPaid},
		{billing.EventSubscriptionDeleted, "canceled", store.PlanFree},
	} {
		f := newWebhookFixture(t)
		receipt, err := f.receive(t, f.event(tc.eventType, 1_800_000_000, subscription(tc.status)))
		if err != nil || receipt.Outcome != store.StripeEventApplied {
			t.Errorf("%s %s: %+v, %v", tc.eventType, tc.status, receipt, err)
			continue
		}
		if got := f.store.Plan(); got != tc.plan {
			t.Errorf("%s %s: plan = %s, want %s", tc.eventType, tc.status, got, tc.plan)
		}
		if sub := f.store.Applied()[0].sub; sub == nil || sub.Status != tc.status {
			t.Errorf("%s %s: stored %+v", tc.eventType, tc.status, sub)
		}
	}
}

func TestWebhookSubscriptionLifecycle(t *testing.T) {
	f := newWebhookFixture(t)
	for i, step := range []struct {
		eventType string
		status    string
		plan      store.Plan
	}{
		{billing.EventSubscriptionCreated, "incomplete", store.PlanFree},
		{billing.EventSubscriptionUpdated, "active", store.PlanPaid},
		{billing.EventSubscriptionUpdated, "past_due", store.PlanPaid},
		{billing.EventSubscriptionUpdated, "unpaid", store.PlanFree},
		{billing.EventSubscriptionUpdated, "active", store.PlanPaid},
		{billing.EventSubscriptionDeleted, "canceled", store.PlanFree},
	} {
		if _, err := f.receive(t, f.event(step.eventType, int64(1_800_000_000+i), subscription(step.status))); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := f.store.Plan(); got != step.plan {
			t.Errorf("step %d (%s %s): plan = %s, want %s", i, step.eventType, step.status, got, step.plan)
		}
	}
}

func TestWebhookCheckoutEvents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType string
		session   map[string]any
		want      *store.SubscriptionState
	}{
		{
			"paid", billing.EventCheckoutCompleted, checkoutSession("subscription", "paid"),
			&store.SubscriptionState{ID: testSub, CustomerID: testCustomer, Status: billing.CheckoutCompletedStatus, Paid: true},
		},
		{
			"free trial", billing.EventCheckoutCompleted, checkoutSession("subscription", "no_payment_required"),
			&store.SubscriptionState{ID: testSub, CustomerID: testCustomer, Status: billing.CheckoutCompletedStatus, Paid: true},
		},
		{
			"async payment succeeded", billing.EventCheckoutAsyncPaymentPassed, checkoutSession("subscription", "paid"),
			&store.SubscriptionState{ID: testSub, CustomerID: testCustomer, Status: billing.CheckoutCompletedStatus, Paid: true},
		},
		{"payment pending", billing.EventCheckoutCompleted, checkoutSession("subscription", "unpaid"), nil},
		{"one-off payment", billing.EventCheckoutCompleted, checkoutSession("payment", "paid"), nil},
		{"no subscription", billing.EventCheckoutCompleted, func() map[string]any {
			s := checkoutSession("subscription", "paid")
			s["subscription"] = nil
			return s
		}(), nil},
	} {
		f := newWebhookFixture(t)
		receipt, err := f.receive(t, f.event(tc.eventType, 1_800_000_000, tc.session))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		applied := f.store.Applied()
		if len(applied) != 1 {
			t.Errorf("%s: applied %d, want 1 (the event is recorded either way)", tc.name, len(applied))
			continue
		}
		got := applied[0].sub
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: subscription %+v, want none", tc.name, got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s: subscription %+v, want %+v", tc.name, got, tc.want)
		}
		wantOutcome := store.StripeEventApplied
		if tc.want == nil {
			wantOutcome = store.StripeEventIgnored
		}
		if receipt.Outcome != wantOutcome {
			t.Errorf("%s: outcome %s, want %s", tc.name, receipt.Outcome, wantOutcome)
		}
	}
}

func TestWebhookReadsExpandedObjects(t *testing.T) {
	f := newWebhookFixture(t)
	session := checkoutSession("subscription", "paid")
	session["customer"] = map[string]any{"id": testCustomer, "object": "customer"}
	session["subscription"] = map[string]any{"id": testSub, "object": "subscription", "status": "active"}
	if _, err := f.receive(t, f.event(billing.EventCheckoutCompleted, 1_800_000_000, session)); err != nil {
		t.Fatal(err)
	}
	sub := f.store.Applied()[0].sub
	if sub == nil || sub.ID != testSub || sub.CustomerID != testCustomer {
		t.Errorf("subscription = %+v", sub)
	}
}

func TestWebhookIgnoresOtherEventsWithoutTouchingTheStore(t *testing.T) {
	f := newWebhookFixture(t)
	for _, eventType := range []string{"invoice.paid", "invoice.payment_failed", "customer.created", "customer.subscription.trial_will_end", "checkout.session.expired"} {
		receipt, err := f.receive(t, f.event(eventType, 1_800_000_000, map[string]any{"id": "in_1", "object": "invoice"}))
		if err != nil || receipt.Outcome != store.StripeEventIgnored || receipt.EventType != eventType {
			t.Errorf("%s: %+v, %v; want ignored", eventType, receipt, err)
		}
	}
	connected := eventPayload("evt_connected", billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active"))
	var withAccount map[string]any
	if err := json.Unmarshal(connected, &withAccount); err != nil {
		t.Fatal(err)
	}
	withAccount["account"] = "acct_someone"
	connected, _ = json.Marshal(withAccount)
	if receipt, err := f.receive(t, connected); err != nil || receipt.Outcome != store.StripeEventIgnored {
		t.Errorf("connected account: %+v, %v; want ignored", receipt, err)
	}
	if applied := f.store.Applied(); len(applied) != 0 {
		t.Errorf("ignored events reached the store: %+v", applied)
	}
}

func TestWebhookAcceptsAnotherAPIVersion(t *testing.T) {
	f := newWebhookFixture(t)
	var event map[string]any
	if err := json.Unmarshal(f.event(billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active")), &event); err != nil {
		t.Fatal(err)
	}
	event["api_version"] = "2024-06-20"
	payload, _ := json.Marshal(event)
	if receipt, err := f.receive(t, payload); err != nil || receipt.Outcome != store.StripeEventApplied {
		t.Errorf("%+v, %v", receipt, err)
	}
	if !strings.Contains(f.logs.String(), `"api_version":"2024-06-20"`) {
		t.Errorf("the event's API version is not logged: %s", f.logs)
	}
}

func TestWebhookRefusesUnreadableSignedEvents(t *testing.T) {
	for name, payload := range map[string][]byte{
		"not json":           []byte("not json"),
		"thin event":         []byte(`{"id":"evt_1","object":"v2.core.event","type":"x"}`),
		"no id":              eventPayload("", billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active")),
		"no created":         eventPayload("evt_1", billing.EventSubscriptionUpdated, 0, subscription("active")),
		"wrong object":       eventPayload("evt_1", billing.EventSubscriptionUpdated, 1_800_000_000, map[string]any{"id": "in_1", "object": "invoice"}),
		"no customer":        eventPayload("evt_1", billing.EventSubscriptionUpdated, 1_800_000_000, map[string]any{"id": testSub, "object": "subscription", "status": "active"}),
		"no status":          eventPayload("evt_1", billing.EventSubscriptionUpdated, 1_800_000_000, map[string]any{"id": testSub, "object": "subscription", "customer": testCustomer}),
		"customer is number": eventPayload("evt_1", billing.EventSubscriptionUpdated, 1_800_000_000, map[string]any{"id": testSub, "object": "subscription", "customer": 7, "status": "active"}),
		"session not object": eventPayload("evt_1", billing.EventCheckoutCompleted, 1_800_000_000, map[string]any{"id": "cs_1", "object": "subscription"}),
		"no data":            []byte(`{"id":"evt_1","object":"event","type":"customer.subscription.updated","created":1800000000}`),
	} {
		f := newWebhookFixture(t)
		_, err := f.receive(t, payload)
		if !errors.Is(err, billing.ErrInvalidEvent) || errors.Is(err, billing.ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
		if applied := f.store.Applied(); len(applied) != 0 {
			t.Errorf("%s: an unreadable event reached the store", name)
		}
	}
}

func TestWebhookPassesOnStoreErrors(t *testing.T) {
	f := newWebhookFixture(t)
	f.store.err = errors.New("database down")
	_, err := f.receive(t, f.event(billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active")))
	if err == nil || errors.Is(err, billing.ErrInvalidEvent) || errors.Is(err, billing.ErrInvalidSignature) || !strings.Contains(err.Error(), "database down") {
		t.Errorf("err = %v, want the store error", err)
	}
}

func TestWebhookWarnsAboutUnknownCustomersAndSecondSubscriptions(t *testing.T) {
	f := newWebhookFixture(t)
	stranger := subscription("active")
	stranger["customer"] = "cus_nobody"
	if receipt, err := f.receive(t, f.event(billing.EventSubscriptionUpdated, 1_800_000_000, stranger)); err != nil || receipt.Outcome != store.StripeEventUnknownCustomer {
		t.Errorf("unknown customer: %+v, %v", receipt, err)
	}
	if !strings.Contains(f.logs.String(), "stripe event for a customer no user has") {
		t.Errorf("no warning for the unknown customer: %s", f.logs)
	}

	if _, err := f.receive(t, f.event(billing.EventSubscriptionCreated, 1_800_000_001, subscription("active"))); err != nil {
		t.Fatal(err)
	}
	second := subscription("active")
	second["id"] = "sub_test_second"
	if _, err := f.receive(t, f.event(billing.EventSubscriptionCreated, 1_800_000_002, second)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.logs.String(), "user has more than one paid subscription") || !strings.Contains(f.logs.String(), `"paid_subscriptions":2`) {
		t.Errorf("no warning for the second paid subscription: %s", f.logs)
	}
}

func TestWebhookNotConfigured(t *testing.T) {
	payload := eventPayload("evt_1", billing.EventSubscriptionUpdated, 1_800_000_000, subscription("active"))
	header := sign(payload, webhookSecret, time.Now())
	for name, hook := range map[string]*billing.Webhook{
		"nil":            nil,
		"zero":           {},
		"no secret":      billing.NewWebhook(config.Stripe{}, newEventStore()),
		"invalid secret": billing.NewWebhook(config.Stripe{WebhookSecret: config.NewSecret(webhookSecret), WebhookProblem: "bad"}, newEventStore()),
		"no store":       billing.NewWebhook(config.Stripe{WebhookSecret: config.NewSecret(webhookSecret)}, nil),
	} {
		if hook.Configured() {
			t.Errorf("%s: Configured() = true", name)
		}
		if _, err := hook.Receive(t.Context(), payload, header); !errors.Is(err, billing.ErrNotConfigured) {
			t.Errorf("%s: err = %v, want ErrNotConfigured", name, err)
		}
	}
	if !billing.NewWebhook(config.Stripe{WebhookSecret: config.NewSecret(webhookSecret)}, newEventStore()).Configured() {
		t.Error("a webhook with a secret and a store is not configured")
	}
}
