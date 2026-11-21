package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
)

const (
	stripeWebhookPath   = "/v1/billing/stripe-webhook"
	maxStripeEventBytes = 1 << 20
)

type StripeEventReceiver interface {
	Configured() bool
	Receive(ctx context.Context, payload []byte, signature string) (billing.Receipt, error)
}

func WithStripeEvents(r StripeEventReceiver) HandlerOption {
	return func(h *Handler) { h.stripeEvents = r }
}

var (
	webhookNotConfigured = v1.ReceiveStripeEvent503JSONResponse{StripeWebhookNotConfiguredJSONResponse: v1.StripeWebhookNotConfiguredJSONResponse{
		Code: "billing_not_configured", Message: "stripe webhooks are not configured on this server",
	}}
	stripeEventTooLarge = v1.ReceiveStripeEvent413JSONResponse{StripeEventTooLargeJSONResponse: v1.StripeEventTooLargeJSONResponse{
		Code: "body_too_large", Message: "the request body is too large",
	}}
	invalidSignature   = stripeEventRefused("invalid_signature", "the Stripe-Signature header does not verify this body")
	invalidStripeEvent = stripeEventRefused("invalid_event", "the stripe event cannot be read")
)

func stripeEventRefused(code, message string) v1.ReceiveStripeEvent400JSONResponse {
	return v1.ReceiveStripeEvent400JSONResponse{InvalidStripeEventJSONResponse: v1.InvalidStripeEventJSONResponse{Code: code, Message: message}}
}

func (h *Handler) ReceiveStripeEvent(ctx context.Context, req v1.ReceiveStripeEventRequestObject) (v1.ReceiveStripeEventResponseObject, error) {
	if !h.stripeEvents.Configured() {
		return webhookNotConfigured, nil
	}
	body, _ := ctx.Value(bodyContextKey{}).(capturedBody)
	if _, tooLarge := errors.AsType[*http.MaxBytesError](body.err); tooLarge {
		return stripeEventTooLarge, nil
	}
	signature := ""
	if req.Params.StripeSignature != nil {
		signature = *req.Params.StripeSignature
	}
	receipt, err := h.stripeEvents.Receive(ctx, body.data, signature)
	switch {
	case err == nil:
		return v1.ReceiveStripeEvent200JSONResponse{Outcome: v1.StripeEventReceiptOutcome(receipt.Outcome)}, nil
	case errors.Is(err, billing.ErrNotConfigured):
		return webhookNotConfigured, nil
	case errors.Is(err, billing.ErrInvalidSignature):
		h.logger.WarnContext(ctx, "stripe webhook refused: bad signature", "error", err, "request_id", middleware.GetReqID(ctx))
		return invalidSignature, nil
	case errors.Is(err, billing.ErrInvalidEvent):
		h.logger.WarnContext(ctx, "stripe webhook refused: unreadable event", "event_id", receipt.EventID, "error", err,
			"request_id", middleware.GetReqID(ctx))
		return invalidStripeEvent, nil
	}
	h.logger.ErrorContext(ctx, "stripe webhook failed", "event_id", receipt.EventID, "event_type", receipt.EventType, "error", err,
		"request_id", middleware.GetReqID(ctx))
	return v1.ReceiveStripeEventdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
}

func (h *Handler) stripeEventBodyError(w http.ResponseWriter, r *http.Request, err error) {
	body, _ := r.Context().Value(bodyContextKey{}).(capturedBody)
	var response v1.ReceiveStripeEventResponseObject = stripeEventRefused("invalid_body", err.Error())
	if _, tooLarge := errors.AsType[*http.MaxBytesError](body.err); tooLarge {
		response = stripeEventTooLarge
	}
	if err := response.VisitReceiveStripeEventResponse(w); err != nil {
		h.logger.WarnContext(r.Context(), "write request error failed", "error", err)
	}
}
