//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks/webhookstest"
)

func webhookSealer(t *testing.T) *webhooks.Sealer {
	t.Helper()
	s, err := webhooks.NewSealer(config.NewSecret(string(bytes.Repeat([]byte{9}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type subscriber struct {
	user     store.User
	sub      store.WebhookSubscription
	secret   config.Secret
	receiver *webhookstest.Receiver
}

func subscribe(t *testing.T, pool *pgxpool.Pool, sealer *webhooks.Sealer, slug string, paid bool, respond func(int) int) subscriber {
	t.Helper()
	ctx := t.Context()
	s := store.New(pool)
	workOSID := "user_jobs_webhook_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, workOSID)
	})
	user, _, err := s.EnsureUser(ctx, store.UserInput{WorkOSUserID: workOSID})
	if err != nil {
		t.Fatal(err)
	}
	if paid {
		if _, err := pool.Exec(ctx, `UPDATE users SET plan = 'paid' WHERE id = $1`, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	secret := webhooks.NewSecret()
	receiver := webhookstest.NewReceiver(t, secret.Reveal(), respond)
	id := uuid.New()
	sealed, err := sealer.Seal(id, secret)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateWebhookSubscription(ctx, store.WebhookSubscriptionInput{
		ID: id, UserID: user.ID, AgencySlug: slug, URL: receiver.URL(), PayloadVersion: store.WebhookPayloadV1, SecretCiphertext: sealed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return subscriber{user: user, sub: sub, secret: secret, receiver: receiver}
}

func recordAgendaChange(t *testing.T, pool *pgxpool.Pool, meetingID uuid.UUID, note string) store.MeetingEvent {
	t.Helper()
	var e store.MeetingEvent
	found := fixedNow.Add(-time.Hour)
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		e, err = store.New(pool).RecordMeetingEventTx(t.Context(), tx, store.MeetingEventInput{
			Type:      store.EventAgendaChanged,
			MeetingID: meetingID,
			Payload: store.AgendaEventPayload{
				AgendaVersionID: uuid.New(), Version: 2, FoundAt: &found,
				ChangeNote: &note, NoteSource: store.NoteFromDiff,
			},
			OccurredAt: found,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (h *harness) fanOut(eventID uuid.UUID) {
	h.t.Helper()
	res, err := h.client.Insert(h.t.Context(), jobs.FanOutWebhooksArgs{EventID: eventID}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		h.t.Fatal(err)
	}
	testdb.WaitForJobState(h.t, h.pool, res.Job.ID, "completed")
}

func (h *harness) deliveryJobs(eventID uuid.UUID) map[uuid.UUID]int64 {
	h.t.Helper()
	rows, err := h.pool.Query(h.t.Context(), `SELECT id, args->>'subscription_id' FROM river_job
WHERE kind = 'deliver_webhook' AND queue = $1 AND args->>'event_id' = $2`, h.webhookQueue, eventID.String())
	if err != nil {
		h.t.Fatal(err)
	}
	jobsBySub := map[uuid.UUID]int64{}
	for rows.Next() {
		var (
			id  int64
			sub uuid.UUID
		)
		if err := rows.Scan(&id, &sub); err != nil {
			h.t.Fatal(err)
		}
		jobsBySub[sub] = id
	}
	if err := rows.Err(); err != nil {
		h.t.Fatal(err)
	}
	return jobsBySub
}

func TestMeetingEventIsDeliveredSignedToEachEnabledSubscriber(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhooks-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	meeting := seedMeeting(t, pool, agencyID, "webhooks")
	otherSlug := "jobs-test-webhooks-other-" + randomSuffix(t)
	seedAgency(t, pool, otherSlug, "live")

	first := subscribe(t, pool, sealer, slug, true, nil)
	second := subscribe(t, pool, sealer, slug, true, func(int) int { return http.StatusOK })
	paused := subscribe(t, pool, sealer, slug, true, nil)
	pausedStatus := store.WebhookPaused
	if _, err := store.New(pool).UpdateWebhookSubscription(ctx, paused.user.ID, paused.sub.ID, store.WebhookSubscriptionUpdate{Status: &pausedStatus}); err != nil {
		t.Fatal(err)
	}
	free := subscribe(t, pool, sealer, slug, false, nil)
	granted := subscribe(t, pool, sealer, slug, false, nil)
	flagOff := subscribe(t, pool, sealer, slug, true, nil)
	elsewhere := subscribe(t, pool, sealer, otherSlug, true, nil)

	h := start(t, pool, jobs.Config{
		Enabled: func(ctx context.Context, flag string) bool {
			key := openfeature.TransactionContext(ctx).TargetingKey()
			switch flag {
			case jobs.FlagWebhookSubscriptions:
				return key != flagOff.user.WorkOSUserID
			case jobs.FlagWebhookSubscriptionsGranted:
				return key == granted.user.WorkOSUserID
			}
			return true
		},
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test", first.receiver.SenderOptions()...)},
	})

	event := recordAgendaChange(t, pool, meeting.ID, "Added: “14. Adopt the 2027 calendar”")
	h.fanOut(event.ID)

	deliveries := h.deliveryJobs(event.ID)
	if len(deliveries) != 3 || deliveries[first.sub.ID] == 0 || deliveries[second.sub.ID] == 0 || deliveries[granted.sub.ID] == 0 {
		t.Fatalf("delivery jobs = %v, want one each for %s, %s and %s", deliveries, first.sub.ID, second.sub.ID, granted.sub.ID)
	}
	for _, id := range deliveries {
		testdb.WaitForJobState(t, pool, id, "completed")
	}

	for _, s := range []subscriber{first, second, granted} {
		got := s.receiver.Deliveries()
		if len(got) != 1 {
			t.Fatalf("subscription %s got %d deliveries", s.sub.ID, len(got))
		}
		d := got[0]
		if d.VerifyErr != nil || d.Header.Get("webhook-id") != event.ID.String() || !d.Timestamp.Equal(fixedNow) {
			t.Errorf("delivery = %+v", d)
		}
		var body struct {
			ID             uuid.UUID `json:"id"`
			Type           string    `json:"type"`
			PayloadVersion string    `json:"payloadVersion"`
			OccurredAt     time.Time `json:"occurredAt"`
			Agency         struct{ Slug string }
			Meeting        struct {
				ID        uuid.UUID
				Title     string
				SourceURL string `json:"sourceUrl"`
			}
			Agenda struct {
				Version          int
				ChangeNote       string `json:"changeNote"`
				ChangeNoteSource string `json:"changeNoteSource"`
			}
		}
		if err := json.Unmarshal(d.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.ID != event.ID || body.Type != "agenda_changed" || body.PayloadVersion != "v1" || !body.OccurredAt.Equal(event.OccurredAt) ||
			body.Agency.Slug != slug || body.Meeting.ID != meeting.ID || body.Meeting.SourceURL != meeting.SourceURL ||
			body.Agenda.Version != 2 || body.Agenda.ChangeNote != "Added: “14. Adopt the 2027 calendar”" || body.Agenda.ChangeNoteSource != "diff" {
			t.Errorf("body = %s", d.Body)
		}
		attempts, err := store.New(pool).ListWebhookAttempts(ctx, s.sub.ID)
		if err != nil || len(attempts) != 1 || !attempts[0].Succeeded || attempts[0].Attempt != 1 || attempts[0].EventID != event.ID ||
			attempts[0].StatusCode == nil || !attempts[0].StartedAt.Equal(fixedNow) {
			t.Errorf("attempts = %+v, %v", attempts, err)
		}
	}
	if got := *mustAttempts(t, pool, first.sub.ID)[0].StatusCode; got != http.StatusNoContent {
		t.Errorf("first status = %d", got)
	}
	for name, s := range map[string]subscriber{"paused": paused, "free": free, "flag off": flagOff, "other agency": elsewhere} {
		if n := len(s.receiver.Deliveries()); n != 0 {
			t.Errorf("%s subscription got %d deliveries", name, n)
		}
	}

	var fanned int
	if err := pool.QueryRow(ctx, `SELECT deliveries FROM webhook_event_fan_outs WHERE event_id = $1`, event.ID).Scan(&fanned); err != nil || fanned != 3 {
		t.Errorf("fan-out row deliveries = %d, %v", fanned, err)
	}
	res, err := h.client.Insert(ctx, jobs.FanOutWebhooksArgs{EventID: event.ID}, &river.InsertOpts{Queue: h.queue})
	if err != nil {
		t.Fatal(err)
	}
	if !res.UniqueSkippedAsDuplicate {
		testdb.WaitForJobState(t, pool, res.Job.ID, "completed")
	}
	if got := h.deliveryJobs(event.ID); len(got) != 3 {
		t.Errorf("a second fan-out added deliveries: %v", got)
	}
}

func mustAttempts(t *testing.T, pool *pgxpool.Pool, subID uuid.UUID) []store.WebhookAttempt {
	t.Helper()
	attempts, err := store.New(pool).ListWebhookAttempts(t.Context(), subID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("attempts = %d, %v", len(attempts), err)
	}
	return attempts
}

func TestFailedWebhookDeliveryIsRetriedAndEveryAttemptStored(t *testing.T) {
	pool := testdb.Pool(t)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhook-retry-" + randomSuffix(t)
	meeting := seedMeeting(t, pool, seedAgency(t, pool, slug, "live"), "webhook-retry")
	s := subscribe(t, pool, sealer, slug, true, func(n int) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusAccepted
	})
	h := start(t, pool, jobs.Config{
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test", s.receiver.SenderOptions()...)},
	})
	event := recordAgendaChange(t, pool, meeting.ID, "Changed the time")
	h.fanOut(event.ID)
	deliveries := h.deliveryJobs(event.ID)
	testdb.WaitForJobState(t, pool, deliveries[s.sub.ID], "completed")

	got := s.receiver.Deliveries()
	if len(got) != 2 || got[0].Header.Get("webhook-id") != got[1].Header.Get("webhook-id") || got[0].VerifyErr != nil || got[1].VerifyErr != nil {
		t.Fatalf("deliveries = %+v", got)
	}
	attempts := mustAttempts(t, pool, s.sub.ID)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %+v", attempts)
	}
	retried, failed := attempts[0], attempts[1]
	if failed.Succeeded || failed.Attempt != 1 || *failed.StatusCode != http.StatusServiceUnavailable || *failed.Error != "the endpoint answered 503" {
		t.Errorf("failed attempt = %+v", failed)
	}
	if !retried.Succeeded || retried.Attempt != 2 || *retried.StatusCode != http.StatusAccepted || retried.Error != nil {
		t.Errorf("retried attempt = %+v", retried)
	}
}

func TestWebhookToANonPublicAddressIsCancelledAndStored(t *testing.T) {
	pool := testdb.Pool(t)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhook-refused-" + randomSuffix(t)
	meeting := seedMeeting(t, pool, seedAgency(t, pool, slug, "live"), "webhook-refused")
	s := subscribe(t, pool, sealer, slug, true, nil)
	h := start(t, pool, jobs.Config{
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test")},
	})
	event := recordAgendaChange(t, pool, meeting.ID, "Changed the room")
	h.fanOut(event.ID)
	testdb.WaitForJobState(t, pool, h.deliveryJobs(event.ID)[s.sub.ID], "cancelled")

	if n := len(s.receiver.Deliveries()); n != 0 {
		t.Errorf("deliveries = %d", n)
	}
	attempts := mustAttempts(t, pool, s.sub.ID)
	if len(attempts) != 1 || attempts[0].Succeeded || attempts[0].StatusCode != nil || attempts[0].Error == nil {
		t.Fatalf("attempts = %+v", attempts)
	}
	if want := "address is not a public internet address"; !bytes.Contains([]byte(*attempts[0].Error), []byte(want)) {
		t.Errorf("error = %q", *attempts[0].Error)
	}
}

func TestWebhookRetryIsScheduledOnTheBackoff(t *testing.T) {
	pool := testdb.Pool(t)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhook-backoff-" + randomSuffix(t)
	meeting := seedMeeting(t, pool, seedAgency(t, pool, slug, "live"), "webhook-backoff")
	s := subscribe(t, pool, sealer, slug, true, func(int) int { return http.StatusServiceUnavailable })
	now := time.Now().UTC()
	h := start(t, pool, jobs.Config{
		Clock:    sourcestest.NewClock(now),
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test", s.receiver.SenderOptions()...)},
	})
	event := recordAgendaChange(t, pool, meeting.ID, "Changed the agenda")
	h.fanOut(event.ID)
	job := h.waitForError(h.deliveryJobs(event.ID)[s.sub.ID])
	if job.State != rivertype.JobStateRetryable || job.MaxAttempts != 8 {
		t.Fatalf("job = %s, max attempts %d", job.State, job.MaxAttempts)
	}
	if wait := job.ScheduledAt.Sub(now); wait < 54*time.Second || wait > 66*time.Second {
		t.Errorf("first retry in %v, want about a minute", wait)
	}
	read, err := store.New(pool).GetWebhookSubscription(t.Context(), s.user.ID, s.sub.ID)
	if err != nil || read.ConsecutiveFailedEvents != 0 || read.Status != store.WebhookActive {
		t.Errorf("an event still being retried is not counted: %+v, %v", read, err)
	}
}

func (h *harness) exhaust(id int64) {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		err := h.pool.QueryRow(h.t.Context(), `UPDATE river_job SET state = CASE WHEN state = 'retryable' THEN 'available' ELSE state END,
    scheduled_at = CASE WHEN state = 'retryable' THEN now() ELSE scheduled_at END
WHERE id = $1 RETURNING state`, id).Scan(&state)
		if err != nil {
			h.t.Fatal(err)
		}
		if state == "discarded" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("job %d was not discarded after 60s", id)
}

func TestWebhookSubscriptionPausesAfterFiveFailedEventsUntilResumed(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	st := store.New(pool)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhook-pause-" + randomSuffix(t)
	meeting := seedMeeting(t, pool, seedAgency(t, pool, slug, "live"), "webhook-pause")
	var (
		mu      sync.Mutex
		failing = true
	)
	respond := func(fail bool) {
		mu.Lock()
		defer mu.Unlock()
		failing = fail
	}
	s := subscribe(t, pool, sealer, slug, true, func(int) int {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return http.StatusInternalServerError
		}
		return http.StatusNoContent
	})
	h := start(t, pool, jobs.Config{
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test", s.receiver.SenderOptions()...)},
	})
	event := func(note string) (store.MeetingEvent, int64) {
		t.Helper()
		e := recordAgendaChange(t, pool, meeting.ID, note)
		h.fanOut(e.ID)
		return e, h.deliveryJobs(e.ID)[s.sub.ID]
	}
	subscription := func() store.WebhookSubscription {
		t.Helper()
		read, err := st.GetWebhookSubscription(ctx, s.user.ID, s.sub.ID)
		if err != nil {
			t.Fatal(err)
		}
		return read
	}
	earlierFailures := func(n int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE webhook_subscriptions SET consecutive_failed_events = $2 WHERE id = $1`, s.sub.ID, n); err != nil {
			t.Fatal(err)
		}
	}

	earlierFailures(3)
	_, id := event("fourth failure")
	h.exhaust(id)
	if read := subscription(); read.Status != store.WebhookActive || read.ConsecutiveFailedEvents != 4 {
		t.Fatalf("after the fourth failed event: %+v", read)
	}

	respond(false)
	_, id = event("delivered")
	testdb.WaitForJobState(t, pool, id, "completed")
	if read := subscription(); read.Status != store.WebhookActive || read.ConsecutiveFailedEvents != 0 {
		t.Fatalf("a delivered event resets the count: %+v", read)
	}

	respond(true)
	earlierFailures(4)
	last, id := event("fifth failure")
	h.exhaust(id)
	read := subscription()
	if read.Status != store.WebhookPaused || read.PausedReason == nil || *read.PausedReason != store.WebhookPausedFailedDeliveries ||
		read.PausedAt == nil || read.ConsecutiveFailedEvents != 5 {
		t.Fatalf("after the fifth failed event: %+v", read)
	}
	attempts, err := st.ListWebhookAttempts(ctx, s.sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	perEvent := 0
	for _, a := range attempts {
		if a.EventID == last.ID {
			perEvent++
		}
	}
	if perEvent != 8 {
		t.Errorf("attempts for the fifth failed event = %d, want 8", perEvent)
	}

	if _, id := event("while paused"); id != 0 {
		t.Error("a paused subscription was sent an event")
	}

	respond(false)
	active := store.WebhookActive
	if read, err := st.UpdateWebhookSubscription(ctx, s.user.ID, s.sub.ID, store.WebhookSubscriptionUpdate{Status: &active}); err != nil ||
		read.Status != store.WebhookActive || read.ConsecutiveFailedEvents != 0 || read.PausedReason != nil {
		t.Fatalf("resumed = %+v, %v", read, err)
	}
	_, id = event("after resuming")
	testdb.WaitForJobState(t, pool, id, "completed")
}
