package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var (
	submissionNotFound = v1.NotFoundJSONResponse{Code: "not_found", Message: "no submitted agency has this id"}
	notPending         = v1.ReviewConflictJSONResponse{Body: v1.Error{Code: "not_pending", Message: "this agency has already been reviewed"}}
	slugTaken          = v1.ReviewConflictJSONResponse{Body: v1.Error{Code: "slug_taken", Message: "another agency already has this slug"}}
	invalidSlug        = v1.InvalidApprovalJSONResponse{Body: v1.Error{
		Code: "invalid_slug",
		Message: fmt.Sprintf("the slug must be %d to %d lower-case letters, digits and single hyphens, not starting with %q",
			store.MinSlugLength, store.MaxSlugLength, store.SubmittedSlugPrefix),
	}}
)

func (h *Handler) ListPendingAgencySubmissions(ctx context.Context, _ v1.ListPendingAgencySubmissionsRequestObject) (v1.ListPendingAgencySubmissionsResponseObject, error) {
	if _, ok := adminFrom(ctx); !ok {
		return v1.ListPendingAgencySubmissions403JSONResponse{ForbiddenJSONResponse: v1.ForbiddenJSONResponse{Body: forbidden}}, nil
	}
	pending, err := h.store.ListPendingAgencySubmissions(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "list pending agency submissions failed", "error", err)
		return v1.ListPendingAgencySubmissionsdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	submissions := make([]v1.PendingAgencySubmission, 0, len(pending))
	for _, p := range pending {
		submissions = append(submissions, v1.PendingAgencySubmission{
			Id:          p.AgencyID,
			Name:        p.Name,
			Website:     p.Website,
			SubmittedAt: p.SubmittedAt.UTC(),
			SubmittedBy: v1.Submitter{Id: p.SubmittedBy, Email: p.SubmitterEmail},
		})
	}
	return v1.ListPendingAgencySubmissions200JSONResponse{Body: v1.PendingAgencySubmissionList{Submissions: submissions}}, nil
}

func (h *Handler) ApproveAgencySubmission(ctx context.Context, request v1.ApproveAgencySubmissionRequestObject) (v1.ApproveAgencySubmissionResponseObject, error) {
	admin, ok := adminFrom(ctx)
	if !ok {
		return v1.ApproveAgencySubmission403JSONResponse{ForbiddenJSONResponse: v1.ForbiddenJSONResponse{Body: forbidden}}, nil
	}
	if request.Body == nil || !store.ValidLiveSlug(request.Body.Slug) {
		return v1.ApproveAgencySubmission400JSONResponse{InvalidApprovalJSONResponse: invalidSlug}, nil
	}
	reviewed, err := h.store.ApproveAgencySubmission(ctx, request.Id, request.Body.Slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.ApproveAgencySubmission404JSONResponse{NotFoundJSONResponse: submissionNotFound}, nil
	case errors.Is(err, store.ErrNotPending):
		return v1.ApproveAgencySubmission409JSONResponse{ReviewConflictJSONResponse: notPending}, nil
	case errors.Is(err, store.ErrSlugTaken):
		return v1.ApproveAgencySubmission409JSONResponse{ReviewConflictJSONResponse: slugTaken}, nil
	case errors.Is(err, store.ErrInvalidSlug):
		return v1.ApproveAgencySubmission400JSONResponse{InvalidApprovalJSONResponse: invalidSlug}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "approve agency submission failed", "agency_id", request.Id, "error", err)
		return v1.ApproveAgencySubmissiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logReview(ctx, "agency submission approved", reviewed, admin.ID)
	return v1.ApproveAgencySubmission200JSONResponse{Body: reviewedBody(reviewed)}, nil
}

func (h *Handler) RejectAgencySubmission(ctx context.Context, request v1.RejectAgencySubmissionRequestObject) (v1.RejectAgencySubmissionResponseObject, error) {
	admin, ok := adminFrom(ctx)
	if !ok {
		return v1.RejectAgencySubmission403JSONResponse{ForbiddenJSONResponse: v1.ForbiddenJSONResponse{Body: forbidden}}, nil
	}
	reviewed, err := h.store.RejectAgencySubmission(ctx, request.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return v1.RejectAgencySubmission404JSONResponse{NotFoundJSONResponse: submissionNotFound}, nil
	case errors.Is(err, store.ErrNotPending):
		return v1.RejectAgencySubmission409JSONResponse{ReviewConflictJSONResponse: notPending}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "reject agency submission failed", "agency_id", request.Id, "error", err)
		return v1.RejectAgencySubmissiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	h.logReview(ctx, "agency submission rejected", reviewed, admin.ID)
	return v1.RejectAgencySubmission200JSONResponse{Body: reviewedBody(reviewed)}, nil
}

func (h *Handler) logReview(ctx context.Context, message string, reviewed store.ReviewedAgency, adminID uuid.UUID) {
	h.logger.InfoContext(ctx, message, "agency_id", reviewed.ID, "slug", reviewed.Slug, "status", string(reviewed.Status), "admin_id", adminID)
}

func reviewedBody(r store.ReviewedAgency) v1.ReviewedAgencySubmission {
	status := v1.ReviewedAgencySubmissionStatusRejected
	if r.Status == store.AgencyStatusLive {
		status = v1.ReviewedAgencySubmissionStatusLive
	}
	return v1.ReviewedAgencySubmission{Id: r.ID, Slug: r.Slug, Name: r.Name, Website: r.Website, Status: status}
}
