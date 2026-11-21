package webhooks_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks/webhookstest"
)

func TestSignMatchesTheStandardWebhooksVector(t *testing.T) {
	secret := config.NewSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	got, err := webhooks.Sign(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if err != nil || got != "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=" {
		t.Fatalf("Sign = %q, %v", got, err)
	}
	header := http.Header{}
	header.Set(webhooks.HeaderID, "msg_p5jXN8AQM9LWM0D4loKWxJek")
	header.Set(webhooks.HeaderTimestamp, "1614265330")
	header.Set(webhooks.HeaderSignature, "v1,bm90IGl0 "+got)
	if ts, err := webhookstest.Verify(secret.Reveal(), header, []byte(`{"test": 2432232314}`)); err != nil || ts.Unix() != 1614265330 {
		t.Errorf("Verify = %v, %v", ts, err)
	}
	if _, err := webhookstest.Verify(secret.Reveal(), header, []byte(`{"test": 1}`)); !errors.Is(err, webhookstest.ErrBadSignature) {
		t.Errorf("Verify of another body = %v", err)
	}
}

func TestSignRefusesASecretItCannotDecode(t *testing.T) {
	for _, secret := range []string{"", "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "whsec_", "whsec_not base64!"} {
		if _, err := webhooks.Sign(config.NewSecret(secret), "id", time.Now(), nil); !errors.Is(err, webhooks.ErrUnreadable) {
			t.Errorf("Sign(%q) = %v, want ErrUnreadable", secret, err)
		}
	}
}

var (
	eventID   = uuid.MustParse("3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c")
	versionID = uuid.MustParse("7a6b5c4d-3e2f-4a1b-8c9d-0e1f2a3b4c5d")
	meetingID = uuid.MustParse("11111111-2222-4333-8444-555555555555")
	occurred  = time.Date(2026, 11, 17, 13, 0, 0, 0, time.FixedZone("PST", -8*3600))
)

func event(typ store.EventType, payload any) webhooks.Event {
	raw, _ := json.Marshal(payload)
	return webhooks.Event{
		ID:         eventID,
		Type:       typ,
		Payload:    raw,
		OccurredAt: occurred,
		AgencySlug: "la-metro",
		AgencyName: "LA Metro",
		Meeting: store.Meeting{
			ID:        meetingID,
			Title:     "Board of Directors",
			StartsAt:  time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC),
			SourceURL: "https://metro.example/meetings/1",
		},
	}
}

