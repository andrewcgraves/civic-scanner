package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-feature/go-sdk/openfeature"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

var webhookNow = time.Date(2026, 11, 18, 9, 30, 15, 0, time.UTC)

type fakeDeliveryStore struct {
	delivery   store.WebhookDelivery
	err        error
	recordErr  error
	attempts   []store.WebhookAttemptInput
	failure    store.WebhookEventFailure
	failureErr error
	failed     []int
	resetErr   error
	delivered  int
}

func (s *fakeDeliveryStore) RecordWebhookEventFailed(_ context.Context, _ uuid.UUID, pauseAfter int) (store.WebhookEventFailure, error) {
	s.failed = append(s.failed, pauseAfter)
	return s.failure, s.failureErr
}

func (s *fakeDeliveryStore) RecordWebhookEventDelivered(context.Context, uuid.UUID) error {
	s.delivered++
	return s.resetErr
}

func (s *fakeDeliveryStore) WebhookDelivery(context.Context, uuid.UUID, uuid.UUID) (store.WebhookDelivery, error) {
	return s.delivery, s.err
}

func (s *fakeDeliveryStore) RecordWebhookAttempt(_ context.Context, in store.WebhookAttemptInput) error {
	s.attempts = append(s.attempts, in)
	return s.recordErr
}

type fakeSender struct {
	result   webhooks.Result
	requests []webhooks.Request
}

func (s *fakeSender) Send(_ context.Context, req webhooks.Request) webhooks.Result {
	s.requests = append(s.requests, req)
	return s.result
}

