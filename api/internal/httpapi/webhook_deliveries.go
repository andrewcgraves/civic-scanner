package httpapi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const (
	webhookTestPattern      = "/v1/webhook-subscriptions/{id}/test"
	maxWebhookTestsPerUser  = 10
	webhookTestWindow       = time.Minute
	webhookTestLimiterSweep = 1024
)

var errUnreadableWebhookSecret = errors.New("the signing secret cannot be read; rotate the secret")

type WebhookSender interface {
	Send(ctx context.Context, req webhooks.Request) webhooks.Result
}

func WithWebhookSender(s WebhookSender) HandlerOption {
	return func(h *Handler) { h.webhookSender = s }
}

func WithClock(now func() time.Time) HandlerOption {
	return func(h *Handler) { h.now = now }
}

type webhookTestLimiter struct {
	mu   sync.Mutex
	sent map[uuid.UUID][]time.Time
}

func newWebhookTestLimiter() *webhookTestLimiter {
	return &webhookTestLimiter{sent: make(map[uuid.UUID][]time.Time)}
}

func (l *webhookTestLimiter) allow(userID uuid.UUID, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-webhookTestWindow)
	if len(l.sent) >= webhookTestLimiterSweep {
		for id, times := range l.sent {
			if len(times) == 0 || !times[len(times)-1].After(cutoff) {
				delete(l.sent, id)
			}
		}
	}
	recent := l.sent[userID][:0]
	for _, t := range l.sent[userID] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= maxWebhookTestsPerUser {
		l.sent[userID] = recent
		return recent[0].Sub(cutoff), false
	}
	l.sent[userID] = append(recent, now)
	return 0, true
}

func retryAfterSeconds(wait time.Duration) int {
	return max(int(math.Ceil(wait.Seconds())), 1)
}

func apiWebhookAttempt(a store.WebhookAttempt) v1.WebhookDeliveryAttempt {
	return v1.WebhookDeliveryAttempt{
		Attempt:    a.Attempt,
		StartedAt:  a.StartedAt.UTC(),
		DurationMs: int(a.Duration.Milliseconds()),
		StatusCode: a.StatusCode,
		Error:      a.Error,
		Succeeded:  a.Succeeded,
	}
}

func apiWebhookDelivery(d store.WebhookDeliveryLogEntry) v1.WebhookDelivery {
	attempts := make([]v1.WebhookDeliveryAttempt, 0, len(d.Attempts))
	for _, a := range d.Attempts {
		attempts = append(attempts, apiWebhookAttempt(a))
	}
	meeting := v1.WebhookMeeting{Id: d.Meeting.ID, Title: d.Meeting.Title, StartsAt: d.Meeting.StartsAt.UTC()}
	if d.Meeting.SourceURL != "" {
		meeting.SourceUrl = &d.Meeting.SourceURL
	}
	return v1.WebhookDelivery{
		EventId:        d.EventID,
		Type:           v1.WebhookEventType(d.EventType),
		Meeting:        meeting,
		Succeeded:      d.Succeeded,
		FirstAttemptAt: d.FirstAttemptAt.UTC(),
		LastAttemptAt:  d.LastAttemptAt.UTC(),
		Attempts:       attempts,
	}
}

func (h *Handler) ListWebhookDeliveries(ctx context.Context, req v1.ListWebhookDeliveriesRequestObject) (v1.ListWebhookDeliveriesResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.ListWebhookDeliveries402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	params := store.ListWebhookDeliveriesParams{}
	if req.Params.Limit != nil {
		if *req.Params.Limit < 1 || *req.Params.Limit > store.MaxWebhookDeliveriesLimit {
			return v1.ListWebhookDeliveries400JSONResponse{BadRequestJSONResponse: v1.BadRequestJSONResponse{
				Code:    "invalid_parameter",
				Message: fmt.Sprintf("limit must be between 1 and %d", store.MaxWebhookDeliveriesLimit),
			}}, nil
		}
		params.Limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		params.Cursor = *req.Params.Cursor
	}
	page, err := h.store.ListWebhookDeliveries(ctx, user.ID, req.Id, params)
	switch {
	case errors.Is(err, store.ErrInvalidCursor):
		return v1.ListWebhookDeliveries400JSONResponse{BadRequestJSONResponse: v1.BadRequestJSONResponse{
			Code:    "invalid_cursor",
			Message: "cursor is not valid for this list; start again without a cursor",
		}}, nil
	case errors.Is(err, store.ErrNotFound):
		return v1.ListWebhookDeliveries404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "list webhook deliveries failed", "error", err, "user_id", user.ID, "subscription_id", req.Id)
		return v1.ListWebhookDeliveriesdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	list := v1.WebhookDeliveryList{Deliveries: make([]v1.WebhookDelivery, 0, len(page.Deliveries))}
	for _, d := range page.Deliveries {
		list.Deliveries = append(list.Deliveries, apiWebhookDelivery(d))
	}
	if page.NextCursor != "" {
		list.NextCursor = &page.NextCursor
	}
	return v1.ListWebhookDeliveries200JSONResponse{Body: list}, nil
}

