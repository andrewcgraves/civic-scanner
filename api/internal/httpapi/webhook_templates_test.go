package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const (
	previewPath   = "/v1/webhook-templates/preview"
	validTemplate = `{"text":"{{event.label}}: {{meeting.title}} ({{agency.name}})","n":1}`
)

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateWebhookSubscriptionWithATemplate(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good",
		`{"agency":"la-metro","url":"https://hooks.example.com/x","format":"custom","template":`+jsonString(t, validTemplate)+`}`)
	if rec.Code != http.StatusCreated || len(env.calls.created) != 1 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	in := env.calls.created[0]
	if in.Format != store.WebhookFormatCustom || in.Template == nil || *in.Template != validTemplate {
		t.Errorf("created %+v", in)
	}
	got, _ := decodeWebhookWithSecret(t, rec)
	if got.Format != v1.WebhookFormatCustom || got.Template == nil || *got.Template != validTemplate {
		t.Errorf("response %+v", got)
	}
}

func TestCreateWebhookSubscriptionStoresATemplateWithAPreset(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good",
		`{"agency":"la-metro","url":"https://hooks.example.com/x","format":"slack","template":`+jsonString(t, validTemplate)+`}`)
	if rec.Code != http.StatusCreated || env.calls.created[0].Template == nil || env.calls.created[0].Format != store.WebhookFormatSlack {
		t.Fatalf("status %d, body %s, created %+v", rec.Code, rec.Body, env.calls.created)
	}
}

func TestCreateWebhookSubscriptionRefusesBadTemplates(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, says string
	}{
		{"custom without a template", `"format":"custom"`, "template_required", "needs a template"},
		{"invalid JSON", `"format":"custom","template":"{\"a\":"`, webhooks.TemplateInvalidJSON, "not valid JSON"},
		{"unknown placeholder", `"format":"custom","template":"{\"a\":\"{{meeting.name}}\"}"`, webhooks.TemplateUnknownName, "{{meeting.name}}"},
		{"not an object", `"format":"custom","template":"[1]"`, webhooks.TemplateInvalid, "JSON object"},
		{"too large", `"format":"custom","template":` + jsonString(t, `{"a":"`+strings.Repeat("x", webhooks.MaxTemplateBytes)+`"}`), webhooks.TemplateTooLarge, "8192"},
		{"bad template with a preset", `"format":"discord","template":"nope"`, webhooks.TemplateInvalidJSON, "not valid JSON"},
	} {
		env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
		rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", `{"agency":"la-metro","url":"https://hooks.example.com/x",`+tc.body+`}`)
		problem := decodeError(t, rec)
		if rec.Code != http.StatusBadRequest || problem.Code != tc.code || !strings.Contains(problem.Message, tc.says) {
			t.Errorf("%s: status %d, body %s", tc.name, rec.Code, rec.Body)
		}
		if len(env.calls.created) != 0 {
			t.Errorf("%s: stored %+v", tc.name, env.calls.created)
		}
	}
}

func TestCreateWebhookSubscriptionTakesATemplateNearTheCap(t *testing.T) {
	template := `{"a":"` + strings.Repeat(`\"`, (webhooks.MaxTemplateBytes-8)/2) + `"}`
	body := `{"agency":"la-metro","url":"https://hooks.example.com/x","format":"custom","template":` + jsonString(t, template) + `}`
	if len(body) <= maxRequestBodyBytes {
		t.Fatalf("the body is only %d bytes", len(body))
	}
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", body); rec.Code != http.StatusCreated {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
	huge := `{"agency":"la-metro","url":"https://hooks.example.com/x","template":"` + strings.Repeat("x", maxWebhookBodyBytes) + `"}`
	if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", huge); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d for a body over the limit", rec.Code)
	}
}