func testWebhookSealer(t *testing.T) *webhooks.Sealer {
	t.Helper()
	s, err := webhooks.NewSealer(config.NewSecret(string(bytes.Repeat([]byte{7}, config.WebhookSecretKeyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func deliveryFixture(t *testing.T, sealer *webhooks.Sealer) store.WebhookDelivery {
	t.Helper()
	subID := uuid.New()
	sealed, err := sealer.Seal(subID, config.NewSecret("whsec_c2VjcmV0"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(store.AgendaEventPayload{AgendaVersionID: uuid.New(), Version: 1})
	return store.WebhookDelivery{
		SubscriptionID:   subID,
		UserID:           uuid.New(),
		WorkOSUserID:     "user_paid",
		Paid:             true,
		URL:              "https://hooks.example/a",
		PayloadVersion:   "v1",
		Status:           store.WebhookActive,
		SecretCiphertext: sealed,
		Event:            store.MeetingEvent{ID: uuid.New(), Type: store.EventAgendaPublished, Payload: payload, OccurredAt: webhookNow},
		AgencySlug:       "agency",
		AgencyName:       "Agency",
		Meeting:          store.Meeting{ID: uuid.New(), Title: "Board", StartsAt: webhookNow},
	}
}

func deliverWorker(t *testing.T, st *fakeDeliveryStore, sender *fakeSender, sealer *webhooks.Sealer) *DeliverWebhookWorker {
	t.Helper()
	return &DeliverWebhookWorker{
		logger:   discard,
		store:    st,
		webhooks: Webhooks{Sealer: sealer, Sender: sender},
		clock:    sourcestest.NewClock(webhookNow),
		enabled:  func(context.Context, string) bool { return true },
	}
}

func deliverJob() *river.Job[DeliverWebhookArgs] {
	return &river.Job[DeliverWebhookArgs]{JobRow: &rivertype.JobRow{ID: 3, Attempt: 2, MaxAttempts: webhookDeliverAttempts}}
}

func TestWebhookArgs(t *testing.T) {
	if kind := (DeliverWebhookArgs{}).Kind(); kind != "deliver_webhook" {
		t.Errorf("deliver Kind() = %q", kind)
	}
	if opts := (DeliverWebhookArgs{}).InsertOpts(); opts.MaxAttempts != 8 || opts.Queue != WebhookQueue || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Errorf("deliver InsertOpts() = %+v", opts)
	}
	if kind := (FanOutWebhooksArgs{}).Kind(); kind != "fan_out_webhooks" {
		t.Errorf("fan-out Kind() = %q", kind)
	}
	if opts := (FanOutWebhooksArgs{}).InsertOpts(); opts.MaxAttempts != 5 || !opts.UniqueOpts.ByArgs {
		t.Errorf("fan-out InsertOpts() = %+v", opts)
	}
	if kind := (EnqueueWebhookFanOutsArgs{}).Kind(); kind != "enqueue_webhook_fan_outs" {
		t.Errorf("sweep Kind() = %q", kind)
	}
	if opts := (EnqueueWebhookFanOutsArgs{}).InsertOpts(); opts.MaxAttempts != 1 || !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Errorf("sweep InsertOpts() = %+v", opts)
	}
	args, opts := periodicWebhookSweep(sourcestest.NewClock(webhookNow), "ingest")()
	if got, ok := args.(EnqueueWebhookFanOutsArgs); !ok || !got.Slot.Equal(time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)) || opts.Queue != "ingest" {
		t.Errorf("periodic constructor = %#v, %+v", args, opts)
	}
	if got := (&DeliverWebhookWorker{}).Timeout(nil); got != webhookDeliverTimeout {
		t.Errorf("Timeout = %v", got)
	}
}

func TestDeliverWebhookSendsASignedV1Body(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	sender := &fakeSender{result: webhooks.Result{StatusCode: http.StatusOK, Duration: 42 * time.Millisecond}}
	if err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob()); err != nil {
		t.Fatalf("Work = %v", err)
	}
	if len(sender.requests) != 1 {
		t.Fatalf("requests = %d", len(sender.requests))
	}
	req := sender.requests[0]
	if req.URL != st.delivery.URL || req.ID != st.delivery.Event.ID.String() || !req.At.Equal(webhookNow) || req.Secret.Reveal() != "whsec_c2VjcmV0" {
		t.Errorf("request = %+v", req)
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil || body["type"] != "agenda_published" || body["payloadVersion"] != "v1" {
		t.Errorf("body = %s, %v", req.Body, err)
	}
	want := store.WebhookAttemptInput{
		SubscriptionID: st.delivery.SubscriptionID, EventID: st.delivery.Event.ID, Attempt: 2,
		StartedAt: webhookNow, Duration: 42 * time.Millisecond, StatusCode: 200, Succeeded: true,
	}
	if len(st.attempts) != 1 || st.attempts[0] != want {
		t.Errorf("attempts = %+v, want %+v", st.attempts, want)
	}
	if st.delivered != 1 || len(st.failed) != 0 {
		t.Errorf("delivered %d, failed %v", st.delivered, st.failed)
	}
}

func TestDeliverWebhookSendsThePresetBodies(t *testing.T) {
	sealer := testWebhookSealer(t)
	for _, tc := range []struct {
		format store.WebhookFormat
		web    string
		link   string
	}{
		{store.WebhookFormatDiscord, "https://civic.example/", "https://civic.example/meetings/"},
		{store.WebhookFormatSlack, "https://civic.example", "https://civic.example/meetings/"},
		{store.WebhookFormatSlack, "", "https://agency.example/board"},
	} {
		st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
		st.delivery.Format = tc.format
		st.delivery.Meeting.SourceURL = "https://agency.example/board"
		sender := &fakeSender{result: webhooks.Result{StatusCode: http.StatusNoContent}}
		w := deliverWorker(t, st, sender, sealer)
		w.webhooks.PublicWebURL = tc.web
		if err := w.Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 1 {
			t.Fatalf("%s: Work = %v, requests %d", tc.format, err, len(sender.requests))
		}
		req := sender.requests[0]
		want, err := webhooks.Body(tc.format, "v1", "", webhooks.Event{
			ID: st.delivery.Event.ID, Type: st.delivery.Event.Type, Payload: st.delivery.Event.Payload, OccurredAt: st.delivery.Event.OccurredAt,
			AgencySlug: st.delivery.AgencySlug, AgencyName: st.delivery.AgencyName, Meeting: st.delivery.Meeting,
			PageURL: webhooks.MeetingPage(tc.web, st.delivery.Meeting),
		})
		if err != nil || !bytes.Equal(req.Body, want) || !strings.Contains(string(req.Body), tc.link) {
			t.Errorf("%s with web URL %q: body %s, %v", tc.format, tc.web, req.Body, err)
		}
		if req.ID != st.delivery.Event.ID.String() || req.Secret.Reveal() != "whsec_c2VjcmV0" {
			t.Errorf("%s: the preset is not signed like the standard body: %+v", tc.format, req)
		}
	}
}

func TestDeliverWebhookSkipsWhatShouldNotBeSent(t *testing.T) {
	sealer := testWebhookSealer(t)
	for name, change := range map[string]func(*fakeDeliveryStore, *DeliverWebhookWorker){
		"gone":   func(s *fakeDeliveryStore, _ *DeliverWebhookWorker) { s.err = store.ErrNotFound },
		"paused": func(s *fakeDeliveryStore, _ *DeliverWebhookWorker) { s.delivery.Status = store.WebhookPaused },
		"free plan": func(s *fakeDeliveryStore, w *DeliverWebhookWorker) {
			s.delivery.Paid = false
			w.enabled = func(_ context.Context, flag string) bool { return flag != FlagWebhookSubscriptionsGranted }
		},
		"granted but flag off": func(s *fakeDeliveryStore, w *DeliverWebhookWorker) {
			s.delivery.Paid = false
			w.enabled = func(_ context.Context, flag string) bool { return flag != FlagWebhookSubscriptions }
		},
		"flag off": func(_ *fakeDeliveryStore, w *DeliverWebhookWorker) {
			w.enabled = func(_ context.Context, flag string) bool { return flag != FlagWebhookSubscriptions }
		},
		"accounts off": func(_ *fakeDeliveryStore, w *DeliverWebhookWorker) {
			w.enabled = func(_ context.Context, flag string) bool { return flag != flagAccounts }
		},
	} {
		st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
		sender := &fakeSender{}
		w := deliverWorker(t, st, sender, sealer)
		change(st, w)
		if err := w.Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 0 || len(st.attempts) != 0 {
			t.Errorf("%s: Work = %v, requests %d, attempts %d", name, err, len(sender.requests), len(st.attempts))
		}
	}
}

func TestDeliverWebhookSendsToAGrantedFreeUser(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	st.delivery.Paid = false
	sender := &fakeSender{result: webhooks.Result{StatusCode: 204}}
	w := deliverWorker(t, st, sender, sealer)
	var granted []string
	w.enabled = func(ctx context.Context, flag string) bool {
		if flag == FlagWebhookSubscriptionsGranted {
			granted = append(granted, openfeature.TransactionContext(ctx).TargetingKey())
		}
		return true
	}
	if err := w.Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 1 {
		t.Fatalf("Work = %v, requests %d", err, len(sender.requests))
	}
	if len(granted) != 1 || granted[0] != st.delivery.WorkOSUserID {
		t.Errorf("grant checked for %v, want %s", granted, st.delivery.WorkOSUserID)
	}
}

func TestDeliverWebhookChecksFlagsForTheSubscriber(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	var identities []string
	w := deliverWorker(t, st, &fakeSender{result: webhooks.Result{StatusCode: 204}}, sealer)
	w.enabled = func(ctx context.Context, _ string) bool {
		identities = append(identities, openfeature.TransactionContext(ctx).TargetingKey())
		return true
	}
	if err := w.Work(t.Context(), deliverJob()); err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 || identities[0] != "user_paid" || identities[1] != "user_paid" {
		t.Errorf("identities = %q", identities)
	}
}

func TestDeliverWebhookClassifiesFailures(t *testing.T) {
	sealer := testWebhookSealer(t)
	refused := webhooks.Result{Err: errors.Join(errors.New("refused to connect to 10.0.0.1"), webhooks.ErrForbiddenAddress), Duration: time.Millisecond}
	for name, tc := range map[string]struct {
		result     webhooks.Result
		cancel     bool
		statusCode int
		problem    string
	}{
		"server error":    {webhooks.Result{StatusCode: 503}, false, 503, "the endpoint answered 503"},
		"client error":    {webhooks.Result{StatusCode: 404}, false, 404, "the endpoint answered 404"},
		"redirect":        {webhooks.Result{StatusCode: 302}, false, 302, "the endpoint answered 302"},
		"network":         {webhooks.Result{Err: errors.New("connection reset")}, false, 0, "connection reset"},
		"refused address": {refused, true, 0, refused.Err.Error()},
	} {
		st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
		err := deliverWorker(t, st, &fakeSender{result: tc.result}, sealer).Work(t.Context(), deliverJob())
		var cancel *river.JobCancelError
		if err == nil || errors.As(err, &cancel) != tc.cancel {
			t.Errorf("%s: Work = %v, want cancel %v", name, err, tc.cancel)
		}
		if len(st.attempts) != 1 || st.attempts[0].Succeeded || st.attempts[0].StatusCode != tc.statusCode || st.attempts[0].Error != tc.problem {
			t.Errorf("%s: attempts = %+v", name, st.attempts)
		}
		if wantFailed := tc.cancel; (len(st.failed) == 1) != wantFailed || st.delivered != 0 {
			t.Errorf("%s: failed events %v, delivered %d; a cancelled delivery is a failed event, a retried one is not yet", name, st.failed, st.delivered)
		}
	}
}

func TestDeliverWebhookCountsTheEventAfterTheLastAttempt(t *testing.T) {
	sealer := testWebhookSealer(t)
	for name, tc := range map[string]struct {
		failure store.WebhookEventFailure
		err     error
	}{
		"counted": {failure: store.WebhookEventFailure{ConsecutiveFailedEvents: 2}},
		"paused":  {failure: store.WebhookEventFailure{ConsecutiveFailedEvents: 5, Paused: true}},
		"gone":    {err: store.ErrNotFound},
		"db down": {err: errors.New("db down")},
	} {
		st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer), failure: tc.failure, failureErr: tc.err}
		job := deliverJob()
		job.Attempt = job.MaxAttempts
		err := deliverWorker(t, st, &fakeSender{result: webhooks.Result{StatusCode: 500}}, sealer).Work(t.Context(), job)
		var cancel *river.JobCancelError
		if err == nil || errors.As(err, &cancel) {
			t.Errorf("%s: Work = %v, want the delivery error", name, err)
		}
		if len(st.failed) != 1 || st.failed[0] != webhookPauseAfterFailures {
			t.Errorf("%s: failed events = %v", name, st.failed)
		}
	}
}

func TestDeliverWebhookResetErrorDoesNotRetryADeliveredEvent(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer), resetErr: errors.New("db down")}
	if err := deliverWorker(t, st, &fakeSender{result: webhooks.Result{StatusCode: 200}}, sealer).Work(t.Context(), deliverJob()); err != nil || st.delivered != 1 {
		t.Errorf("Work = %v, delivered %d", err, st.delivered)
	}
}

