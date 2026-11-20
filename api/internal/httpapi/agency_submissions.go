package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/submission"
)

const (
	FlagAgencySubmissions = "agency-submissions"
	maxRequestBodyBytes   = 16 << 10
)

var (
	submissionsOff  = v1.NotFoundJSONResponse{Code: "not_found", Message: "not found"}
	websiteOnFile   = v1.AlreadyOnFileJSONResponse{Body: v1.Error{Code: "already_on_file", Message: "an agency with this website is already on file"}}
	submissionLimit = v1.SubmissionLimitJSONResponse{Body: v1.Error{Code: "submission_limit_reached", Message: "you have too many submissions awaiting review"}}
	bodyTooLarge    = v1.PayloadTooLargeJSONResponse{Body: v1.Error{Code: "body_too_large", Message: "the request body is too large"}}
	invalidBody     = v1.InvalidSubmissionJSONResponse{Body: v1.ValidationError{Code: "invalid_body", Message: "the body must be a JSON object with string name and website"}}
)

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
	w.Header().Set("Cache-Control", "no-store")
	var response v1.CreateAgencySubmissionResponseObject = v1.CreateAgencySubmission400JSONResponse{InvalidSubmissionJSONResponse: invalidBody}
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		response = v1.CreateAgencySubmission413JSONResponse{PayloadTooLargeJSONResponse: bodyTooLarge}
	}
	if gate := h.submissionGate(r.Context()); gate != nil {
		response = gate
	}
	if err := response.VisitCreateAgencySubmissionResponse(w); err != nil {
		h.logger.WarnContext(r.Context(), "write request error failed", "error", err)
	}
}

func (h *Handler) CreateAgencySubmission(ctx context.Context, req v1.CreateAgencySubmissionRequestObject) (v1.CreateAgencySubmissionResponseObject, error) {
	if gate := h.submissionGate(ctx); gate != nil {
		return gate, nil
	}
	if req.Body == nil {
		return v1.CreateAgencySubmission400JSONResponse{InvalidSubmissionJSONResponse: invalidBody}, nil
	}
	clean, problems := submission.Validate(req.Body.Name, req.Body.Website)
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
