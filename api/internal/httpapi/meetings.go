package httpapi

import (
	"context"
	"errors"
	"net/http"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var meetingNotFound = v1.NotFoundJSONResponse{Code: "not_found", Message: "meeting not found"}

func (h *Handler) GetMeeting(ctx context.Context, req v1.GetMeetingRequestObject) (v1.GetMeetingResponseObject, error) {
	detail, err := h.store.GetLiveMeetingDetail(ctx, req.Id)
	if errors.Is(err, store.ErrNotFound) {
		return v1.GetMeeting404JSONResponse{NotFoundJSONResponse: meetingNotFound}, nil
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get meeting failed", "meeting_id", req.Id, "error", err)
		return v1.GetMeetingdefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	return v1.GetMeeting200JSONResponse(apiMeetingDetail(detail)), nil
}

func apiMeetingDetail(d store.MeetingDetail) v1.MeetingDetail {
	m := d.Meeting
	out := v1.MeetingDetail{
		Id:         m.ID,
		Agency:     apiAgency(d.Agency),
		Title:      m.Title,
		StartsAt:   m.StartsAt.UTC(),
		Status:     v1.MeetingStatus(m.Status),
		Recordings: make([]v1.Recording, 0, len(d.Recordings)),
	}
	if sourceURL := m.SourceURL; sourceURL != "" {
		out.SourceUrl = &sourceURL
	}
	if a := d.Agenda; a != nil {
		out.Agenda = &v1.Agenda{
			Version:     int64(a.Version),
			DocumentUrl: a.SourceURL,
			Summary:     a.Summary,
			ChangeNote:  a.ChangeNote,
			PublishedAt: a.CreatedAt.UTC(),
		}
	}
	for _, r := range d.Recordings {
		out.Recordings = append(out.Recordings, v1.Recording{SourceUrl: r.Recording.SourceURL, Transcript: apiTranscript(r.Transcript)})
	}
	return out
}

func apiTranscript(t *store.Transcript) *v1.Transcript {
	if t == nil {
		return nil
	}
	segments := make([]v1.TranscriptSegment, 0, len(t.Segments))
	for _, s := range t.Segments {
		segment := v1.TranscriptSegment{StartMs: s.Start.Milliseconds(), EndMs: s.End.Milliseconds(), Text: s.Text}
		if speaker := s.Speaker; speaker != "" {
			segment.Speaker = &speaker
		}
		segments = append(segments, segment)
	}
	return &v1.Transcript{Summary: t.Summary, Segments: segments, UpdatedAt: t.UpdatedAt.UTC()}
}
