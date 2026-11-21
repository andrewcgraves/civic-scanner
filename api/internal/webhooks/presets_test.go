package webhooks_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden webhook bodies")

const pageURL = "https://civic.example/meetings/11111111-2222-4333-8444-555555555555"

func units(s string) int { return len(utf16.Encode([]rune(s))) }

func presetEvents() map[string]webhooks.Event {
	markdown := "Added *14.* Adopt the 2027 calendar & <@123> @everyone `now` # [link](https://x.example) ~~gone~~ ||spoiler||"
	diff := "Removed: “Item 9”"
	long := strings.Repeat("Ein Fahrplan für 2027 🚆 — ", 400)
	cases := map[string]webhooks.Event{
		"agenda_published":         event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1}),
		"agenda_changed_model":     event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 2, ChangeNote: &markdown, NoteSource: store.NoteFromModel}),
		"agenda_changed_diff":      event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 3, ChangeNote: &diff, NoteSource: store.NoteFromDiff}),
		"agenda_changed_truncated": event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 4, ChangeNote: &long, NoteSource: store.NoteFromModel}),
		"transcript_ready":         event(store.EventTranscriptReady, store.TranscriptEventPayload{TranscriptID: versionID, RecordingID: meetingID}),
	}
	for name, e := range cases {
		e.PageURL = pageURL
		cases[name] = e
	}
	noLink := cases["agenda_published"]
	noLink.PageURL = ""
	cases["agenda_published_no_link"] = noLink
	directed := cases["agenda_changed_diff"]
	directed.Message = "Student fares on the A Line drop to *$1* from July 1, approved 9 to 3 <@123>."
	cases["agenda_changed_directed"] = directed
	return cases
}

func TestPresetBodiesMatchGolden(t *testing.T) {
	for name, e := range presetEvents() {
		for _, format := range []store.WebhookFormat{store.WebhookFormatStandard, store.WebhookFormatDiscord, store.WebhookFormatSlack} {
			body, err := webhooks.Body(format, store.WebhookPayloadV1, "", e)
			if err != nil {
				t.Fatalf("%s %s: %v", format, name, err)
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, body, "", "  "); err != nil {
				t.Fatalf("%s %s: not JSON: %v", format, name, err)
			}
			pretty.WriteByte('\n')
			path := filepath.Clean(filepath.Join("testdata", string(format), name+".json"))
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pretty.Bytes(), want) {
				t.Errorf("%s differs (run go test ./internal/webhooks -run TestPresetBodiesMatchGolden -update):\n%s", path, pretty.Bytes())
			}
		}
	}
}

func TestStandardBodyIsThePayload(t *testing.T) {
	e := presetEvents()["agenda_changed_model"]
	want, err := webhooks.Payload(store.WebhookPayloadV1, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []store.WebhookFormat{store.WebhookFormatStandard, ""} {
		if got, err := webhooks.Body(format, store.WebhookPayloadV1, "", e); err != nil || !bytes.Equal(got, want) {
			t.Errorf("format %q: %s, %v", format, got, err)
		}
	}
}

func oversized() webhooks.Event {
	note := strings.Repeat("🚆*", 6000)
	e := event(store.EventAgendaChanged, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 2, ChangeNote: &note, NoteSource: store.NoteFromModel})
	e.Meeting.Title = strings.Repeat("Ω🚆", 400)
	e.AgencyName = strings.Repeat("Agency & <Sons> ", 400)
	e.PageURL = pageURL
	return e
}

func TestDiscordBodyFitsTheLimits(t *testing.T) {
	body, err := webhooks.Body(store.WebhookFormatDiscord, store.WebhookPayloadV1, "", oversized())
	if err != nil {
		t.Fatal(err)
	}
	var msg webhooks.DiscordMessage
	if err := json.Unmarshal(body, &msg); err != nil || len(msg.Embeds) != 1 {
		t.Fatalf("%s: %v", body, err)
	}
	embed := msg.Embeds[0]
	for name, got := range map[string][2]int{
		"title":       {units(embed.Title), webhooks.DiscordTitleMax},
		"description": {units(embed.Description), webhooks.DiscordDescriptionMax},
		"author":      {units(embed.Author.Name), webhooks.DiscordAuthorMax},
		"total":       {embed.TextUnits(), webhooks.DiscordEmbedTotalMax},
	} {
		if got[0] > got[1] || got[0] == 0 {
			t.Errorf("%s: %d characters, limit %d", name, got[0], got[1])
		}
	}
	for _, s := range []string{embed.Title, embed.Description, embed.Author.Name} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, utf8.RuneError) {
			t.Errorf("not valid text: %q", s)
		}
	}
	if !strings.HasSuffix(embed.Title, "…") || !strings.HasSuffix(embed.Author.Name, "…") {
		t.Errorf("cut text does not end with an ellipsis: %q, %q", embed.Title, embed.Author.Name)
	}
	if !strings.Contains(embed.Description, "…\n[Open the meeting]("+pageURL+")") || strings.Contains(embed.Description, `\…`) {
		t.Errorf("the cut note does not end with an ellipsis and a link to the meeting:\n%s", embed.Description)
	}
	if embed.URL != pageURL || msg.AllowedMentions.Parse == nil || len(msg.AllowedMentions.Parse) != 0 {
		t.Errorf("url %q, allowed mentions %#v", embed.URL, msg.AllowedMentions)
	}
}

