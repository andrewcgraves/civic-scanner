package webhooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
)

const (
	DiscordTitleMax       = 256
	DiscordDescriptionMax = 4096
	DiscordAuthorMax      = 256
	DiscordFieldValueMax  = 1024
	DiscordEmbedTotalMax  = 6000
	SlackTextMax          = 4000
	SlackHeaderMax        = 150
	SlackSectionMax       = 3000
	SlackPlainTextMax     = 3000
	presetFooter          = "Civic Scanner"
	openMeeting           = "Open the meeting"
	byModel               = "Change described by a language model."
	slackDateFallback     = "Monday, January 2, 2006 at 3:04 PM UTC"
	testLabelPrefix       = "Test: "
)

type preset struct {
	label   string
	color   int
	summary string
	note    string
	byModel bool
	event   v1.WebhookEventV1
	link    string
}

func presetFor(e Event) (preset, error) {
	ev, err := eventV1(e)
	if err != nil {
		return preset{}, err
	}
	p := preset{event: ev, link: e.PageURL}
	switch ev.Type {
	case v1.WebhookAgendaPublished:
		p.label, p.color = "Agenda published", 0x2E7D32
		p.summary = fmt.Sprintf("The agenda is published (version %d).", ev.Agenda.Version)
	case v1.WebhookAgendaChanged:
		p.label, p.color = "Agenda changed", 0xF9A825
		p.summary = fmt.Sprintf("The agenda changed (now version %d).", ev.Agenda.Version)
		if ev.Agenda.ChangeNote != nil {
			p.note = *ev.Agenda.ChangeNote
			p.byModel = ev.Agenda.ChangeNoteSource != nil && *ev.Agenda.ChangeNoteSource == v1.WebhookChangeNoteFromModel
		}
	case v1.WebhookTranscriptReady:
		p.label, p.color = "Transcript ready", 0x1565C0
		p.summary = "The recording's transcript and summary are ready."
	}
	if e.Test {
		p.label = testLabelPrefix + p.label
	}
	return p, nil
}

func marshalPreset(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type DiscordMessage struct {
	Embeds          []DiscordEmbed         `json:"embeds"`
	AllowedMentions DiscordAllowedMentions `json:"allowed_mentions"`
}

type DiscordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type DiscordEmbed struct {
	Title       string         `json:"title"`
	URL         string         `json:"url,omitempty"`
	Description string         `json:"description"`
	Color       int            `json:"color"`
	Timestamp   string         `json:"timestamp"`
	Author      DiscordAuthor  `json:"author"`
	Fields      []DiscordField `json:"fields"`
	Footer      DiscordFooter  `json:"footer"`
}

type DiscordAuthor struct {
	Name string `json:"name"`
}

type DiscordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type DiscordFooter struct {
	Text string `json:"text"`
}

func (e DiscordEmbed) TextUnits() int {
	n := textUnits(e.Title) + textUnits(e.Description) + textUnits(e.Author.Name) + textUnits(e.Footer.Text)
	for _, f := range e.Fields {
		n += textUnits(f.Name) + textUnits(f.Value)
	}
	return n
}

func discordEscape(r rune) string {
	if strings.ContainsRune("\\*_~`|<>#-[@", r) {
		return `\` + string(r)
	}
	return string(r)
}

func discordLink(link string) string {
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(link)
}

func discordBody(e Event) ([]byte, error) {
	p, err := presetFor(e)
	if err != nil {
		return nil, err
	}
	title, _ := fit(p.event.Meeting.Title, DiscordTitleMax, nil)
	author, _ := fit(p.event.Agency.Name, DiscordAuthorMax, nil)
	description := "**" + p.label + "**\n" + p.summary
	if p.note != "" {
		credit := ""
		if p.byModel {
			credit = "\n\n*" + byModel + "*"
		}
		room := DiscordDescriptionMax - textUnits(description) - 2 - textUnits(credit)
		note, cut := fit(p.note, room, discordEscape)
		more := ""
		if cut && p.link != "" {
			more = "\n[" + openMeeting + "](" + discordLink(p.link) + ")"
			note, _ = fit(p.note, room-textUnits(more), discordEscape)
		}
		description += "\n\n" + note + more + credit
	}
	starts := p.event.Meeting.StartsAt.Unix()
	embed := DiscordEmbed{
		Title:       title,
		Description: description,
		Color:       p.color,
		Timestamp:   p.event.OccurredAt.UTC().Format(time.RFC3339),
		Author:      DiscordAuthor{Name: author},
		Fields:      []DiscordField{{Name: "Starts", Value: fmt.Sprintf("<t:%d:F> (<t:%d:R>)", starts, starts), Inline: true}},
		Footer:      DiscordFooter{Text: presetFooter},
	}
	if p.link != "" {
		embed.URL = p.link
	}
	if title == "" {
		embed.Title = p.label
	}
	return marshalPreset(DiscordMessage{Embeds: []DiscordEmbed{embed}, AllowedMentions: DiscordAllowedMentions{Parse: []string{}}})
}

type SlackMessage struct {
	Text   string       `json:"text"`
	Blocks []SlackBlock `json:"blocks"`
}

type SlackBlock struct {
	Type     string         `json:"type"`
	Text     *SlackText     `json:"text,omitempty"`
	Elements []SlackElement `json:"elements,omitempty"`
}

type SlackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

type SlackElement struct {
	Type  string `json:"type"`
	Text  any    `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
	URL   string `json:"url,omitempty"`
}

func slackPlain(s string) *SlackText {
	off := false
	return &SlackText{Type: "plain_text", Text: s, Emoji: &off}
}

func slackEscape(r rune) string {
	switch r {
	case '&':
		return "&amp;"
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	default:
		return string(r)
	}
}

func slackBody(e Event) ([]byte, error) {
	p, err := presetFor(e)
	if err != nil {
		return nil, err
	}
	off := false
	prefix := p.label + ": "
	header, _ := fit(prefix+p.event.Meeting.Title, SlackHeaderMax, nil)
	agency, _ := fit(p.event.Agency.Name, SlackPlainTextMax, nil)
	starts := p.event.Meeting.StartsAt
	when := fmt.Sprintf("Starts <!date^%d^{date_long_pretty} at {time}|%s>", starts.Unix(), starts.UTC().Format(slackDateFallback))
	section := p.summary
	if p.note != "" {
		note, _ := fit(p.note, SlackSectionMax-textUnits(section)-2, nil)
		section += "\n\n" + note
	}
	context := []SlackElement{{Type: "mrkdwn", Text: when}}
	if agency != "" {
		context = append([]SlackElement{{Type: "plain_text", Text: agency, Emoji: &off}}, context...)
	}
	blocks := []SlackBlock{
		{Type: "header", Text: slackPlain(header)},
		{Type: "context", Elements: context},
		{Type: "section", Text: slackPlain(section)},
	}
	if p.byModel {
		blocks = append(blocks, SlackBlock{Type: "context", Elements: []SlackElement{{Type: "plain_text", Text: byModel, Emoji: &off}}})
	}
	if p.link != "" {
		blocks = append(blocks, SlackBlock{Type: "actions", Elements: []SlackElement{
			{Type: "button", Text: slackPlain(openMeeting), URL: p.link},
		}})
	}
	text, _ := fit(prefix+p.event.Meeting.Title+" ("+p.event.Agency.Name+")", SlackTextMax, slackEscape)
	return marshalPreset(SlackMessage{Text: text, Blocks: blocks})
}