func TestWebhookRetryDelaySpansADay(t *testing.T) {
	want := []time.Duration{time.Minute, 4 * time.Minute, 16 * time.Minute, 64 * time.Minute, 256 * time.Minute, 9 * time.Hour, 9 * time.Hour}
	if len(want) != webhookDeliverAttempts-1 {
		t.Fatalf("schedule has %d retries, attempts %d", len(want), webhookDeliverAttempts)
	}
	var total time.Duration
	for i, d := range want {
		if got := webhookRetryDelay(i+1, 0.5); got != d {
			t.Errorf("retry after failure %d = %v, want %v", i+1, got, d)
		}
		total += d
	}
	if total < 22*time.Hour || total > 26*time.Hour {
		t.Errorf("retries span %v, want about a day", total)
	}
	if got := webhookRetryDelay(40, 0.5); got != webhookRetryCap {
		t.Errorf("a late retry = %v, want the cap", got)
	}
	if lo, hi := webhookRetryDelay(3, 0), webhookRetryDelay(3, 1); lo != 16*time.Minute-96*time.Second || hi != 16*time.Minute+96*time.Second {
		t.Errorf("jitter bounds = %v, %v", lo, hi)
	}
}

func TestDeliverWebhookNextRetryCountsErrorsNotSnoozes(t *testing.T) {
	w := deliverWorker(t, &fakeDeliveryStore{}, &fakeSender{}, nil)
	job := deliverJob()
	job.Attempt = 5
	job.Errors = []rivertype.AttemptError{{}, {}}
	got := w.NextRetry(job).Sub(webhookNow)
	if got < 16*time.Minute*9/10 || got > 16*time.Minute*11/10 {
		t.Errorf("NextRetry after two earlier errors = %v, want about 16m", got)
	}
}

