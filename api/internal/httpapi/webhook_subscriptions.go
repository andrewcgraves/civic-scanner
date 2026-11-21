package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const (
	FlagWebhookSubscriptions    = "webhook-subscriptions"
	webhookSubscriptionsPattern = "/v1/webhook-subscriptions"
	webhookSubscriptionPattern  = "/v1/webhook-subscriptions/{id}"
)

var (
	webhooksNotConfigured = v1.WebhooksUnavailableJSONResponse{Body: v1.Error{Code: "webhooks_not_configured", Message: "webhook subscriptions cannot be created on this server right now"}}
	webhookNotFound       = v1.WebhookSubscriptionNotFoundJSONResponse{Body: v1.Error{Code: "not_found", Message: "not found"}}
	webhookExists         = v1.WebhookSubscriptionExistsJSONResponse{Body: v1.Error{Code: "already_subscribed", Message: "you already have a subscription to this agency at this URL"}}
	webhookLimit          = v1.WebhookSubscriptionLimitJSONResponse{Body: v1.Error{Code: "subscription_limit_reached", Message: fmt.Sprintf("you can have at most %d webhook subscriptions", store.MaxWebhookSubscriptionsPerUser)}}
	webhookPaymentNeeded  = v1.PaymentRequiredJSONResponse{Body: paymentRequired}
)

func WithWebhookSealer(s *webhooks.Sealer) HandlerOption {
	return func(h *Handler) { h.webhookSealer = s }
}

func invalidWebhook(code, message string) v1.InvalidWebhookSubscriptionJSONResponse {
	return v1.InvalidWebhookSubscriptionJSONResponse{Body: v1.Error{Code: code, Message: message}}
}

func webhookEndpoint(raw string) (string, *v1.InvalidWebhookSubscriptionJSONResponse) {
	url, problem := webhooks.NormalizeEndpoint(raw)
	if problem != nil {
		bad := invalidWebhook(problem.Code, problem.Message)
		return "", &bad
	}
	return url, nil
}

func webhookPayloadVersion(version *v1.WebhookPayloadVersion) (string, *v1.InvalidWebhookSubscriptionJSONResponse) {
	if version == nil || *version == v1.WebhookPayloadV1 {
		return store.WebhookPayloadV1, nil
	}
	bad := invalidWebhook("invalid_payload_version", "payloadVersion must be v1")
	return "", &bad
}

func apiWebhookSubscription(s store.WebhookSubscription) v1.WebhookSubscription {
	return v1.WebhookSubscription{
		Id:              s.ID,
		Agency:          s.AgencySlug,
		Url:             s.URL,
		PayloadVersion:  v1.WebhookPayloadVersion(s.PayloadVersion),
		Status:          v1.WebhookSubscriptionStatus(s.Status),
		CreatedAt:       s.CreatedAt.UTC(),
		UpdatedAt:       s.UpdatedAt.UTC(),
		SecretRotatedAt: s.SecretRotatedAt.UTC(),
	}
}

func apiWebhookWithSecret(s store.WebhookSubscription, secret string) v1.WebhookSubscriptionWithSecret {
	sub := apiWebhookSubscription(s)
	return v1.WebhookSubscriptionWithSecret{
		Id:              sub.Id,
		Agency:          sub.Agency,
		Url:             sub.Url,
		PayloadVersion:  sub.PayloadVersion,
		Status:          sub.Status,
		CreatedAt:       sub.CreatedAt,
		UpdatedAt:       sub.UpdatedAt,
		SecretRotatedAt: sub.SecretRotatedAt,
		Secret:          secret,
	}
}

func (h *Handler) webhookBodyError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	if user, gateErr := h.requirePaid(ctx, FlagWebhookSubscriptions); gateErr != nil {
		h.refusePaid(ctx, w, "WebhookSubscriptionBody", user, gateErr)
		return
	}
	invalidBody(w, r, err)
}

