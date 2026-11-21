package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type fakeStore struct {
	pingErr     error
	agencies    []store.Agency
	listErr     error
	live        map[string]store.Agency
	getErr      error
	page        store.MeetingPage
	meetingsErr error
	listed      *listMeetingsCall
	meeting     *store.MeetingDetail
	meetingErr  error
	feeds       map[string]store.AgencyFeed
	feedErr     error
	feedCalls   *int
	users       *userCalls
	submissions *submissionCalls
	reviews     *reviewCalls
}

type submissionCalls struct {
	inputs []store.AgencySubmissionInput
	result store.AgencySubmission
	err    error
}

func (f fakeStore) CreateAgencySubmission(_ context.Context, in store.AgencySubmissionInput) (store.AgencySubmission, error) {
	if f.submissions == nil {
		return store.AgencySubmission{}, errors.New("unexpected CreateAgencySubmission")
	}
	f.submissions.inputs = append(f.submissions.inputs, in)
	return f.submissions.result, f.submissions.err
}

type reviewCall struct {
	action string
	id     uuid.UUID
	slug   string
}

type reviewCalls struct {
	calls    []reviewCall
	pending  []store.PendingAgencySubmission
	reviewed store.ReviewedAgency
	err      error
}

func (f fakeStore) ListPendingAgencySubmissions(context.Context) ([]store.PendingAgencySubmission, error) {
	if f.reviews == nil {
		return nil, errors.New("unexpected ListPendingAgencySubmissions")
	}
	f.reviews.calls = append(f.reviews.calls, reviewCall{action: "list"})
	return f.reviews.pending, f.reviews.err
}

func (f fakeStore) ApproveAgencySubmission(_ context.Context, id uuid.UUID, slug string) (store.ReviewedAgency, error) {
	if f.reviews == nil {
		return store.ReviewedAgency{}, errors.New("unexpected ApproveAgencySubmission")
	}
	f.reviews.calls = append(f.reviews.calls, reviewCall{action: "approve", id: id, slug: slug})
	return f.reviews.reviewed, f.reviews.err
}

func (f fakeStore) RejectAgencySubmission(_ context.Context, id uuid.UUID) (store.ReviewedAgency, error) {
	if f.reviews == nil {
		return store.ReviewedAgency{}, errors.New("unexpected RejectAgencySubmission")
	}
	f.reviews.calls = append(f.reviews.calls, reviewCall{action: "reject", id: id})
	return f.reviews.reviewed, f.reviews.err
}

type userCalls struct {
	inputs []store.UserInput
	user   store.User
	err    error
}

func (f fakeStore) EnsureUser(_ context.Context, in store.UserInput) (store.User, bool, error) {
	if f.users == nil {
		return store.User{}, false, errors.New("unexpected EnsureUser")
	}
	f.users.inputs = append(f.users.inputs, in)
	return f.users.user, false, f.users.err
}

func (f fakeStore) GetLiveAgencyFeed(_ context.Context, slug string) (store.AgencyFeed, error) {
	if f.feedCalls != nil {
		*f.feedCalls++
	}
	if f.feedErr != nil {
		return store.AgencyFeed{}, f.feedErr
	}
	feed, ok := f.feeds[slug]
	if !ok {
		return store.AgencyFeed{}, store.ErrNotFound
	}
	return feed, nil
}

func (f fakeStore) GetLiveMeetingDetail(_ context.Context, id uuid.UUID) (store.MeetingDetail, error) {
	if f.meetingErr != nil {
		return store.MeetingDetail{}, f.meetingErr
	}
	if f.meeting == nil || f.meeting.Meeting.ID != id {
		return store.MeetingDetail{}, store.ErrNotFound
	}
	return *f.meeting, nil
}

type listMeetingsCall struct {
	agencyID uuid.UUID
	params   store.ListMeetingsParams
}

func (f fakeStore) Ping(context.Context) error { return f.pingErr }

func (f fakeStore) ListAgencies(context.Context) ([]store.Agency, error) {
	return f.agencies, f.listErr
}

func (f fakeStore) GetLiveAgency(_ context.Context, slug string) (store.Agency, error) {
	if f.getErr != nil {
		return store.Agency{}, f.getErr
	}
	a, ok := f.live[slug]
	if !ok {
		return store.Agency{}, store.ErrNotFound
	}
	return a, nil
}

func (f fakeStore) ListMeetings(_ context.Context, agencyID uuid.UUID, params store.ListMeetingsParams) (store.MeetingPage, error) {
	if f.listed != nil {
		*f.listed = listMeetingsCall{agencyID: agencyID, params: params}
	}
	return f.page, f.meetingsErr
}

func get(t *testing.T, s Store, path string, out any) int {
	t.Helper()
	router := NewRouter(NewHandler(s, slog.New(slog.DiscardHandler), "test"), slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantCode   int
		wantStatus v1.HealthStatus
	}{
		{"ok", nil, http.StatusOK, v1.Ok},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable, v1.Degraded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body v1.Health
			code := get(t, fakeStore{pingErr: tt.pingErr}, "/v1/healthz", &body)
			if code != tt.wantCode || body.Status != tt.wantStatus {
				t.Errorf("got %d %q, want %d %q", code, body.Status, tt.wantCode, tt.wantStatus)
			}
			if body.Version == nil || *body.Version != "test" {
				t.Errorf("version = %v, want test", body.Version)
			}
		})
	}
}

func TestListAgencies(t *testing.T) {
	id := uuid.New()
	var body v1.AgencyList
	code := get(t, fakeStore{agencies: []store.Agency{{ID: id, Slug: "la-metro", Name: "LA Metro", Website: "https://www.metro.net"}}}, "/v1/agencies", &body)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	want := v1.Agency{Id: id, Slug: "la-metro", Name: "LA Metro", Website: "https://www.metro.net"}
	if len(body.Agencies) != 1 || body.Agencies[0] != want {
		t.Errorf("agencies = %+v, want [%+v]", body.Agencies, want)
	}
}

func TestListAgenciesEmptyIsArray(t *testing.T) {
	var body map[string]json.RawMessage
	get(t, fakeStore{}, "/v1/agencies", &body)
	if string(body["agencies"]) != "[]" {
		t.Errorf("agencies = %s, want []", body["agencies"])
	}
}

func TestListAgenciesError(t *testing.T) {
	var body v1.Error
	code := get(t, fakeStore{listErr: errors.New("boom")}, "/v1/agencies", &body)
	if code != http.StatusInternalServerError || body.Code != "internal_error" {
		t.Errorf("got %d %+v", code, body)
	}
}

func TestUnversionedPathNotFound(t *testing.T) {
	if code := get(t, fakeStore{}, "/healthz", nil); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}