func TestDeliverWebhookCancelsWhenTheSecretCannotBeOpened(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	st.delivery.SecretCiphertext = []byte{1, 2, 3}
	sender := &fakeSender{}
	err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob())
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) || len(sender.requests) != 0 {
		t.Fatalf("Work = %v, requests %d", err, len(sender.requests))
	}
	if len(st.attempts) != 1 || !strings.Contains(st.attempts[0].Error, "rotate the secret") || st.attempts[0].Succeeded {
		t.Errorf("attempts = %+v", st.attempts)
	}
	if len(st.failed) != 1 {
		t.Errorf("failed events = %v", st.failed)
	}
}

func TestDeliverWebhookCancelsAPayloadItCannotBuild(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	st.delivery.Event.Payload = json.RawMessage(`{}`)
	sender := &fakeSender{}
	err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob())
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) || len(sender.requests) != 0 || len(st.attempts) != 0 || len(st.failed) != 0 {
		t.Errorf("Work = %v, requests %d, attempts %d, failed events %v", err, len(sender.requests), len(st.attempts), st.failed)
	}
}

func TestDeliverWebhookWaitsForTheSecretKey(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	sender := &fakeSender{}
	err := deliverWorker(t, st, sender, nil).Work(t.Context(), deliverJob())
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || len(sender.requests) != 0 {
		t.Errorf("Work = %v, requests %d", err, len(sender.requests))
	}
}