func TestUpdateWebhookSubscriptionTemplate(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	sub := env.add(t, env.user.ID)
	path := "/v1/webhook-subscriptions/" + sub.ID.String()

	rec := env.do(t, http.MethodPatch, path, "Bearer good", `{"format":"custom"}`)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "template_required" || len(env.calls.updates) != 0 {
		t.Fatalf("custom without a stored template: status %d, body %s", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"template":"{\"a\":\"{{nope}}\"}"}`)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != webhooks.TemplateUnknownName || len(env.calls.updates) != 0 {
		t.Fatalf("unknown placeholder: status %d, body %s", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"format":"custom","template":`+jsonString(t, validTemplate)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	update := env.calls.updates[0]
	if update.Format == nil || *update.Format != store.WebhookFormatCustom || update.Template == nil || *update.Template != validTemplate {
		t.Errorf("update %+v", update)
	}
	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"format":"slack"}`)
	if rec.Code != http.StatusOK || env.calls.subs[0].Template == nil {
		t.Fatalf("to slack: status %d, body %s", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"format":"custom"}`)
	if rec.Code != http.StatusOK || env.calls.subs[0].Format != store.WebhookFormatCustom {
		t.Errorf("back to custom with the stored template: status %d, body %s", rec.Code, rec.Body)
	}
	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"template":`+jsonString(t, `{"b":"{{agency.slug}}"}`)+`}`)
	if rec.Code != http.StatusOK || *env.calls.subs[0].Template != `{"b":"{{agency.slug}}"}` {
		t.Errorf("template only: status %d, body %s", rec.Code, rec.Body)
	}
}

func TestUpdateWebhookSubscriptionTemplateRaceIsRefused(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	sub := env.add(t, env.user.ID)
	template := validTemplate
	env.calls.subs[0].Template = &template
	env.calls.updateErr = store.ErrWebhookTemplateRequired
	rec := env.do(t, http.MethodPatch, "/v1/webhook-subscriptions/"+sub.ID.String(), "Bearer good", `{"format":"custom"}`)
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != "template_required" {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func preview(t *testing.T, env webhookEnv, body string) (int, v1.WebhookTemplatePreview, v1.Error) {
	t.Helper()
	rec := env.do(t, http.MethodPost, previewPath, "Bearer good", body)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Code != http.StatusOK {
		return rec.Code, v1.WebhookTemplatePreview{}, decodeError(t, rec)
	}
	var out v1.WebhookTemplatePreview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return rec.Code, out, v1.Error{}
}

func TestPreviewWebhookTemplate(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)}
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags, WithClock(clock.Now))
	template := `{"t":"{{event.label}} | {{meeting.title}} | {{agency.name}} ({{agency.slug}}) | {{agenda.version}} | {{agenda.changeNote}} | {{event.test}} | {{meeting.link}}","n":1.5}`
	status, out, _ := preview(t, env, `{"type":"agenda_changed","template":`+jsonString(t, template)+`}`)
	if status != http.StatusOK || out.Type != v1.WebhookAgendaChanged {
		t.Fatalf("status %d, %+v", status, out)
	}
	want := `{"t":"Test: Agenda changed | ` + webhooks.SampleMeetingTitle + ` | Sample agency (sample-agency) | 2 | ` + webhooks.SampleChangeNote + ` | true | ","n":1.5}`
	if out.Body != want {
		t.Errorf("body\n got %s\nwant %s", out.Body, want)
	}
	if len(env.calls.created)+len(env.calls.updates) != 0 {
		t.Errorf("the preview changed the store")
	}
}

func TestPreviewWebhookTemplateDefaultsToAgendaPublished(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	status, out, _ := preview(t, env, `{"template":"{\"type\":\"{{event.type}}\"}"}`)
	if status != http.StatusOK || out.Type != v1.WebhookAgendaPublished || out.Body != `{"type":"agenda_published"}` {
		t.Errorf("status %d, %+v", status, out)
	}
}

func TestPreviewWebhookTemplateUsesALiveAgency(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	env.h.store = fakeStore{users: env.users, webhooks: env.calls, live: map[string]store.Agency{
		"la-metro": {ID: uuid.New(), Slug: "la-metro", Name: `LA "Metro"`},
	}}
	status, out, _ := preview(t, env, `{"agency":" la-metro ","template":"{\"a\":\"{{agency.name}}/{{agency.slug}}\"}"}`)
	if status != http.StatusOK || out.Body != `{"a":"LA \"Metro\"/la-metro"}` {
		t.Errorf("status %d, %+v", status, out)
	}
	status, _, problem := preview(t, env, `{"agency":"nowhere","template":"{}"}`)
	if status != http.StatusBadRequest || problem.Code != "unknown_agency" {
		t.Errorf("unknown agency: status %d, %+v", status, problem)
	}
	env.h.store = fakeStore{users: env.users, webhooks: env.calls, getErr: errors.New("database down")}
	if status, _, problem := preview(t, env, `{"agency":"la-metro","template":"{}"}`); status != http.StatusInternalServerError || problem.Code != "internal_error" {
		t.Errorf("database error: status %d, %+v", status, problem)
	}
}

func TestPreviewWebhookTemplateRefuses(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	for _, tc := range []struct {
		name, body, code, says string
	}{
		{"no template", `{}`, webhooks.TemplateInvalidJSON, "ends early"},
		{"invalid JSON", `{"template":"{\"a\" 1}"}`, webhooks.TemplateInvalidJSON, "byte"},
		{"unknown placeholder", `{"template":"{\"a\":\"{{ meeting.summary }}\"}"}`, webhooks.TemplateUnknownName, "{{ meeting.summary }}"},
		{"placeholder in a key", `{"template":"{\"{{agency.slug}}\":1}"}`, webhooks.TemplateInvalid, "not in the key"},
		{"bad type", `{"type":"meeting_cancelled","template":"{}"}`, "invalid_event_type", "type must be"},
		{"too large", `{"template":` + jsonString(t, `{"a":"`+strings.Repeat("é", webhooks.MaxTemplateBytes/2)+`"}`) + `}`, webhooks.TemplateTooLarge, "at most 8192"},
	} {
		status, _, problem := preview(t, env, tc.body)
		if status != http.StatusBadRequest || problem.Code != tc.code || !strings.Contains(problem.Message, tc.says) {
			t.Errorf("%s: status %d, %+v", tc.name, status, problem)
		}
	}
}

func TestPreviewWebhookTemplateFailsClosedWithoutThePaidGate(t *testing.T) {
	env := newWebhookEnv(t, store.PlanPaid, bothWebhookFlags)
	resp, err := env.h.PreviewWebhookTemplate(t.Context(), v1.PreviewWebhookTemplateRequestObject{Body: &v1.WebhookTemplatePreviewInput{Template: "{}"}})
	if _, ok := resp.(v1.PreviewWebhookTemplate402JSONResponse); err != nil || !ok {
		t.Errorf("response %T, %v", resp, err)
	}
}

func TestSendWebhookTestRendersACustomTemplate(t *testing.T) {
	env, sub, sender, _ := newTestSendEnv(t)
	template := `{"content":"{{event.label}}: {{meeting.title}}","id":"{{event.id}}","test":"{{event.test}}"}`
	env.calls.subs[0].Format = store.WebhookFormatCustom
	env.calls.subs[0].Template = &template
	rec := env.do(t, http.MethodPost, testPath(sub.ID), "Bearer good", `{"type":"agenda_changed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	result, _ := decodeTestResult(t, rec)
	sent := sender.sent()
	if len(sent) != 1 || sent[0].ID != result.Id.String() {
		t.Fatalf("sent = %+v", sent)
	}
	want := `{"content":"Test: Agenda changed: ` + webhooks.SampleMeetingTitle + `","id":"` + result.Id.String() + `","test":"true"}`
	if string(sent[0].Body) != want {
		t.Errorf("body\n got %s\nwant %s", sent[0].Body, want)
	}
}
