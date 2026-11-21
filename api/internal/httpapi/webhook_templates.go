package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const (
	webhookTemplatesPath           = "/v1/webhook-templates"
	webhookTemplatesPreviewPattern = webhookTemplatesPath + "/preview"
	sampleAgencySlug               = "sample-agency"
	sampleAgencyName               = "Sample agency"
)

func (h *Handler) PreviewWebhookTemplate(ctx context.Context, req v1.PreviewWebhookTemplateRequestObject) (v1.PreviewWebhookTemplateResponseObject, error) {
	user, ok := paidFrom(ctx)
	if !ok {
		return v1.PreviewWebhookTemplate402JSONResponse{PaymentRequiredJSONResponse: webhookPaymentNeeded}, nil
	}
	if req.Body == nil {
		return v1.PreviewWebhookTemplate400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_body", "the body must be a JSON object")}, nil
	}
	eventType := store.EventAgendaPublished
	if req.Body.Type != nil {
		eventType = store.EventType(*req.Body.Type)
		if !eventType.Valid() {
			return v1.PreviewWebhookTemplate400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("invalid_event_type",
				"type must be agenda_published, agenda_changed or transcript_ready")}, nil
		}
	}
	template, err := webhooks.ParseTemplate(req.Body.Template)
	if problem, ok := errors.AsType[*webhooks.TemplateError](err); ok {
		return v1.PreviewWebhookTemplate400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook(problem.Code, problem.Message)}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "parse webhook template failed", "error", err, "user_id", user.ID)
		return v1.PreviewWebhookTemplatedefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	slug, name := sampleAgencySlug, sampleAgencyName
	if req.Body.Agency != nil {
		agency, err := h.store.GetLiveAgency(ctx, strings.TrimSpace(*req.Body.Agency))
		switch {
		case errors.Is(err, store.ErrNotFound):
			return v1.PreviewWebhookTemplate400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook("unknown_agency", "no live agency has this slug")}, nil
		case err != nil:
			h.logger.ErrorContext(ctx, "get agency for webhook preview failed", "error", err, "user_id", user.ID)
			return v1.PreviewWebhookTemplatedefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
		}
		slug, name = agency.Slug, agency.Name
	}
	event, err := webhooks.SampleEvent(eventType, slug, name, h.now())
	if err != nil {
		h.logger.ErrorContext(ctx, "build webhook preview event failed", "error", err, "user_id", user.ID)
		return v1.PreviewWebhookTemplatedefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	body, err := template.Render(event)
	if errors.Is(err, webhooks.ErrRenderedTemplateTooLarge) {
		return v1.PreviewWebhookTemplate400JSONResponse{InvalidWebhookSubscriptionJSONResponse: invalidWebhook(webhooks.TemplateTooLarge, err.Error())}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "render webhook preview failed", "error", err, "user_id", user.ID)
		return v1.PreviewWebhookTemplatedefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	return v1.PreviewWebhookTemplate200JSONResponse{Body: v1.WebhookTemplatePreview{Type: v1.WebhookEventType(eventType), Body: string(body)}}, nil
}
