package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var (
	agencyNotFound = v1.NotFoundJSONResponse{Code: "not_found", Message: "agency not found"}
	internalError  = v1.Error{Code: "internal_error", Message: "internal server error"}
)

func apiAgency(a store.Agency) v1.Agency {
	return v1.Agency{Id: a.ID, Slug: a.Slug, Name: a.Name, Website: a.Website}
}

func (h *Handler) GetAgency(ctx context.Context, req v1.GetAgencyRequestObject) (v1.GetAgencyResponseObject, error) {
	agency, err := h.store.GetLiveAgency(ctx, req.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return v1.GetAgency404JSONResponse{NotFoundJSONResponse: agencyNotFound}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get agency failed", "slug", req.Slug, "error", err)
		return v1.GetAgencydefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	return v1.GetAgency200JSONResponse(apiAgency(agency)), nil
}

func (h *Handler) ListAgencyMeetings(ctx context.Context, req v1.ListAgencyMeetingsRequestObject) (v1.ListAgencyMeetingsResponseObject, error) {
	params := store.ListMeetingsParams{}
	if req.Params.Limit != nil {
		if *req.Params.Limit < 1 || *req.Params.Limit > store.MaxMeetingsLimit {
			return v1.ListAgencyMeetings400JSONResponse{BadRequestJSONResponse: v1.BadRequestJSONResponse{
				Code:    "invalid_parameter",
				Message: fmt.Sprintf("limit must be between 1 and %d", store.MaxMeetingsLimit),
			}}, nil
		}
		params.Limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		params.Cursor = *req.Params.Cursor
	}

	agency, err := h.store.GetLiveAgency(ctx, req.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return v1.ListAgencyMeetings404JSONResponse{NotFoundJSONResponse: agencyNotFound}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get agency failed", "slug", req.Slug, "error", err)
		return v1.ListAgencyMeetingsdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}

	page, err := h.store.ListMeetings(ctx, agency.ID, params)
	if errors.Is(err, store.ErrInvalidCursor) {
		return v1.ListAgencyMeetings400JSONResponse{BadRequestJSONResponse: v1.BadRequestJSONResponse{
			Code:    "invalid_cursor",
			Message: "cursor is not valid for this list; start again without a cursor",
		}}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "list meetings failed", "slug", req.Slug, "error", err)
		return v1.ListAgencyMeetingsdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}

	meetings := make([]v1.MeetingSummary, 0, len(page.Meetings))
	for _, m := range page.Meetings {
		item := v1.MeetingSummary{Id: m.ID, Title: m.Title, StartsAt: m.StartsAt.UTC(), Status: v1.MeetingStatus(m.Status)}
		if sourceURL := m.SourceURL; sourceURL != "" {
			item.SourceUrl = &sourceURL
		}
		meetings = append(meetings, item)
	}
	list := v1.MeetingList{Meetings: meetings}
	if page.NextCursor != "" {
		list.NextCursor = &page.NextCursor
	}
	return v1.ListAgencyMeetings200JSONResponse(list), nil
}
