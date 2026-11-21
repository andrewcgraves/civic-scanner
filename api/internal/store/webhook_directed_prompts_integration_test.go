//go:build integration

package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestWebhookSubscriptionPromptIsStoredChangedAndCleared(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	user := webhookUser(t, pool)
	slug := webhookAgency(t, pool, "live")

	in := webhookInput(user, slug, "https://hooks.example.com/p")
	focus := "Fares on the A Line"
	in.Prompt = &focus
	created, err := s.CreateWebhookSubscription(ctx, in)
	if err != nil || created.Prompt == nil || *created.Prompt != focus {
		t.Fatalf("created = %+v, %v", created, err)
	}
	blank := ""
	standard := webhookInput(user, slug, "https://hooks.example.com/s")
	standard.Prompt = &blank
	if sub, err := s.CreateWebhookSubscription(ctx, standard); err != nil || sub.Prompt != nil {
		t.Errorf("blank prompt stored as %v, %v", sub.Prompt, err)
	}

	paused := store.WebhookPaused
	sub, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Status: &paused})
	if err != nil || sub.Prompt == nil || *sub.Prompt != focus {
		t.Errorf("an update without a prompt changed it: %+v, %v", sub.Prompt, err)
	}
	other := "Bus lanes on Vermont"
	if sub, err = s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Prompt: &other}); err != nil || *sub.Prompt != other {
		t.Errorf("changed prompt = %v, %v", sub.Prompt, err)
	}
	if got, err := s.GetWebhookSubscription(ctx, user.ID, created.ID); err != nil || *got.Prompt != other {
		t.Errorf("read back = %v, %v", got.Prompt, err)
	}
	if sub, err = s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Prompt: &blank}); err != nil || sub.Prompt != nil {
		t.Errorf("cleared prompt = %v, %v", sub.Prompt, err)
	}

	huge := strings.Repeat("x", 4001)
	if _, err := s.UpdateWebhookSubscription(ctx, user.ID, created.ID, store.WebhookSubscriptionUpdate{Prompt: &huge}); err == nil {
		t.Error("the database took a prompt over 4,000 bytes")
	}
}

func TestDirectedFanOutTargetsAndDeliveryCarryThePromptAndRun(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	_, plain := paidSubscription(t, pool, f.slug, "https://hooks.example.com/plain", true)
	owner, directed := paidSubscription(t, pool, f.slug, "https://hooks.example.com/directed", true)
	focus := "Fares"
	if _, err := s.UpdateWebhookSubscription(ctx, owner.ID, directed.ID, store.WebhookSubscriptionUpdate{Prompt: &focus}); err != nil {
		t.Fatal(err)
	}

	fo, err := s.WebhookFanOutTargets(ctx, f.event.ID)
	if err != nil || len(fo.Targets) != 2 || fo.Targets[0].Directed || !fo.Targets[1].Directed {
		t.Fatalf("targets = %+v, %v", fo.Targets, err)
	}

	d, err := s.WebhookDelivery(ctx, directed.ID, f.event.ID)
	if err != nil || d.Prompt == nil || *d.Prompt != focus || d.DirectedOutcome != nil || d.DirectedMessage != nil {
		t.Fatalf("delivery before a run = %+v, %v", d, err)
	}
	if d, err := s.WebhookDelivery(ctx, plain.ID, f.event.ID); err != nil || d.Prompt != nil {
		t.Errorf("standard delivery prompt = %v, %v", d.Prompt, err)
	}

	run := store.DirectedRunInput{SubscriptionID: directed.ID, EventID: f.event.ID, Model: "anthropic/strong", Calls: 1, InputTokens: 1000, OutputTokens: 10}
	if r, err := s.RecordDirectedRun(ctx, run); err != nil || r.Outcome != nil || r.InputTokens != 1000 {
		t.Fatalf("usage only = %+v, %v", r, err)
	}
	run.Outcome, run.Message, run.InputTokens, run.OutputTokens = store.DirectedRelevant, "Fares rise in July.", 1100, 30
	r, err := s.RecordDirectedRun(ctx, run)
	if err != nil || r.Outcome == nil || *r.Outcome != store.DirectedRelevant || *r.Message != "Fares rise in July." ||
		r.Calls != 2 || r.InputTokens != 2100 || r.OutputTokens != 40 || r.Model != "anthropic/strong" {
		t.Fatalf("relevant = %+v, %v", r, err)
	}
	if r, err := s.RecordDirectedRun(ctx, store.DirectedRunInput{SubscriptionID: directed.ID, EventID: f.event.ID, Outcome: store.DirectedNotRelevant, Calls: 1, InputTokens: 5}); err != nil ||
		*r.Outcome != store.DirectedRelevant || *r.Message != "Fares rise in July." || r.InputTokens != 2105 || r.Model != "anthropic/strong" {
		t.Errorf("a later run replaced the final outcome: %+v, %v", r, err)
	}

	d, err = s.WebhookDelivery(ctx, directed.ID, f.event.ID)
	if err != nil || d.DirectedOutcome == nil || *d.DirectedOutcome != store.DirectedRelevant || d.DirectedMessage == nil || *d.DirectedMessage != "Fares rise in July." {
		t.Errorf("delivery after the run = %+v, %v", d, err)
	}
	if got, err := s.GetDirectedRun(ctx, directed.ID, f.event.ID); err != nil || got.Calls != 3 {
		t.Errorf("GetDirectedRun = %+v, %v", got, err)
	}
	if _, err := s.GetDirectedRun(ctx, plain.ID, f.event.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetDirectedRun of a standard subscription: %v", err)
	}
}