func (h *Handler) ListWebhookSubscriptions(ctx context.Context, _ v1.ListWebhookSubscriptionsRequestObject) (v1.ListWebhookSubscriptionsResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.ListWebhookSubscriptions402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	subs, err := h.store.ListWebhookSubscriptions(ctx, user.ID)
	if err != nil {
		h.logger.ErrorContext(ctx, "list webhook subscriptions failed", "error", err, "user_id", user.ID)
		return v1.ListWebhookSubscriptionsdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	out := make([]v1.WebhookSubscription, 0, len(subs))
	for _, s := range subs {
		out = append(out, apiWebhookSubscription(s))
	}
	return v1.ListWebhookSubscriptions200JSONResponse{Body: v1.WebhookSubscriptionList{Subscriptions: out}}, nil
}

func (h *Handler) CreateWebhookSubscription(ctx context.Context, req v1.CreateWebhookSubscriptionRequestObject) (v1.CreateWebhookSubscriptionResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.CreateWebhookSubscription402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	if req.Body == nil {
		return v1.CreateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_body", "the body must be a JSON object")}, nil
	}
	agency := strings.TrimSpace(req.Body.Agency)
	if agency == "" {
		return v1.CreateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("unknown_agency", "agency must be the slug of a live agency")}, nil
	}
	url, bad := webhookEndpoint(req.Body.Url)
	if bad != nil {
		return v1.CreateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: *bad}, nil
	}
	version, bad := webhookPayloadVersion(req.Body.PayloadVersion)
	if bad != nil {
		return v1.CreateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: *bad}, nil
	}
	if !h.webhookSealer.Configured() {
		return v1.CreateWebhookSubscription503JSONResponse{WebhooksUnavailableJSONResponse: webhooksNotConfigured}, nil
	}
	id := uuid.New()
	secret := webhooks.NewSecret()
	sealed, err := h.webhookSealer.Seal(id, secret)
	if err != nil {
		h.logger.ErrorContext(ctx, "seal webhook secret failed", "error", err)
		return v1.CreateWebhookSubscriptiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	created, err := h.store.CreateWebhookSubscription(ctx, store.WebhookSubscriptionInput{
		ID: id, UserID: user.ID, AgencySlug: agency, URL: url, PayloadVersion: version, SecretCiphertext: sealed,
	})
	switch {
	case errors.Is(err, store.ErrUnknownAgency):
		return v1.CreateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("unknown_agency", "no live agency has this slug")}, nil
	case errors.Is(err, store.ErrWebhookSubscriptionExists):
		return v1.CreateWebhookSubscription409JSONResponse{WebhookSubscriptionExistsJSONResponse: webhookExists}, nil
	case errors.Is(err, store.ErrWebhookSubscriptionLimit):
		return v1.CreateWebhookSubscription429JSONResponse{WebhookSubscriptionLimitJSONResponse: webhookLimit}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "create webhook subscription failed", "error", err, "user_id", user.ID)
		return v1.CreateWebhookSubscriptiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logger.InfoContext(ctx, "webhook subscription created", "subscription_id", created.ID, "user_id", user.ID, "agency_id", created.AgencyID)
	return v1.CreateWebhookSubscription201JSONResponse{Body: apiWebhookWithSecret(created, secret.Reveal())}, nil
}

func (h *Handler) GetWebhookSubscription(ctx context.Context, req v1.GetWebhookSubscriptionRequestObject) (v1.GetWebhookSubscriptionResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.GetWebhookSubscription402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	sub, err := h.store.GetWebhookSubscription(ctx, user.ID, req.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.GetWebhookSubscription404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "get webhook subscription failed", "error", err, "user_id", user.ID)
		return v1.GetWebhookSubscriptiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	return v1.GetWebhookSubscription200JSONResponse{Body: apiWebhookSubscription(sub)}, nil
}

