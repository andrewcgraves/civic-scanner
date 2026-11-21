//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks/webhookstest"
)

func TestWebhookDeliveryLogAndTestSendAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	db := store.New(pool)
	sealer, err := webhooks.NewSealer(config.NewSecret(string(bytes.Repeat([]byte{9}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	flags := func(_ context.Context, flag string) bool {
		return slices.Contains([]string{httpapi.FlagAccounts, httpapi.FlagWebhookSubscriptions}, flag)
	}
	receiver := webhookstest.NewReceiver(t, "", nil)
	s := newAuthServer(t, flags,
		httpapi.WithWebhookSealer(sealer),
		httpapi.WithWebhookAddressCheck(webhooks.AddressCheck{Allow: netip.Addr.IsLoopback}),
		httpapi.WithWebhookSender(webhooks.NewSender("civic-scanner-test", receiver.SenderOptions()...)),
	)
	strangerID := "user_integration_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, strangerID)
	})
	owner := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))
	stranger := signedToken(t, s.key, "k1", s.claims(strangerID, 5*time.Minute))

	slug := "civ53-" + uuid.NewString()
	var agencyID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agencies (slug, name, status, website) VALUES ($1, 'Log Agency', 'live', 'https://example.com') RETURNING id`, slug).Scan(&agencyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.WithoutCancel(ctx)
		_, _ = pool.Exec(bg, `DELETE FROM meeting_events WHERE meeting_id IN (SELECT id FROM meetings WHERE agency_id = $1)`, agencyID)
		_, _ = pool.Exec(bg, `DELETE FROM meetings WHERE agency_id = $1`, agencyID)
		_, _ = pool.Exec(bg, `DELETE FROM agencies WHERE id = $1`, agencyID)
	})
	base := s.url + "/v1/webhook-subscriptions"
	for _, token := range []string{owner, stranger} {
		if status, _, raw := callJSON(t, http.MethodGet, base, token, ""); status != http.StatusPaymentRequired {
			t.Fatalf("free user: %d %s", status, raw)
		}
	}
	for _, workosID := range []string{s.users, strangerID} {
		if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'paid' WHERE workos_user_id = $1`, workosID); err != nil {
			t.Fatal(err)
		}
	}

	status, _, raw := callJSON(t, http.MethodPost, base, owner, `{"agency":"`+slug+`","url":"`+receiver.URL()+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, raw)
	}
	var created v1.WebhookSubscriptionWithSecret
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	receiver.SetSecret(created.Secret)
	one := base + "/" + created.Id.String()

	status, header, raw := callJSON(t, http.MethodPost, one+"/test", owner, `{"type":"agenda_changed"}`)
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("test: %d %s", status, raw)
	}
	var result v1.WebhookTestResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded || result.StatusCode == nil || *result.StatusCode != http.StatusNoContent || result.Type != v1.WebhookAgendaChanged {
		t.Fatalf("test result = %s", raw)
	}
	deliveries := receiver.Deliveries()
	if len(deliveries) != 1 || deliveries[0].VerifyErr != nil || deliveries[0].Header.Get("webhook-id") != result.Id.String() {
		t.Fatalf("receiver got %+v", deliveries)
	}
	var body v1.WebhookEventV1
	if err := json.Unmarshal(deliveries[0].Body, &body); err != nil || body.Test == nil || !*body.Test || body.Agency.Slug != slug || body.Agency.Name != "Log Agency" {
		t.Fatalf("test body = %s, %v", deliveries[0].Body, err)
	}

	if status, _, raw := callJSON(t, http.MethodPost, one+"/test", stranger, ""); status != http.StatusNotFound {
		t.Errorf("stranger's test: %d %s", status, raw)
	}
	if status, _, raw := callJSON(t, http.MethodGet, one+"/deliveries", stranger, ""); status != http.StatusNotFound {
		t.Errorf("stranger's log: %d %s", status, raw)
	}
	if len(receiver.Deliveries()) != 1 {
		t.Errorf("the stranger's test was sent")
	}

	status, _, raw = callJSON(t, http.MethodGet, one+"/deliveries", owner, "")
	if status != http.StatusOK || string(bytes.TrimSpace(raw)) != `{"deliveries":[],"nextCursor":null}` {
		t.Fatalf("log after a test only: %d %s", status, raw)
	}

	meeting, _, err := db.UpsertMeeting(ctx, store.MeetingInput{
		AgencyID: agencyID, Title: "Board", StartsAt: time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC), Status: "scheduled", SourceID: "civ53",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []store.MeetingEvent
	for range 3 {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			e, err := db.RecordMeetingEventTx(ctx, tx, store.MeetingEventInput{
				Type: store.EventTranscriptReady, MeetingID: meeting.ID,
				Payload: store.TranscriptEventPayload{TranscriptID: uuid.New(), RecordingID: uuid.New()},
			})
			events = append(events, e)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i, e := range events {
		for attempt := 1; attempt <= i+1; attempt++ {
			if err := db.RecordWebhookAttempt(ctx, store.WebhookAttemptInput{
				SubscriptionID: created.Id, EventID: e.ID, Attempt: attempt, StartedAt: at.Add(time.Duration(i)*time.Minute + time.Duration(attempt)*time.Second),
				Duration: 20 * time.Millisecond, StatusCode: 500,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	var listed []v1.WebhookDelivery
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		url := one + "/deliveries?limit=2"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		status, _, raw := callJSON(t, http.MethodGet, url, owner, "")
		if status != http.StatusOK {
			t.Fatalf("log page %d: %d %s", pages, status, raw)
		}
		var page v1.WebhookDeliveryList
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		listed = append(listed, page.Deliveries...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d deliveries", len(listed))
	}
	for i, d := range listed {
		want := events[len(events)-1-i]
		if d.EventId != want.ID || d.Type != v1.WebhookTranscriptReady || d.Meeting.Id != meeting.ID || d.Succeeded || len(d.Attempts) != len(events)-i {
			t.Errorf("delivery %d = %+v", i, d)
		}
	}

	if status, _, raw := callJSON(t, http.MethodGet, one+"/deliveries?cursor=bogus", owner, ""); status != http.StatusBadRequest {
		t.Errorf("bad cursor: %d %s", status, raw)
	}

	status, _, raw = callJSON(t, http.MethodPost, one+"/secret", owner, "")
	if status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, raw)
	}
	var rotated v1.WebhookSubscriptionWithSecret
	if err := json.Unmarshal(raw, &rotated); err != nil {
		t.Fatal(err)
	}
	status, _, raw = callJSON(t, http.MethodPost, one+"/test", owner, "")
	if err := json.Unmarshal(raw, &result); err != nil || status != http.StatusOK || result.Succeeded || result.StatusCode == nil || *result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("test with the old secret at the receiver: %d %s", status, raw)
	}
	receiver.SetSecret(rotated.Secret)
	status, _, raw = callJSON(t, http.MethodPost, one+"/test", owner, "")
	if err := json.Unmarshal(raw, &result); err != nil || status != http.StatusOK || !result.Succeeded {
		t.Fatalf("test with the new secret: %d %s", status, raw)
	}

	if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET url = 'https://10.0.0.5/hook' WHERE id = $1`, created.Id); err != nil {
		t.Fatal(err)
	}
	sent := len(receiver.Deliveries())
	status, _, raw = callJSON(t, http.MethodPost, one+"/test", owner, "")
	if err := json.Unmarshal(raw, &result); err != nil || status != http.StatusOK || result.Succeeded || result.Error == nil || result.StatusCode != nil {
		t.Fatalf("test to a private address: %d %s", status, raw)
	}
	if len(receiver.Deliveries()) != sent {
		t.Error("a refused test reached the receiver")
	}
}