func TestAFailedDirectedRunCanStillBecomeFinal(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	_, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/f", true)

	if r, err := s.RecordDirectedRun(ctx, store.DirectedRunInput{SubscriptionID: sub.ID, EventID: f.event.ID, Outcome: store.DirectedFailed, Calls: 1}); err != nil || *r.Outcome != store.DirectedFailed || r.Message != nil {
		t.Fatalf("failed = %+v, %v", r, err)
	}
	if r, err := s.RecordDirectedRun(ctx, store.DirectedRunInput{SubscriptionID: sub.ID, EventID: f.event.ID, Outcome: store.DirectedNotRelevant, Calls: 1}); err != nil || *r.Outcome != store.DirectedNotRelevant || r.Calls != 2 {
		t.Errorf("after a failure = %+v, %v", r, err)
	}
}

func TestRecordDirectedRunRefusesBadInput(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	_, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/bad", true)
	for name, in := range map[string]store.DirectedRunInput{
		"relevant without a message": {SubscriptionID: sub.ID, EventID: f.event.ID, Outcome: store.DirectedRelevant},
		"unknown outcome":            {SubscriptionID: sub.ID, EventID: f.event.ID, Outcome: "maybe"},
		"no subscription":            {EventID: f.event.ID},
		"negative tokens":            {SubscriptionID: sub.ID, EventID: f.event.ID, InputTokens: -1},
	} {
		if _, err := s.RecordDirectedRun(ctx, in); !errors.Is(err, store.ErrInvalidDirectedRun) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := s.RecordDirectedRun(ctx, store.DirectedRunInput{SubscriptionID: uuid.New(), EventID: f.event.ID}); err == nil {
		t.Error("a run of a missing subscription was stored")
	}
	long := strings.Repeat("é", 3000)
	r, err := s.RecordDirectedRun(ctx, store.DirectedRunInput{SubscriptionID: sub.ID, EventID: f.event.ID, Outcome: store.DirectedRelevant, Message: long})
	if err != nil || len(*r.Message) > store.MaxDirectedMessageBytes {
		t.Errorf("long message: %d bytes, %v", len(*r.Message), err)
	}
}
