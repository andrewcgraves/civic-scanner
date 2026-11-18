package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var laMetro = store.Agency{ID: uuid.MustParse("6f1c2a52-3c1e-4d8e-9b55-0a7c1d2e3f40"), Slug: "la-metro", Name: "LA Metro", Website: "https://www.metro.net"}

func liveStore() fakeStore {
	return fakeStore{live: map[string]store.Agency{laMetro.Slug: laMetro}}
}

func getRaw(t *testing.T, s Store, path string) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	rec := httptest.NewRecorder()
	NewRouter(NewHandler(s, logger, "test"), logger).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Errorf("status = %d, want %d (body %s)", rec.Code, wantStatus, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body v1.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != wantCode || body.Message == "" {
		t.Errorf("body = %q (%v), want %s Error", rec.Body.String(), err, wantCode)
	}
}

func TestGetAgencyReturnsTheListItemShape(t *testing.T) {
	var body map[string]any
	code := get(t, liveStore(), "/v1/agencies/la-metro", &body)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	want := map[string]any{"id": laMetro.ID.String(), "slug": "la-metro", "name": "LA Metro", "website": "https://www.metro.net"}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Errorf("body = %v, want %v", body, want)
	}
}

func TestGetAgencyNotFoundForUnknownAndNotLiveAlike(t *testing.T) {
	pending := liveStore()
	pending.agencies = []store.Agency{{ID: uuid.New(), Slug: "pending-agency", Name: "Pending", Website: "https://example.com"}}

	unknown := getRaw(t, pending, "/v1/agencies/no-such-agency")
	notLive := getRaw(t, pending, "/v1/agencies/pending-agency")

	assertError(t, unknown, http.StatusNotFound, "not_found")
	assertError(t, notLive, http.StatusNotFound, "not_found")
	if unknown.Body.String() != notLive.Body.String() {
		t.Errorf("bodies differ: %q vs %q", unknown.Body.String(), notLive.Body.String())
	}
}

func TestGetAgencyStoreError(t *testing.T) {
	s := liveStore()
	s.getErr = errors.New("boom")
	assertError(t, getRaw(t, s, "/v1/agencies/la-metro"), http.StatusInternalServerError, "internal_error")
}

func TestListAgencyMeetingsShape(t *testing.T) {
	first := store.Meeting{ID: uuid.MustParse("0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e"), AgencyID: laMetro.ID, Title: "Board of Directors", StartsAt: time.Date(2026, 3, 26, 17, 0, 0, 0, time.UTC), Status: "held", SourceURL: "https://metro.legistar.com/m/1", SourceID: "1"}
	second := store.Meeting{ID: uuid.MustParse("1c7d2a1f-9b9e-4c2f-8c9a-2a3b4c5d6e7f"), AgencyID: laMetro.ID, Title: "Planning Committee", StartsAt: time.Date(2026, 3, 18, 18, 30, 0, 0, time.UTC), Status: "cancelled", SourceID: "2"}
	var call listMeetingsCall
	s := liveStore()
	s.page = store.MeetingPage{Meetings: []store.Meeting{first, second}, NextCursor: "next-page"}
	s.listed = &call

	rec := getRaw(t, s, "/v1/agencies/la-metro/meetings?limit=2&cursor=this-page")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	want := `{"meetings":[` +
		`{"id":"0b6c1f0e-8a8d-4b1e-9b8f-1f2a3b4c5d6e","sourceUrl":"https://metro.legistar.com/m/1","startsAt":"2026-03-26T17:00:00Z","status":"held","title":"Board of Directors"},` +
		`{"id":"1c7d2a1f-9b9e-4c2f-8c9a-2a3b4c5d6e7f","startsAt":"2026-03-18T18:30:00Z","status":"cancelled","title":"Planning Committee"}` +
		`],"nextCursor":"next-page"}` + "\n"
	if rec.Body.String() != want {
		t.Errorf("body =\n%s\nwant\n%s", rec.Body.String(), want)
	}
	if call.agencyID != laMetro.ID || call.params != (store.ListMeetingsParams{Cursor: "this-page", Limit: 2}) {
		t.Errorf("store called with %+v", call)
	}
}

func TestListAgencyMeetingsEmptyListHasNullCursor(t *testing.T) {
	rec := getRaw(t, liveStore(), "/v1/agencies/la-metro/meetings")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if want := `{"meetings":[],"nextCursor":null}` + "\n"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

func TestListAgencyMeetingsLimit(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", 0},
		{"?limit=1", 1},
		{"?limit=200", 200},
	} {
		var call listMeetingsCall
		s := liveStore()
		s.listed = &call
		rec := getRaw(t, s, "/v1/agencies/la-metro/meetings"+tc.query)
		if rec.Code != http.StatusOK || call.params.Limit != tc.want {
			t.Errorf("%q: status %d, store limit %d; want 200 and %d", tc.query, rec.Code, call.params.Limit, tc.want)
		}
	}
}

func TestListAgencyMeetingsRejectsBadLimit(t *testing.T) {
	for _, limit := range []string{"0", "-1", "201", "abc", "1.5", ""} {
		var call listMeetingsCall
		s := liveStore()
		s.listed = &call
		rec := getRaw(t, s, "/v1/agencies/la-metro/meetings?limit="+limit)
		assertError(t, rec, http.StatusBadRequest, "invalid_parameter")
		if call != (listMeetingsCall{}) {
			t.Errorf("limit %q: store was called", limit)
		}
	}
}

func TestListAgencyMeetingsRejectsBadCursor(t *testing.T) {
	s := liveStore()
	s.meetingsErr = fmt.Errorf("list: %w", store.ErrInvalidCursor)
	assertError(t, getRaw(t, s, "/v1/agencies/la-metro/meetings?cursor=tampered"), http.StatusBadRequest, "invalid_cursor")
}

func TestListAgencyMeetingsNotFoundForUnknownAndNotLiveAlike(t *testing.T) {
	var call listMeetingsCall
	s := liveStore()
	s.listed = &call
	unknown := getRaw(t, s, "/v1/agencies/no-such-agency/meetings")
	notLive := getRaw(t, s, "/v1/agencies/pending-agency/meetings")

	assertError(t, unknown, http.StatusNotFound, "not_found")
	assertError(t, notLive, http.StatusNotFound, "not_found")
	if unknown.Body.String() != notLive.Body.String() {
		t.Errorf("bodies differ: %q vs %q", unknown.Body.String(), notLive.Body.String())
	}
	if call != (listMeetingsCall{}) {
		t.Error("store listed meetings for an agency that is not live")
	}
}

func TestListAgencyMeetingsStoreErrors(t *testing.T) {
	lookup := liveStore()
	lookup.getErr = errors.New("boom")
	assertError(t, getRaw(t, lookup, "/v1/agencies/la-metro/meetings"), http.StatusInternalServerError, "internal_error")

	list := liveStore()
	list.meetingsErr = errors.New("boom")
	assertError(t, getRaw(t, list, "/v1/agencies/la-metro/meetings"), http.StatusInternalServerError, "internal_error")
}