func (h *Handler) SendWebhookTest(ctx context.Context, req v1.SendWebhookTestRequestObject) (v1.SendWebhookTestResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.SendWebhookTest402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	eventType := store.EventAgendaPublished
	if req.Body != nil && req.Body.Type != nil {
		eventType = store.EventType(*req.Body.Type)
		if !eventType.Valid() {
			return v1.SendWebhookTest400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_event_type",
				"type must be agenda_published, agenda_changed or transcript_ready")}, nil
		}
	}
	if !h.webhookSealer.Configured() || h.webhookSender == nil {
		return v1.SendWebhookTest503JSONResponse{WebhooksUnavailableJSONResponse: webhooksNotConfigured}, nil
	}
	sub, err := h.store.GetWebhookSubscription(ctx, user.ID, req.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.SendWebhookTest404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "get webhook subscription failed", "error", err, "user_id", user.ID)
		return v1.SendWebhookTestdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	now := h.now()
	if wait, allowed := h.webhookTests.allow(user.ID, now); !allowed {
		retryAfter := retryAfterSeconds(wait)
		h.logger.InfoContext(ctx, "webhook test refused", "reason", "rate_limited", "user_id", user.ID, "subscription_id", sub.ID)
		return v1.SendWebhookTest429JSONResponse{WebhookTestLimitJSONResponse: v1.WebhookTestLimitJSONResponse{
			Body:    v1.Error{Code: "test_limit_reached", Message: fmt.Sprintf("you can send at most %d test webhooks a minute", maxWebhookTestsPerUser)},
			Headers: v1.WebhookTestLimitResponseHeaders{RetryAfter: &retryAfter},
		}}, nil
	}
	sealed, err := h.store.WebhookSubscriptionSecret(ctx, sub.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.SendWebhookTest404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "read webhook secret failed", "error", err, "user_id", user.ID, "subscription_id", sub.ID)
		return v1.SendWebhookTestdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	event, body, err := webhookTestBody(eventType, sub, now)
	if err != nil {
		h.logger.ErrorContext(ctx, "build webhook test failed", "error", err, "user_id", user.ID, "subscription_id", sub.ID)
		return v1.SendWebhookTestdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	result := h.sendWebhookTest(ctx, sub, event.ID.String(), body, sealed, now)
	h.logger.InfoContext(ctx, "webhook test sent", "subscription_id", sub.ID, "user_id", user.ID, "event_type", string(eventType),
		"succeeded", result.Succeeded(), "status_code", result.StatusCode, "duration_ms", result.Duration.Milliseconds())
	return v1.SendWebhookTest200JSONResponse{Body: apiWebhookTestResult(event, now, result)}, nil
}

func webhookTestBody(eventType store.EventType, sub store.WebhookSubscription, now time.Time) (webhooks.Event, []byte, error) {
	event, err := webhooks.SampleEvent(eventType, sub.AgencySlug, sub.AgencyName, now)
	if err != nil {
		return webhooks.Event{}, nil, err
	}
	body, err := webhooks.Payload(sub.PayloadVersion, event)
	return event, body, err
}

func (h *Handler) sendWebhookTest(ctx context.Context, sub store.WebhookSubscription, id string, body, sealed []byte, at time.Time) webhooks.Result {
	if problem := h.webhookAddresses.Check(ctx, sub.URL); problem != nil {
		h.logger.InfoContext(ctx, "webhook endpoint refused", "code", problem.Code, "user_id", sub.UserID)
		return webhooks.Result{Err: errors.New(problem.Message)}
	}
	secret, err := h.webhookSealer.Open(sub.ID, sealed)
	if err != nil {
		return webhooks.Result{Err: errUnreadableWebhookSecret}
	}
	return h.webhookSender.Send(ctx, webhooks.Request{URL: sub.URL, ID: id, Body: body, Secret: secret, At: at})
}

func apiWebhookTestResult(event webhooks.Event, sentAt time.Time, result webhooks.Result) v1.WebhookTestResult {
	out := v1.WebhookTestResult{
		Id:         event.ID,
		Type:       v1.WebhookEventType(event.Type),
		SentAt:     sentAt.UTC().Truncate(time.Second),
		Succeeded:  result.Succeeded(),
		DurationMs: int(max(result.Duration.Milliseconds(), 0)),
	}
	if result.StatusCode != 0 {
		status := result.StatusCode
		out.StatusCode = &status
	}
	if result.Err != nil {
		problem := result.Err.Error()
		out.Error = &problem
	}
	return out
}