func TestSlackBodyFitsTheLimits(t *testing.T) {
	body, err := webhooks.Body(store.WebhookFormatSlack, store.WebhookPayloadV1, "", oversized())
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type     string `json:"type"`
			Text     *struct{ Type, Text string }
			Elements []struct {
				Type string          `json:"type"`
				Text json.RawMessage `json:"text"`
				URL  string          `json:"url"`
			}
		}
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if n := units(msg.Text); n > webhooks.SlackTextMax || !strings.Contains(msg.Text, "&amp; &lt;Sons&gt;") {
		t.Errorf("text: %d characters, not escaped: %.80q", n, msg.Text)
	}
	if len(msg.Blocks) == 0 || len(msg.Blocks) > 50 {
		t.Fatalf("%d blocks", len(msg.Blocks))
	}
	button := ""
	for _, b := range msg.Blocks {
		limit := webhooks.SlackSectionMax
		if b.Type == "header" {
			limit = webhooks.SlackHeaderMax
		}
		if b.Text != nil && (units(b.Text.Text) > limit || b.Text.Text == "") {
			t.Errorf("%s: %d characters, limit %d", b.Type, units(b.Text.Text), limit)
		}
		if b.Type == "section" && !strings.HasSuffix(b.Text.Text, "…") {
			t.Errorf("the cut section does not end with an ellipsis: %.80q", b.Text.Text)
		}
		for _, el := range b.Elements {
			var text string
			if json.Unmarshal(el.Text, &text) == nil && units(text) > webhooks.SlackPlainTextMax {
				t.Errorf("%s element: %d characters", b.Type, units(text))
			}
			if el.Type == "button" {
				button = el.URL
			}
		}
	}
	if button != pageURL {
		t.Errorf("button links to %q, want %q", button, pageURL)
	}
}

func TestSlackBodyLeavesOutAnEmptyAgencyName(t *testing.T) {
	e := presetEvents()["agenda_published"]
	e.AgencyName = " \t"
	body, err := webhooks.Body(store.WebhookFormatSlack, store.WebhookPayloadV1, "", e)
	if err != nil || strings.Contains(string(body), `"text":""`) || !strings.Contains(string(body), "<!date^") {
		t.Errorf("%s, %v", body, err)
	}
}

func TestBodyRefusesWhatItCannotDescribe(t *testing.T) {
	good := event(store.EventAgendaPublished, store.AgendaEventPayload{AgendaVersionID: versionID, Version: 1})
	if _, err := webhooks.Body("teams", store.WebhookPayloadV1, "", good); !errors.Is(err, webhooks.ErrUnsupportedFormat) {
		t.Errorf("unknown format: %v", err)
	}
	bad := event(store.EventTranscriptReady, map[string]string{})
	for _, format := range []store.WebhookFormat{store.WebhookFormatDiscord, store.WebhookFormatSlack} {
		if _, err := webhooks.Body(format, store.WebhookPayloadV1, "", bad); !errors.Is(err, webhooks.ErrUnsupportedEvent) {
			t.Errorf("%s: %v", format, err)
		}
	}
}

func TestMeetingPage(t *testing.T) {
	m := event(store.EventAgendaPublished, nil).Meeting
	noSource := m
	noSource.SourceURL = ""
	unsafe := m
	unsafe.SourceURL = "javascript:alert(1)"
	long := m
	long.SourceURL = "https://metro.example/" + strings.Repeat("a", 2100)
	for name, tc := range map[string]struct {
		base    string
		meeting store.Meeting
		want    string
	}{
		"web page":            {"https://civic.example/", m, pageURL},
		"web page with path":  {"https://civic.example/app", m, "https://civic.example/app/meetings/" + meetingID.String()},
		"agency page":         {"", m, "https://metro.example/meetings/1"},
		"nothing":             {"", noSource, ""},
		"unsafe agency page":  {"", unsafe, ""},
		"too long agency URL": {"", long, ""},
	} {
		if got := webhooks.MeetingPage(tc.base, tc.meeting); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
