package webhooks_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const fullTemplate = `{
  "text": "{{event.label}}: {{meeting.title}} ({{agency.name}})",
  "agency": {"name": "{{ agency.name }}", "slug": "{{agency.slug}}"},
  "meeting": {"id": "{{meeting.id}}", "title": "{{meeting.title}}", "startsAt": "{{meeting.startsAt}}", "source": "{{meeting.sourceUrl}}", "link": "{{meeting.link}}"},
  "event": {"id": "{{event.id}}", "type": "{{event.type}}", "summary": "{{event.summary}}", "at": "{{event.occurredAt}}", "test": "{{event.test}}"},
  "agenda": "v{{agenda.version}}: {{agenda.changeNote}}",
  "kept": [1, 2.50, -3e2, true, false, null, "plain", {}, []]
}`

func templateError(t *testing.T, src string) *webhooks.TemplateError {
	t.Helper()
	_, err := webhooks.ParseTemplate(src)
	problem, ok := errors.AsType[*webhooks.TemplateError](err)
	if !ok {
		t.Fatalf("ParseTemplate(%q) = %v, want a TemplateError", src, err)
	}
	return problem
}

func render(t *testing.T, src string, e webhooks.Event) []byte {
	t.Helper()
	tmpl, err := webhooks.ParseTemplate(src)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}
	body, err := tmpl.Render(e)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("rendered body is not JSON: %s", body)
	}
	return body
}

func TestTemplateFillsEveryPlaceholder(t *testing.T) {
	note := "Added: “14. Adopt the 2027 calendar”"
	e := event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 2, ChangeNote: &note, NoteSource: store.NoteFromModel})
	e.PageURL = pageURL
	body := render(t, fullTemplate, e)
	want := `{"text":"Agenda changed: Board of Directors (LA Metro)",` +
		`"agency":{"name":"LA Metro","slug":"la-metro"},` +
		`"meeting":{"id":"11111111-2222-4333-8444-555555555555","title":"Board of Directors","startsAt":"2026-11-20T18:00:00Z",` +
		`"source":"https://metro.example/meetings/1","link":"` + pageURL + `"},` +
		`"event":{"id":"3f1c2b0e-8a4d-4e7b-9c1a-0d2e3f4a5b6c","type":"agenda_changed","summary":"The agenda changed (now version 2).",` +
		`"at":"2026-11-17T21:00:00Z","test":"false"},` +
		`"agenda":"v2: Added: “14. Adopt the 2027 calendar”",` +
		`"kept":[1,2.50,-3e2,true,false,null,"plain",{},[]]}`
	if string(body) != want {
		t.Errorf("body\n got %s\nwant %s", body, want)
	}
}

func TestTemplateCoversEveryPlaceholder(t *testing.T) {
	for _, name := range webhooks.Placeholders() {
		if !strings.Contains(fullTemplate, name+"}}") && !strings.Contains(fullTemplate, name+" }}") {
			t.Errorf("fullTemplate does not use {{%s}}", name)
		}
	}
}

func TestTemplateLeavesMissingValuesEmpty(t *testing.T) {
	e := event(store.EventTranscriptReady, store.TranscriptEventPayload{TranscriptID: versionID, RecordingID: meetingID})
	e.Meeting.SourceURL = ""
	body := render(t, `{"v":"{{agenda.version}}","n":"{{agenda.changeNote}}","s":"{{meeting.sourceUrl}}","l":"{{meeting.link}}"}`, e)
	if string(body) != `{"v":"","n":"","s":"","l":""}` {
		t.Errorf("body = %s", body)
	}
}

func TestTemplateEscapesSubstitutedValues(t *testing.T) {
	for name, tc := range map[string]struct{ title, want string }{
		"quotes":        {`He said "stop" \ then left`, `"He said \"stop\" \\ then left"`},
		"newlines":      {"Line one\nLine two\r\n\tTabbed", `"Line one\nLine two\r\n\tTabbed"`},
		"unicode":       {"Ünïcödé 🚆 — 東京   sep", "\"Ünïcödé 🚆 — 東京 \\u2028 sep\""},
		"control":       {"bell\x07 nul\x00", `"bell\u0007 nul\u0000"`},
		"markup":        {"<script>&</script>", `"<script>&</script>"`},
		"breaking out":  {`", "injected": "yes`, `"\", \"injected\": \"yes"`},
		"braces":        {"{{agency.slug}}", `"{{agency.slug}}"`},
		"invalid utf-8": {"bad \xff byte", "\"bad � byte\""},
	} {
		e := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
		e.Meeting.Title = tc.title
		body := render(t, `{"title":"{{meeting.title}}"}`, e)
		if got := string(body); got != `{"title":`+tc.want+`}` {
			t.Errorf("%s: body = %s, want title %s", name, got, tc.want)
		}
		var decoded map[string]string
		if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) != 1 {
			t.Errorf("%s: body %s decodes to %v, %v", name, body, decoded, err)
		}
	}
}

func TestTemplateKeepsLiteralTextAsWritten(t *testing.T) {
	e := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
	body := render(t, `{"a\"b":"x\ny é 🚆 </>", "c":"{{agency.slug}}\t{{agency.slug}}"}`, e)
	if string(body) != "{\"a\\\"b\":\"x\\ny é 🚆 </>\",\"c\":\"la-metro\\tla-metro\"}" {
		t.Errorf("body = %s", body)
	}
}