func TestPayloadV1(t *testing.T) {
	note := "Added: “14. Adopt the 2027 calendar”"
	for name, tc := range map[string]struct {
		event webhooks.Event
		want  string
	}{
		"agenda published": {
			event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1}),
			`{"agency":{"name":"LA Metro","slug":"la-metro"},"agenda":{"version":1,"versionId":"7a6b5c4d-3e2f-4a1b-8c9d-0e1f2a3b4c5d"},"id":"3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c","meeting":{"id":"11111111-2222-4333-8444-555555555555","sourceUrl":"https://metro.example/meetings/1","startsAt":"2026-11-20T18:00:00Z","title":"Board of Directors"},"occurredAt":"2026-11-17T21:00:00Z","payloadVersion":"v1","type":"agenda_published"}`,
		},
		"agenda changed with a note": {
			event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 2, ChangeNote: &note, NoteSource: store.NoteFromModel}),
			`{"agency":{"name":"LA Metro","slug":"la-metro"},"agenda":{"changeNote":"Added: “14. Adopt the 2027 calendar”","changeNoteSource":"model","version":2,"versionId":"7a6b5c4d-3e2f-4a1b-8c9d-0e1f2a3b4c5d"},"id":"3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c","meeting":{"id":"11111111-2222-4333-8444-555555555555","sourceUrl":"https://metro.example/meetings/1","startsAt":"2026-11-20T18:00:00Z","title":"Board of Directors"},"occurredAt":"2026-11-17T21:00:00Z","payloadVersion":"v1","type":"agenda_changed"}`,
		},
		"agenda changed without a note source": {
			event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 3, ChangeNote: &note}),
			`{"agency":{"name":"LA Metro","slug":"la-metro"},"agenda":{"changeNote":"Added: “14. Adopt the 2027 calendar”","version":3,"versionId":"7a6b5c4d-3e2f-4a1b-8c9d-0e1f2a3b4c5d"},"id":"3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c","meeting":{"id":"11111111-2222-4333-8444-555555555555","sourceUrl":"https://metro.example/meetings/1","startsAt":"2026-11-20T18:00:00Z","title":"Board of Directors"},"occurredAt":"2026-11-17T21:00:00Z","payloadVersion":"v1","type":"agenda_changed"}`,
		},
		"transcript ready": {
			event(store.EventTranscriptReady, store.TranscriptEventPayload{TranscriptID: versionID, RecordingID: meetingID}),
			`{"agency":{"name":"LA Metro","slug":"la-metro"},"id":"3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c","meeting":{"id":"11111111-2222-4333-8444-555555555555","sourceUrl":"https://metro.example/meetings/1","startsAt":"2026-11-20T18:00:00Z","title":"Board of Directors"},"occurredAt":"2026-11-17T21:00:00Z","payloadVersion":"v1","transcript":{"id":"7a6b5c4d-3e2f-4a1b-8c9d-0e1f2a3b4c5d","recordingId":"11111111-2222-4333-8444-555555555555"},"type":"transcript_ready"}`,
		},
	} {
		got, err := webhooks.Payload("v1", tc.event)
		if err != nil || string(got) != tc.want {
			t.Errorf("%s:\n got %s, %v\nwant %s", name, got, err, tc.want)
		}
	}
	noSource := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
	noSource.Meeting.SourceURL = ""
	if got, err := webhooks.Payload("v1", noSource); err != nil || strings.Contains(string(got), "sourceUrl") {
		t.Errorf("without a source URL: %s, %v", got, err)
	}
}