func (h *Handler) UpdateWebhookSubscription(ctx context.Context, req v1.UpdateWebhookSubscriptionRequestObject) (v1.UpdateWebhookSubscriptionResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.UpdateWebhookSubscription402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	if req.Body == nil {
		return v1.UpdateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_body", "the body must be a JSON object")}, nil
	}
	var update store.WebhookSubscriptionUpdate
	if req.Body.Url != nil {
		url, bad := webhookEndpoint(*req.Body.Url)
		if bad != nil {
			return v1.UpdateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: *bad}, nil
		}
		update.URL = &url
	}
	if req.Body.PayloadVersion != nil {
		version, bad := webhookPayloadVersion(req.Body.PayloadVersion)
		if bad != nil {
			return v1.UpdateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: *bad}, nil
		}
		update.PayloadVersion = &version
	}
	if req.Body.Status != nil {
		status := store.WebhookStatus(*req.Body.Status)
		if !status.Valid() {
			return v1.UpdateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_status", "status must be active or paused")}, nil
		}
		update.Status = &status
	}
	if update.Empty() {
		return v1.UpdateWebhookSubscription400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("nothing_to_update", "give at least one of url, payloadVersion and status")}, nil
	}
	sub, err := h.store.UpdateWebhookSubscription(ctx, user.ID, req.Id, update)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.UpdateWebhookSubscription404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case errors.Is(err, store.ErrWebhookSubscriptionExists):
		return v1.UpdateWebhookSubscription409JSONResponse{WebhookSubscriptionExistsJSONResponse: webhookExists}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "update webhook subscription failed", "error", err, "user_id", user.ID)
		return v1.UpdateWebhookSubscriptiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logger.InfoContext(ctx, "webhook subscription updated", "subscription_id", sub.ID, "user_id", user.ID, "status", string(sub.Status))
	return v1.UpdateWebhookSubscription200JSONResponse{Body: apiWebhookSubscription(sub)}, nil
}

func (h *Handler) DeleteWebhookSubscription(ctx context.Context, req v1.DeleteWebhookSubscriptionRequestObject) (v1.DeleteWebhookSubscriptionResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.DeleteWebhookSubscription402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	err := h.store.DeleteWebhookSubscription(ctx, user.ID, req.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.DeleteWebhookSubscription404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "delete webhook subscription failed", "error", err, "user_id", user.ID)
		return v1.DeleteWebhookSubscriptiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logger.InfoContext(ctx, "webhook subscription deleted", "subscription_id", req.Id, "user_id", user.ID)
	return v1.DeleteWebhookSubscription204Response{}, nil
}

func (h *Handler) RotateWebhookSubscriptionSecret(ctx context.Context, req v1.RotateWebhookSubscriptionSecretRequestObject) (v1.RotateWebhookSubscriptionSecretResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.RotateWebhookSubscriptionSecret402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	if !h.webhookSealer.Configured() {
		return v1.RotateWebhookSubscriptionSecret503JSONResponse{WebhooksUnavailableJSONResponse: webhooksNotConfigured}, nil
	}
	secret := webhooks.NewSecret()
	sealed, err := h.webhookSealer.Seal(req.Id, secret)
	if err != nil {
		h.logger.ErrorContext(ctx, "seal webhook secret failed", "error", err)
		return v1.RotateWebhookSubscriptionSecretdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	sub, err := h.store.RotateWebhookSubscriptionSecret(ctx, user.ID, req.Id, sealed)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.RotateWebhookSubscriptionSecret404JSONResponse{WebhookSubscriptionNotFoundJSONResponse: webhookNotFound}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "rotate webhook secret failed", "error", err, "user_id", user.ID)
		return v1.RotateWebhookSubscriptionSecretdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logger.InfoContext(ctx, "webhook secret rotated", "subscription_id", sub.ID, "user_id", user.ID)
	return v1.RotateWebhookSubscriptionSecret200JSONResponse{Body: apiWebhookWithSecret(sub, secret.Reveal())}, nil
}
