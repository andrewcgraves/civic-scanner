//go:build integration

package jobs_test

import (
	"encoding/json"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

func TestCustomTemplateIsDeliveredSigned(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	sealer := webhookSealer(t)
	slug := "jobs-test-webhook-template-" + randomSuffix(t)
	agencyID := seedAgency(t, pool, slug, "live")
	meeting := seedMeeting(t, pool, agencyID, "template")
	s := subscribe(t, pool, sealer, slug, true, nil)

	template := `{"content": "{{event.label}}: {{meeting.title}}", "note": "{{agenda.changeNote}}", "meta": {"id": "{{event.id}}", "n": 7, "ok": true}}`
	custom := store.WebhookFormatCustom
	if _, err := store.New(pool).UpdateWebhookSubscription(ctx, s.user.ID, s.sub.ID, store.WebhookSubscriptionUpdate{Format: &custom, Template: &template}); err != nil {
		t.Fatal(err)
	}

	h := start(t, pool, jobs.Config{
		Webhooks: jobs.Webhooks{Sealer: sealer, Sender: webhooks.NewSender("CivicScanner/test", s.receiver.SenderOptions()...)},
	})
	note := "Added: “Item \"14\"”\nRemoved: 🚆 </script>"
	event := recordAgendaChange(t, pool, meeting.ID, note)
	h.fanOut(event.ID)
	deliveries := h.deliveryJobs(event.ID)
	if len(deliveries) != 1 {
		t.Fatalf("delivery jobs = %v", deliveries)
	}
	for _, id := range deliveries {
		testdb.WaitForJobState(t, pool, id, "completed")
	}

	got := s.receiver.Deliveries()
	if len(got) != 1 {
		t.Fatalf("got %d deliveries", len(got))
	}
	d := got[0]
	if d.VerifyErr != nil || d.Header.Get("webhook-id") != event.ID.String() || d.Header.Get("Content-Type") != "application/json" {
		t.Errorf("delivery = %+v, verify %v", d.Header, d.VerifyErr)
	}
	want := `{"content":"Agenda changed: ` + meeting.Title + `","note":"Added: “Item \"14\"”\nRemoved: 🚆 </script>","meta":{"id":"` + event.ID.String() + `","n":7,"ok":true}}`
	if string(d.Body) != want {
		t.Errorf("body\n got %s\nwant %s", d.Body, want)
	}
	var decoded struct{ Note string }
	if err := json.Unmarshal(d.Body, &decoded); err != nil || decoded.Note != note {
		t.Errorf("note = %q, %v", decoded.Note, err)
	}
	if attempts := mustAttempts(t, pool, s.sub.ID); len(attempts) != 1 || !attempts[0].Succeeded {
		t.Errorf("attempts = %+v", attempts)
	}
}
