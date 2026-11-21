//go:build integration

package store_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func recordAttempt(t *testing.T, s *store.Store, subID, eventID uuid.UUID, attempt int, at time.Time, status int, succeeded bool) {
	t.Helper()
	in := store.WebhookAttemptInput{
		SubscriptionID: subID, EventID: eventID, Attempt: attempt, StartedAt: at,
		Duration: time.Duration(attempt) * 100 * time.Millisecond, StatusCode: status, Succeeded: succeeded,
	}
	if status == 0 {
		in.Error = "the endpoint did not answer in time"
	}
	if err := s.RecordWebhookAttempt(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookDeliveryLogGroupsAttemptsNewestFirst(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/log", true)
	second := recordEvent(t, pool, f.meeting.ID)
	third := recordEvent(t, pool, f.meeting.ID)
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	recordAttempt(t, s, sub.ID, f.event.ID, 1, base, 500, false)
	recordAttempt(t, s, sub.ID, f.event.ID, 2, base.Add(16*time.Second), 0, false)
	recordAttempt(t, s, sub.ID, f.event.ID, 3, base.Add(time.Hour), 204, true)
	recordAttempt(t, s, sub.ID, second.ID, 1, base.Add(time.Minute), 410, false)
	recordAttempt(t, s, sub.ID, third.ID, 1, base.Add(2*time.Minute), 200, true)

	page, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" || len(page.Deliveries) != 3 {
		t.Fatalf("page = %d deliveries, cursor %q", len(page.Deliveries), page.NextCursor)
	}
	order := []uuid.UUID{third.ID, second.ID, f.event.ID}
	for i, d := range page.Deliveries {
		if d.EventID != order[i] {
			t.Errorf("delivery %d is event %s, want %s", i, d.EventID, order[i])
		}
	}
	first := page.Deliveries[2]
	if first.EventType != store.EventAgendaPublished || first.Meeting.ID != f.meeting.ID || first.Meeting.Title != "Board" ||
		first.Meeting.SourceURL != "https://agency.example/m/1" || !first.Meeting.StartsAt.Equal(f.meeting.StartsAt) {
		t.Errorf("first delivery event and meeting = %+v", first)
	}
	if !first.Succeeded || !first.FirstAttemptAt.Equal(base) || !first.LastAttemptAt.Equal(base.Add(time.Hour)) || len(first.Attempts) != 3 {
		t.Fatalf("first delivery = %+v", first)
	}
	for i, a := range first.Attempts {
		if a.Attempt != i+1 || a.SubscriptionID != sub.ID || a.EventID != f.event.ID || a.Duration != time.Duration(i+1)*100*time.Millisecond {
			t.Errorf("attempt %d = %+v", i, a)
		}
	}
	if a := first.Attempts[1]; a.StatusCode != nil || a.Error == nil || *a.Error != "the endpoint did not answer in time" || a.Succeeded {
		t.Errorf("timed-out attempt = %+v", a)
	}
	if a := first.Attempts[0]; a.StatusCode == nil || *a.StatusCode != 500 || a.Error != nil {
		t.Errorf("500 attempt = %+v", a)
	}
	if d := page.Deliveries[1]; d.Succeeded || len(d.Attempts) != 1 || !d.FirstAttemptAt.Equal(d.LastAttemptAt) {
		t.Errorf("failed delivery = %+v", d)
	}
}

func TestWebhookDeliveryLogPagesWithoutSkipsOrRepeats(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/pages", true)
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	events := []uuid.UUID{f.event.ID}
	for range 4 {
		events = append(events, recordEvent(t, pool, f.meeting.ID).ID)
	}
	for i, id := range events {
		at := base.Add(time.Duration(i/2) * time.Minute)
		recordAttempt(t, s, sub.ID, id, 1, at, 500, false)
	}

	seen := map[uuid.UUID]bool{}
	var got []uuid.UUID
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		page, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if pages == 0 {
			late := recordEvent(t, pool, f.meeting.ID)
			recordAttempt(t, s, sub.ID, late.ID, 1, base.Add(time.Hour), 200, true)
			recordAttempt(t, s, sub.ID, events[0], 2, base.Add(2*time.Hour), 200, true)
		}
		for _, d := range page.Deliveries {
			if seen[d.EventID] {
				t.Errorf("event %s repeated", d.EventID)
			}
			seen[d.EventID] = true
			got = append(got, d.EventID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(got) != len(events) {
		t.Fatalf("listed %d deliveries, want %d", len(got), len(events))
	}
	for _, id := range events {
		if !seen[id] {
			t.Errorf("event %s skipped", id)
		}
	}
	want := slices.Clone(events)
	slices.SortStableFunc(want, func(a, b uuid.UUID) int {
		ia, ib := slices.Index(events, a)/2, slices.Index(events, b)/2
		if ia != ib {
			return ib - ia
		}
		return bytes.Compare(b[:], a[:])
	})
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestWebhookDeliveryLogIsTheOwnersAlone(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	owner, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/mine", true)
	other, otherSub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/theirs", true)
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	recordAttempt(t, s, sub.ID, f.event.ID, 1, at, 200, true)
	recordAttempt(t, s, otherSub.ID, f.event.ID, 1, at, 500, false)

	if _, err := s.ListWebhookDeliveries(ctx, other.ID, sub.ID, store.ListWebhookDeliveriesParams{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's log: err = %v", err)
	}
	if _, err := s.ListWebhookDeliveries(ctx, owner.ID, uuid.New(), store.ListWebhookDeliveriesParams{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown subscription: err = %v", err)
	}
	page, err := s.ListWebhookDeliveries(ctx, owner.ID, sub.ID, store.ListWebhookDeliveriesParams{})
	if err != nil || len(page.Deliveries) != 1 || len(page.Deliveries[0].Attempts) != 1 || !page.Deliveries[0].Succeeded {
		t.Fatalf("owner's log = %+v, %v", page, err)
	}

	empty, err := s.ListWebhookDeliveries(ctx, other.ID, otherSub.ID, store.ListWebhookDeliveriesParams{Limit: 1})
	if err != nil || len(empty.Deliveries) != 1 || empty.NextCursor != "" {
		t.Fatalf("other's log = %+v, %v", empty, err)
	}
}

func TestWebhookDeliveryLogRefusesForeignCursors(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/a", true)
	_, otherSub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/b", true)
	second := recordEvent(t, pool, f.meeting.ID)
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	recordAttempt(t, s, sub.ID, f.event.ID, 1, at, 200, true)
	recordAttempt(t, s, sub.ID, second.ID, 1, at.Add(time.Second), 200, true)

	page, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	for name, cursor := range map[string]string{
		"garbage":  "not-a-cursor",
		"altered":  page.NextCursor[:len(page.NextCursor)-2] + "AA",
		"short":    "AAAA",
		"too long": page.NextCursor + "A",
	} {
		if _, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{Cursor: cursor}); !errors.Is(err, store.ErrInvalidCursor) {
			t.Errorf("%s cursor: err = %v", name, err)
		}
	}
	if _, err := s.ListWebhookDeliveries(ctx, user.ID, otherSub.ID, store.ListWebhookDeliveriesParams{Cursor: page.NextCursor}); !errors.Is(err, store.ErrInvalidCursor) {
		t.Errorf("another subscription's cursor: err = %v", err)
	}
	next, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{Cursor: page.NextCursor, Limit: 1})
	if err != nil || len(next.Deliveries) != 1 || next.Deliveries[0].EventID != f.event.ID || next.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", next, err)
	}
}

func TestWebhookDeliveryLogClampsTheLimit(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	f := seedDeliveryFixture(t, pool)
	user, sub := paidSubscription(t, pool, f.slug, "https://hooks.example.com/clamp", true)
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	events := []uuid.UUID{f.event.ID}
	for range store.DefaultWebhookDeliveriesLimit {
		events = append(events, recordEvent(t, pool, f.meeting.ID).ID)
	}
	for i, id := range events {
		recordAttempt(t, s, sub.ID, id, 1, at.Add(time.Duration(i)*time.Second), 200, true)
	}
	page, err := s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{})
	if err != nil || len(page.Deliveries) != store.DefaultWebhookDeliveriesLimit || page.NextCursor == "" {
		t.Fatalf("default page = %d deliveries, cursor %q, %v", len(page.Deliveries), page.NextCursor, err)
	}
	page, err = s.ListWebhookDeliveries(ctx, user.ID, sub.ID, store.ListWebhookDeliveriesParams{Limit: store.MaxWebhookDeliveriesLimit + 50})
	if err != nil || len(page.Deliveries) != len(events) || page.NextCursor != "" {
		t.Fatalf("clamped page = %d deliveries, cursor %q, %v", len(page.Deliveries), page.NextCursor, err)
	}
}