func TestTemplateMarksTests(t *testing.T) {
	e, err := webhooks.SampleEvent(store.EventAgendaPublished, "la-metro", "LA Metro", occurred)
	if err != nil {
		t.Fatal(err)
	}
	body := render(t, `{"label":"{{event.label}}","test":"{{event.test}}"}`, e)
	if string(body) != `{"label":"Test: Agenda published","test":"true"}` {
		t.Errorf("body = %s", body)
	}
}

func TestTemplateRefusesInvalidJSON(t *testing.T) {
	for name, src := range map[string]string{
		"empty":           ``,
		"unclosed object": `{"a": "b"`,
		"trailing comma":  `{"a": "b",}`,
		"single quotes":   `{'a': 'b'}`,
		"bare word":       `{"a": nope}`,
		"two objects":     `{"a": 1} {"b": 2}`,
		"trailing text":   `{"a": 1} x`,
	} {
		problem := templateError(t, src)
		if problem.Code != webhooks.TemplateInvalidJSON || !strings.Contains(problem.Message, "byte") {
			t.Errorf("%s: %s %q", name, problem.Code, problem.Message)
		}
	}
}

func TestTemplateRefusesUnknownPlaceholders(t *testing.T) {
	problem := templateError(t, `{"embeds":[{"title":"x"},{"title":"{{meeting.titel}}"}]}`)
	if problem.Code != webhooks.TemplateUnknownName {
		t.Fatalf("code = %s", problem.Code)
	}
	for _, want := range []string{"{{meeting.titel}}", "`embeds[1].title`", "meeting.title", "agenda.changeNote"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("message %q does not name %q", problem.Message, want)
		}
	}
	for _, src := range []string{`{"a":"{{}}"}`, `{"a":"{{ AGENCY.NAME }}"}`, `{"a":"{{agency.name | upper}}"}`, `{"a":"{{#each items}}"}`} {
		if problem := templateError(t, src); problem.Code != webhooks.TemplateUnknownName {
			t.Errorf("%s: code = %s", src, problem.Code)
		}
	}
}

func TestTemplateRefusesOtherMistakes(t *testing.T) {
	for name, tc := range map[string]struct{ src, says string }{
		"array":            {`["{{agency.name}}"]`, "must be a JSON object"},
		"string":           {`"{{agency.name}}"`, "must be a JSON object"},
		"unclosed":         {`{"a":"{{agency.name"}`, "not closed"},
		"key placeholder":  {`{"{{agency.slug}}":"x"}`, "not in the key"},
		"duplicate key":    {`{"a":{"b":1,"b":2}}`, "`a.b` appears more than once"},
		"too deep":         {strings.Repeat(`{"a":`, 33) + `1` + strings.Repeat(`}`, 33), "deeper than 32"},
		"odd key in path":  {`{"my key":{"x":"{{nope}}"}}`, "`[\"my key\"].x`"},
		"placeholder only": {`{"a":"{{agency.name}"}`, "not closed"},
	} {
		problem := templateError(t, tc.src)
		if !strings.Contains(problem.Message, tc.says) {
			t.Errorf("%s: %s %q, want it to say %q", name, problem.Code, problem.Message, tc.says)
		}
	}
}

func TestTemplateSizeCap(t *testing.T) {
	pad := func(n int) string {
		src := `{"a":""}`
		return `{"a":"` + strings.Repeat("x", n-len(src)) + `"}`
	}
	if _, err := webhooks.ParseTemplate(pad(webhooks.MaxTemplateBytes)); err != nil {
		t.Errorf("a template of exactly the cap: %v", err)
	}
	problem := templateError(t, pad(webhooks.MaxTemplateBytes+1))
	if problem.Code != webhooks.TemplateTooLarge || !strings.Contains(problem.Message, "8192") {
		t.Errorf("%s %q", problem.Code, problem.Message)
	}
}

func TestTemplateRenderedSizeCap(t *testing.T) {
	note := strings.Repeat("n", 4096)
	e := event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 2, ChangeNote: &note})
	src := `{"a":[` + strings.TrimSuffix(strings.Repeat(`"{{agenda.changeNote}}",`, 100), ",") + `]}`
	tmpl, err := webhooks.ParseTemplate(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmpl.Render(e); !errors.Is(err, webhooks.ErrRenderedTemplateTooLarge) {
		t.Errorf("Render = %v", err)
	}
}

func TestCustomBody(t *testing.T) {
	e := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
	body, err := webhooks.Body(store.WebhookFormatCustom, store.WebhookPayloadV1, `{"m": "{{meeting.title}}"}`, e)
	if err != nil || string(body) != `{"m":"Board of Directors"}` {
		t.Errorf("Body = %s, %v", body, err)
	}
	if _, err := webhooks.Body(store.WebhookFormatCustom, store.WebhookPayloadV1, ``, e); err == nil {
		t.Error("a custom body without a template was rendered")
	}
	bad := event(store.EventAgendaPublished, store.AgendaEventPayload{})
	if _, err := webhooks.Body(store.WebhookFormatCustom, store.WebhookPayloadV1, `{"m": "x"}`, bad); !errors.Is(err, webhooks.ErrUnsupportedEvent) {
		t.Errorf("Body of a broken event = %v", err)
	}
}