func TestPayloadRefusesWhatItCannotDescribe(t *testing.T) {
	good := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
	if _, err := webhooks.Payload("v2", good); !errors.Is(err, webhooks.ErrUnsupportedVersion) {
		t.Errorf("v2: %v", err)
	}
	for name, e := range map[string]webhooks.Event{
		"unknown type":       event("meeting_moved", map[string]string{}),
		"agenda without id":  event(store.EventAgendaPublished, map[string]int{"version": 1}),
		"agenda not object":  {ID: eventID, Type: store.EventAgendaChanged, Payload: json.RawMessage(`[]`)},
		"transcript missing": event(store.EventTranscriptReady, map[string]string{}),
	} {
		if _, err := webhooks.Payload("v1", e); !errors.Is(err, webhooks.ErrUnsupportedEvent) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

var testSecret = webhooks.NewSecret().Reveal()

func request(url string) webhooks.Request {
	return webhooks.Request{URL: url, ID: eventID.String(), Body: []byte(`{"ok":true}`), Secret: config.NewSecret(testSecret), At: time.Unix(1_800_000_000, 0)}
}

func TestSendDeliversASignedRequest(t *testing.T) {
	receiver := webhookstest.NewReceiver(t, testSecret, nil)
	sender := webhooks.NewSender("CivicScanner/test", receiver.SenderOptions()...)
	result := sender.Send(t.Context(), request(receiver.URL()))
	if !result.Succeeded() || result.StatusCode != http.StatusNoContent || result.Problem() != "" || result.Duration <= 0 {
		t.Fatalf("Send = %+v", result)
	}
	deliveries := receiver.Deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %d", len(deliveries))
	}
	d := deliveries[0]
	if d.VerifyErr != nil || d.Timestamp.Unix() != 1_800_000_000 || string(d.Body) != `{"ok":true}` {
		t.Errorf("delivery = %+v", d)
	}
	for header, want := range map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        "CivicScanner/test",
		"Webhook-Id":        eventID.String(),
		"Webhook-Timestamp": "1800000000",
	} {
		if got := d.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestSendReportsAnswersThatAreNotSuccess(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusBadRequest, http.StatusGone, http.StatusInternalServerError} {
		receiver := webhookstest.NewReceiver(t, testSecret, func(int) int { return status })
		result := webhooks.NewSender("ua", receiver.SenderOptions()...).Send(t.Context(), request(receiver.URL()))
		if result.Succeeded() || result.Refused() || result.StatusCode != status || !strings.Contains(result.Problem(), "answered") {
			t.Errorf("%d: %+v, %q", status, result, result.Problem())
		}
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	receiver := webhookstest.NewReceiver(t, testSecret, nil)
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL(), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	opts := append(receiver.SenderOptions(), webhooks.WithRootCAs(certPool(redirect)))
	result := webhooks.NewSender("ua", opts...).Send(t.Context(), request(redirect.URL))
	if result.Succeeded() || result.StatusCode != http.StatusTemporaryRedirect || len(receiver.Deliveries()) != 0 {
		t.Errorf("redirect: %+v, deliveries %d", result, len(receiver.Deliveries()))
	}
}

func TestSendRefusesNonPublicAddressesByDefault(t *testing.T) {
	receiver := webhookstest.NewReceiver(t, testSecret, nil)
	pool := certPool(receiver.Server)
	sender := webhooks.NewSender("ua", webhooks.WithRootCAs(pool))
	for _, url := range []string{
		receiver.URL(),
		strings.Replace(receiver.URL(), "127.0.0.1", "localhost", 1),
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/hooks",
		"https://10.0.0.1/hooks",
		"https://2130706433/hooks",
	} {
		result := sender.Send(t.Context(), request(url))
		if !result.Refused() || result.Succeeded() || !strings.Contains(result.Problem(), "not a public internet address") {
			t.Errorf("%s: %+v", url, result)
		}
	}
	if n := len(receiver.Deliveries()); n != 0 {
		t.Errorf("deliveries = %d", n)
	}
}

func TestSendRefusesAResolvedNonPublicAddress(t *testing.T) {
	receiver := webhookstest.NewReceiver(t, testSecret, nil)
	sender := webhooks.NewSender("ua", webhooks.WithRootCAs(certPool(receiver.Server)),
		webhooks.AllowAddress(func(a netip.Addr) bool { return !a.IsLoopback() }))
	url := strings.Replace(receiver.URL(), "127.0.0.1", "localhost.test.invalid", 1)
	result := sender.Send(t.Context(), request(url))
	if result.Succeeded() || len(receiver.Deliveries()) != 0 {
		t.Errorf("unresolvable host: %+v", result)
	}
	result = sender.Send(t.Context(), request(receiver.URL()))
	if !result.Refused() {
		t.Errorf("loopback with loopback disallowed: %+v", result)
	}
}

func TestSendGivesUpAfterTheTimeout(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	sender := webhooks.NewSender("ua", webhooks.AllowAddress(netip.Addr.IsLoopback), webhooks.WithRootCAs(certPool(slow)),
		webhooks.WithTimeout(200*time.Millisecond))
	result := sender.Send(t.Context(), request(slow.URL))
	if result.Succeeded() || result.Refused() || !errors.Is(result.Err, context.DeadlineExceeded) || !strings.Contains(result.Problem(), "in time") {
		t.Errorf("slow: %+v", result)
	}
}

func TestSendRefusesBadRequestsBeforeConnecting(t *testing.T) {
	sender := webhooks.NewSender("ua")
	for name, req := range map[string]webhooks.Request{
		"http":       request("http://hooks.example/a"),
		"not a url":  request("://"),
		"bad secret": {URL: "https://hooks.example/a", ID: "id", Secret: config.NewSecret("nope")},
	} {
		if result := sender.Send(t.Context(), req); result.Succeeded() || result.Err == nil || result.Problem() == "" {
			t.Errorf("%s: %+v", name, result)
		}
	}
}

func certPool(server *httptest.Server) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return pool
}
