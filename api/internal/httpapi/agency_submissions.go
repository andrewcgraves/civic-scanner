package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/submission"
)

const (
	FlagAgencySubmissions    = "agency-submissions"
	agencySubmissionsPattern = "/v1/agency-submissions"
	maxRequestBodyBytes      = 16 << 10
)

var (
	submissionsOff  = v1.NotFoundJSONResponse{Code: "not_found", Message: "not found"}
	websiteOnFile   = v1.AlreadyOnFileJSONResponse{Body: v1.Error{Code: "already_on_file", Message: "an agency with this website is already on file"}}
	submissionLimit = v1.SubmissionLimitJSONResponse{Body: v1.Error{Code: "submission_limit_reached", Message: "you have too many submissions awaiting review"}}
	bodyTooLarge    = v1.PayloadTooLargeJSONResponse{Body: v1.Error{Code: "body_too_large", Message: errBodyTooLarge.Error()}}
	notJSON         = v1.UnsupportedMediaTypeJSONResponse{Body: v1.Error{Code: "unsupported_media_type", Message: errNotJSON.Error()}}
)

func bodyResponse(err error) v1.CreateAgencySubmissionResponseObject {
	switch {
	case errors.Is(err, errBodyTooLarge):
		return v1.CreateAgencySubmission413JSONResponse{PayloadTooLargeJSONResponse: bodyTooLarge}
	case errors.Is(err, errNotJSON):
		return v1.CreateAgencySubmission415JSONResponse{UnsupportedMediaTypeJSONResponse: notJSON}
	}
	return v1.CreateAgencySubmission400JSONResponse{InvalidSubmissionJSONResponse: v1.InvalidSubmissionJSONResponse{
		Body: v1.ValidationError{Code: "invalid_body", Message: err.Error()},
	}}
}

func (h *Handler) submissionGate(ctx context.Context) v1.CreateAgencySubmissionResponseObject {
	_, err := IdentityFrom(ctx)
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, auth.ErrInvalidToken):
		return v1.CreateAgencySubmission401JSONResponse{UnauthorizedJSONResponse: unauthorized}
	case errors.Is(err, auth.ErrNotConfigured):
		return v1.CreateAgencySubmission503JSONResponse{AuthUnavailableJSONResponse: authNotConfigured}
	case err != nil:
		return v1.CreateAgencySubmission503JSONResponse{AuthUnavailableJSONResponse: authUnavailable}
	case !h.enabled(ctx, FlagAccounts) || !h.enabled(ctx, FlagAgencySubmissions):
		return v1.CreateAgencySubmission404JSONResponse{NotFoundJSONResponse: submissionsOff}
	}
	return nil
}

func (h *Handler) requestBodyError(w http.ResponseWriter, r *http.Request, err error) {
	pattern := ""
	if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
		pattern = routeContext.RoutePattern()
	}
	switch pattern {
	case stripeWebhookPath:
		h.stripeEventBodyError(w, r, err)
		return
	case webhookSubscriptionsPattern, webhookSubscriptionPattern, webhookTestPattern:
		h.webhookBodyError(w, r, err)
		return
	case agencySubmissionsPattern:
	default:
		invalidBody(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response := h.submissionGate(r.Context())
	if response == nil {
		_, err := readSubmissionBody(r.Context())
		if err == nil {
			err = errNotObject
		}
		response = bodyResponse(err)
	}
	if err := response.VisitCreateAgencySubmissionResponse(w); err != nil {
		h.logger.WarnContext(r.Context(), "write request error failed", "error", err)
	}
}

func (h *Handler) CreateAgencySubmission(ctx context.Context, _ v1.CreateAgencySubmissionRequestObject) (v1.CreateAgencySubmissionResponseObject, error) {
	if gate := h.submissionGate(ctx); gate != nil {
		return gate, nil
	}
	body, err := readSubmissionBody(ctx)
	if err != nil {
		return bodyResponse(err), nil
	}
	clean, problems := submission.Validate(body["name"], body["website"])
	if len(problems) > 0 {
		fields := make([]v1.FieldError, 0, len(problems))
		for _, p := range problems {
			fields = append(fields, v1.FieldError{Field: v1.FieldErrorField(p.Field), Code: p.Code, Message: p.Message})
		}
		return v1.CreateAgencySubmission400JSONResponse{InvalidSubmissionJSONResponse: v1.InvalidSubmissionJSONResponse{
			Body: v1.ValidationError{Code: "invalid_submission", Message: "some fields are not valid", Fields: &fields},
		}}, nil
	}
	user, err := h.requireUser(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "agency submission: ensure user failed", "error", err)
		return v1.CreateAgencySubmissiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	created, err := h.store.CreateAgencySubmission(ctx, store.AgencySubmissionInput{
		UserID: user.ID, Name: clean.Name, Website: clean.Website, WebsiteKey: clean.WebsiteKey,
		FollowUp: h.submissionFollowUp,
	})
	switch {
	case errors.Is(err, store.ErrWebsiteOnFile):
		return v1.CreateAgencySubmission409JSONResponse{AlreadyOnFileJSONResponse: websiteOnFile}, nil
	case errors.Is(err, store.ErrSubmissionLimit):
		return v1.CreateAgencySubmission429JSONResponse{SubmissionLimitJSONResponse: submissionLimit}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "agency submission failed", "error", err)
		return v1.CreateAgencySubmissiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logger.InfoContext(ctx, "agency submitted for review", "agency_id", created.AgencyID, "user_id", user.ID)
	return v1.CreateAgencySubmission201JSONResponse{Body: v1.AgencySubmission{
		Id:          created.AgencyID,
		Name:        created.Name,
		Website:     created.Website,
		Status:      v1.AgencySubmissionStatusPending,
		SubmittedAt: created.SubmittedAt.UTC(),
	}}, nil
}