func TestDeliverWebhookStoreErrors(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{err: errors.New("db down")}
	if err := deliverWorker(t, st, &fakeSender{}, sealer).Work(t.Context(), deliverJob()); err == nil || err.Error() != "db down" {
		t.Errorf("read error: %v", err)
	}
	st = &fakeDeliveryStore{delivery: deliveryFixture(t, sealer), recordErr: errors.New("db down")}
	if err := deliverWorker(t, st, &fakeSender{result: webhooks.Result{StatusCode: 200}}, sealer).Work(t.Context(), deliverJob()); err != nil {
		t.Errorf("a delivered webhook whose attempt was not recorded is not sent again: %v", err)
	}
}

type fakeWebhookSweepStore struct {
	ids   []uuid.UUID
	err   error
	calls []store.FanOutParams
}

func (s *fakeWebhookSweepStore) ListEventsAwaitingFanOut(_ context.Context, p store.FanOutParams) ([]uuid.UUID, error) {
	s.calls = append(s.calls, p)
	return s.ids, s.err
}

func TestWebhookSweepNeedsTheSecretKey(t *testing.T) {
	st := &fakeWebhookSweepStore{ids: []uuid.UUID{uuid.New()}}
	w := &EnqueueWebhookFanOutsWorker{logger: discard, store: st, webhooks: Webhooks{Sender: &fakeSender{}}, queue: "q"}
	if err := w.Work(t.Context(), &river.Job[EnqueueWebhookFanOutsArgs]{JobRow: &rivertype.JobRow{}}); err != nil || len(st.calls) != 0 {
		t.Errorf("Work = %v, calls %d", err, len(st.calls))
	}
}

func TestWebhookSweepAsksForABatch(t *testing.T) {
	st := &fakeWebhookSweepStore{}
	w := &EnqueueWebhookFanOutsWorker{logger: discard, store: st, webhooks: Webhooks{Sealer: testWebhookSealer(t), Sender: &fakeSender{}}, queue: "q"}
	job := &river.Job[EnqueueWebhookFanOutsArgs]{JobRow: &rivertype.JobRow{}}
	if err := w.Work(t.Context(), job); err != nil || len(st.calls) != 1 || st.calls[0] != (store.FanOutParams{Limit: 100, Lookback: 1000}) {
		t.Errorf("Work = %v, calls %+v", err, st.calls)
	}
	st.err = errors.New("db down")
	if err := w.Work(t.Context(), job); err == nil {
		t.Error("a store error is returned")
	}
	st.err, st.ids = nil, []uuid.UUID{uuid.New()}
	if err := w.Work(t.Context(), job); err == nil {
		t.Error("outside a River client the insert fails")
	}
}

func TestDeliverWebhookRendersACustomTemplate(t *testing.T) {
	sealer := testWebhookSealer(t)
	st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
	template := `{"title": "{{meeting.title}}", "id": "{{event.id}}", "link": "{{meeting.link}}"}`
	st.delivery.Format = store.WebhookFormatCustom
	st.delivery.Template = &template
	sender := &fakeSender{result: webhooks.Result{StatusCode: http.StatusOK}}
	w := deliverWorker(t, st, sender, sealer)
	w.webhooks.PublicWebURL = "https://civic.example"
	if err := w.Work(t.Context(), deliverJob()); err != nil || len(sender.requests) != 1 {
		t.Fatalf("Work = %v, requests %d", err, len(sender.requests))
	}
	req := sender.requests[0]
	want := `{"title":"` + st.delivery.Meeting.Title + `","id":"` + st.delivery.Event.ID.String() + `","link":"https://civic.example/meetings/` + st.delivery.Meeting.ID.String() + `"}`
	if string(req.Body) != want || req.ID != st.delivery.Event.ID.String() || req.Secret.Reveal() != "whsec_c2VjcmV0" {
		t.Errorf("request = %s, id %s", req.Body, req.ID)
	}
}

func TestDeliverWebhookCancelsABrokenTemplate(t *testing.T) {
	sealer := testWebhookSealer(t)
	for name, template := range map[string]*string{"none": nil, "invalid": new(`{"a":`)} {
		st := &fakeDeliveryStore{delivery: deliveryFixture(t, sealer)}
		st.delivery.Format = store.WebhookFormatCustom
		st.delivery.Template = template
		sender := &fakeSender{}
		err := deliverWorker(t, st, sender, sealer).Work(t.Context(), deliverJob())
		var cancel *river.JobCancelError
		if !errors.As(err, &cancel) || len(sender.requests) != 0 || len(st.attempts) != 0 {
			t.Errorf("%s: Work = %v, requests %d, attempts %d", name, err, len(sender.requests), len(st.attempts))
		}
	}
}
