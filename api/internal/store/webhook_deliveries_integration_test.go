//go:build integration

package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type deliveryFixture struct {
	agencyID uuid.UUID
	slug     string
	meeting  store.Meeting
	event    store.MeetingEvent
}

func seedDeliveryFixture(t *testing.T, pool *pgxpool.Pool) deliveryFixture {
	t.Helper()
	ctx := t.Context()
	s := store.New(pool)
	f := deliveryFixture{slug: webhookAgency(t, pool, "live")}
	if err := pool.QueryRow(ctx, `SELECT id FROM agencies WHERE slug = $1`, f.slug).Scan(&f.agencyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, f.agencyID)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, f.agencyID)
	})
	var err error
	f.meeting, _, err = s.UpsertMeeting(ctx, store.MeetingInput{
		AgencyID: f.agencyID, Title: "Board", StartsAt: time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC),
		Status: "scheduled", SourceURL: "https://agency.example/m/1", SourceID: "m1",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.event = recordEvent(t, pool, f.meeting.ID)
	return f
}

func recordEvent(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID) store.MeetingEvent {
	t.Helper()
	var e store.MeetingEvent
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		e, err = store.New(pool).RecordMeetingEventTx(t.Context(), tx, store.MeetingEventInput{
			Type: store.EventAgendaPublished, MeetingID: meetingID,
			Payload: store.AgendaEventPayload{AgendaVersionID: uuid.New(), Version: 1},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func paidSubscription(t *testing.T, pool *pgxpool.Pool, slug, url string, paid bool) (store.User, store.WebhookSubscription) {
	t.Helper()
	user := webhookUser(t, pool)
	if paid {
		if _, err := pool.Exec(t.Context(), `UPDATE users SET plan = 'paid' WHERE id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := store.New(pool).CreateWebhookSubscription(t.Context(), webhookInput(user, slug, url))
	if err != nil {
		t.Fatal(err)
	}
	return user, sub
}

var wideFanOut = store.FanOutParams{Limit: 100_000, Lookback: 1_000_000}

func TestEventsAwaitFanOutUntilClaimedOnce(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)

	waiting, err := s.ListEventsAwaitingFanOut(ctx, wideFanOut)
	if err != nil || !slices.Contains(waiting, f.event.ID) {
		t.Fatalf("waiting = %d ids, contains event %v, err %v", len(waiting), slices.Contains(waiting, f.event.ID), err)
	}

	claim := func() bool {
		var claimed bool
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			claimed, err = s.ClaimWebhookFanOutTx(ctx, tx, f.event.ID, f.event.Sequence, 2)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return claimed
	}
	if !claim() {
		t.Fatal("first claim refused")
	}
	if claim() {
		t.Error("second claim accepted")
	}
	var deliveries int
	if err := pool.QueryRow(ctx, `SELECT deliveries FROM webhook_event_fan_outs WHERE event_id = $1`, f.event.ID).Scan(&deliveries); err != nil || deliveries != 2 {
		t.Errorf("deliveries = %d, %v", deliveries, err)
	}
	if waiting, err = s.ListEventsAwaitingFanOut(ctx, wideFanOut); err != nil || slices.Contains(waiting, f.event.ID) {
		t.Errorf("claimed event still waiting: %v", err)
	}

	later := recordEvent(t, pool, f.meeting.ID)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		_, err := s.ClaimWebhookFanOutTx(ctx, tx, later.ID, later.Sequence, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM webhook_event_fan_outs WHERE event_id = $1`, f.event.ID); err != nil {
		t.Fatal(err)
	}
	if waiting, err = s.ListEventsAwaitingFanOut(ctx, store.FanOutParams{Limit: 100_000, Lookback: 0}); err != nil || slices.Contains(waiting, f.event.ID) {
		t.Errorf("an event behind the newest fanned-out one is found only within the lookback: %v", err)
	}
	if waiting, err = s.ListEventsAwaitingFanOut(ctx, wideFanOut); err != nil || !slices.Contains(waiting, f.event.ID) {
		t.Errorf("an event committed late is found within the lookback: %v", err)
	}
}

func TestWebhookFanOutTargetsAreActivePaidSubscriptionsOfTheEventsAgency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)

	paidUser, active := paidSubscription(t, pool, f.slug, "https://hooks.example/active", true)
	pausedStatus := store.WebhookPaused
	pausedUser, paused := paidSubscription(t, pool, f.slug, "https://hooks.example/paused", true)
	if _, err := s.UpdateWebhookSubscription(ctx, pausedUser.ID, paused.ID, store.WebhookSubscriptionUpdate{Status: &pausedStatus}); err != nil {
		t.Fatal(err)
	}
	paidSubscription(t, pool, f.slug, "https://hooks.example/free", false)
	paidSubscription(t, pool, webhookAgency(t, pool, "live"), "https://hooks.example/other", true)

	fo, err := s.WebhookFanOutTargets(ctx, f.event.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.WebhookTarget{{SubscriptionID: active.ID, UserID: paidUser.ID, WorkOSUserID: paidUser.WorkOSUserID}}
	if fo.EventID != f.event.ID || fo.Sequence != f.event.Sequence || !slices.Equal(fo.Targets, want) {
		t.Errorf("fan-out = %+v, want targets %+v", fo, want)
	}
	if _, err := s.WebhookFanOutTargets(ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown event: %v", err)
	}
}

func TestWebhookDeliveryReadsTheSubscriptionAndTheEvent(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example/read?token=x", true)

	d, err := s.WebhookDelivery(ctx, sub.ID, f.event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.SubscriptionID != sub.ID || d.UserID != user.ID || d.WorkOSUserID != user.WorkOSUserID || !d.Paid ||
		d.URL != "https://hooks.example/read?token=x" || d.PayloadVersion != "v1" || d.Status != store.WebhookActive ||
		!strings.HasPrefix(string(d.SecretCiphertext), "sealed-") {
		t.Errorf("subscription side = %+v", d)
	}
	if d.Event.ID != f.event.ID || d.Event.Type != store.EventAgendaPublished || string(d.Event.Payload) != string(f.event.Payload) ||
		!d.Event.OccurredAt.Equal(f.event.OccurredAt) || d.AgencySlug != f.slug || d.AgencyName != "Webhook Test Agency" ||
		d.Meeting.ID != f.meeting.ID || d.Meeting.Title != "Board" || !d.Meeting.StartsAt.Equal(f.meeting.StartsAt) ||
		d.Meeting.SourceURL != "https://agency.example/m/1" {
		t.Errorf("event side = %+v", d)
	}

	_, other := paidSubscription(t, pool, webhookAgency(t, pool, "live"), "https://hooks.example/other", true)
	for name, ids := range map[string][2]uuid.UUID{
		"another agency's event": {other.ID, f.event.ID},
		"unknown subscription":   {uuid.New(), f.event.ID},
		"unknown event":          {sub.ID, uuid.New()},
	} {
		if _, err := s.WebhookDelivery(ctx, ids[0], ids[1]); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWebhookAttemptsAreRecordedAndListedNewestFirst(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	_, sub := paidSubscription(t, pool, f.slug, "https://hooks.example/attempts", true)
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	for _, in := range []store.WebhookAttemptInput{
		{Attempt: 1, StartedAt: start, Duration: 1500 * time.Millisecond, StatusCode: 503, Error: "the endpoint answered 503"},
		{Attempt: 2, StartedAt: start.Add(time.Minute), Duration: 40 * time.Millisecond, Error: strings.Repeat("é", 400)},
		{Attempt: 3, StartedAt: start.Add(2 * time.Minute), Duration: 12 * time.Millisecond, StatusCode: 204, Succeeded: true, Error: "ignored"},
		{Attempt: 0, StartedAt: start.Add(3 * time.Minute), Duration: -time.Second},
	} {
		in.SubscriptionID, in.EventID = sub.ID, f.event.ID
		if err := s.RecordWebhookAttempt(ctx, in); err != nil {
			t.Fatalf("record %+v: %v", in.Attempt, err)
		}
	}
	attempts, err := s.ListWebhookAttempts(ctx, sub.ID)
	if err != nil || len(attempts) != 4 {
		t.Fatalf("attempts = %d, %v", len(attempts), err)
	}
	last, ok, long, first := attempts[0], attempts[1], attempts[2], attempts[3]
	if last.Attempt != 1 || last.Duration != 0 || last.StatusCode != nil || last.Error == nil || *last.Error != "the delivery failed" || last.Succeeded {
		t.Errorf("attempt without a status or error = %+v", last)
	}
	if ok.Attempt != 3 || !ok.Succeeded || ok.StatusCode == nil || *ok.StatusCode != 204 || ok.Error != nil || ok.Duration != 12*time.Millisecond {
		t.Errorf("success = %+v", ok)
	}
	if long.Error == nil || len(*long.Error) != 500 || long.StatusCode != nil {
		t.Errorf("long error = %d bytes", len(*long.Error))
	}
	if first.StatusCode == nil || *first.StatusCode != 503 || first.Duration != 1500*time.Millisecond || first.EventID != f.event.ID {
		t.Errorf("first = %+v", first)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	if attempts, err = s.ListWebhookAttempts(ctx, sub.ID); err != nil || len(attempts) != 0 {
		t.Errorf("attempts after the subscription is deleted = %d, %v", len(attempts), err)
	}
}

func TestFailedWebhookEventsPauseTheSubscriptionUntilResumed(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example/pause", true)

	for i := 1; i <= 2; i++ {
		got, err := s.RecordWebhookEventFailed(ctx, sub.ID, 3)
		if err != nil || got != (store.WebhookEventFailure{ConsecutiveFailedEvents: i}) {
			t.Fatalf("failure %d = %+v, %v", i, got, err)
		}
	}
	if err := s.RecordWebhookEventDelivered(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	read, err := s.GetWebhookSubscription(ctx, user.ID, sub.ID)
	if err != nil || read.ConsecutiveFailedEvents != 0 || read.Status != store.WebhookActive {
		t.Fatalf("a delivered event resets the count: %+v, %v", read, err)
	}

	for i := 1; i <= 3; i++ {
		got, err := s.RecordWebhookEventFailed(ctx, sub.ID, 3)
		if err != nil || got.ConsecutiveFailedEvents != i || got.Paused != (i == 3) {
			t.Fatalf("failure %d after reset = %+v, %v", i, got, err)
		}
	}
	read, err = s.GetWebhookSubscription(ctx, user.ID, sub.ID)
	if err != nil || read.Status != store.WebhookPaused || read.PausedReason == nil || *read.PausedReason != store.WebhookPausedFailedDeliveries ||
		read.PausedAt == nil || read.ConsecutiveFailedEvents != 3 {
		t.Fatalf("paused = %+v, %v", read, err)
	}
	fo, err := s.WebhookFanOutTargets(ctx, f.event.ID)
	if err != nil || len(fo.Targets) != 0 {
		t.Errorf("a paused subscription is not a fan-out target: %+v, %v", fo.Targets, err)
	}

	got, err := s.RecordWebhookEventFailed(ctx, sub.ID, 3)
	if err != nil || got.Paused || got.ConsecutiveFailedEvents != 4 {
		t.Errorf("a late failure while paused does not pause again: %+v, %v", got, err)
	}
	pausedAt := *read.PausedAt
	paused := store.WebhookPaused
	read, err = s.UpdateWebhookSubscription(ctx, user.ID, sub.ID, store.WebhookSubscriptionUpdate{Status: &paused})
	if err != nil || *read.PausedReason != store.WebhookPausedFailedDeliveries || !read.PausedAt.Equal(pausedAt) {
		t.Errorf("pausing again keeps why and when: %+v, %v", read, err)
	}

	active := store.WebhookActive
	read, err = s.UpdateWebhookSubscription(ctx, user.ID, sub.ID, store.WebhookSubscriptionUpdate{Status: &active})
	if err != nil || read.Status != store.WebhookActive || read.PausedReason != nil || read.PausedAt != nil || read.ConsecutiveFailedEvents != 0 {
		t.Fatalf("resumed = %+v, %v", read, err)
	}
	var reason *string
	if err := pool.QueryRow(ctx, `SELECT paused_reason FROM webhook_subscriptions WHERE id = $1`, sub.ID).Scan(&reason); err != nil || reason != nil {
		t.Errorf("stored reason after resuming = %v, %v", reason, err)
	}

	read, err = s.UpdateWebhookSubscription(ctx, user.ID, sub.ID, store.WebhookSubscriptionUpdate{Status: &paused})
	if err != nil || read.PausedReason == nil || *read.PausedReason != store.WebhookPausedByOwner || read.PausedAt == nil {
		t.Errorf("paused by the owner = %+v, %v", read, err)
	}
	url := "https://hooks.example/pause-2"
	read, err = s.UpdateWebhookSubscription(ctx, user.ID, sub.ID, store.WebhookSubscriptionUpdate{URL: &url})
	if err != nil || read.Status != store.WebhookPaused || *read.PausedReason != store.WebhookPausedByOwner {
		t.Errorf("changing the URL does not resume: %+v, %v", read, err)
	}

	if _, err := s.RecordWebhookEventFailed(ctx, uuid.New(), 3); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown subscription: %v", err)
	}
	if err := s.RecordWebhookEventDelivered(ctx, uuid.New()); err != nil {
		t.Errorf("unknown subscription delivered: %v", err)
	}
}

func TestFailedWebhookEventsAreCountedOnceEachUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	_, sub := paidSubscription(t, pool, f.slug, "https://hooks.example/concurrent", true)

	const n = 8
	results := make(chan store.WebhookEventFailure, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			got, err := s.RecordWebhookEventFailed(ctx, sub.ID, 5)
			results <- got
			errs <- err
		}()
	}
	pauses := 0
	var counts []int
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		got := <-results
		counts = append(counts, got.ConsecutiveFailedEvents)
		if got.Paused {
			pauses++
		}
	}
	slices.Sort(counts)
	if !slices.Equal(counts, []int{1, 2, 3, 4, 5, 6, 7, 8}) || pauses != 1 {
		t.Errorf("counts = %v, pauses = %d", counts, pauses)
	}
}

func TestAPausedRowWithoutAReasonReadsAsPausedByItsOwner(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example/legacy", true)
	if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET status = 'paused', paused_reason = NULL, paused_at = NULL WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	read, err := s.GetWebhookSubscription(ctx, user.ID, sub.ID)
	if err != nil || read.PausedReason == nil || *read.PausedReason != store.WebhookPausedByOwner || read.PausedAt == nil || !read.PausedAt.Equal(read.UpdatedAt) {
		t.Errorf("legacy paused row = %+v, %v", read, err)
	}
}
