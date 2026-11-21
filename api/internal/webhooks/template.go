package webhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTemplateBytes         = 8 << 10
	MaxRenderedTemplateBytes = 256 << 10
	maxTemplateDepth         = 32
	placeholderOpen          = "{{"
	placeholderClose         = "}}"
)

const (
	TemplateTooLarge    = "template_too_large"
	TemplateInvalidJSON = "invalid_template_json"
	TemplateInvalid     = "invalid_template"
	TemplateUnknownName = "unknown_placeholder"
)

const (
	PlaceholderAgencyName       = "agency.name"
	PlaceholderAgencySlug       = "agency.slug"
	PlaceholderMeetingID        = "meeting.id"
	PlaceholderMeetingTitle     = "meeting.title"
	PlaceholderMeetingStartsAt  = "meeting.startsAt"
	PlaceholderMeetingSourceURL = "meeting.sourceUrl"
	PlaceholderMeetingLink      = "meeting.link"
	PlaceholderEventID          = "event.id"
	PlaceholderEventType        = "event.type"
	PlaceholderEventLabel       = "event.label"
	PlaceholderEventSummary     = "event.summary"
	PlaceholderEventOccurredAt  = "event.occurredAt"
	PlaceholderEventTest        = "event.test"
	PlaceholderAgendaVersion    = "agenda.version"
	PlaceholderAgendaChangeNote = "agenda.changeNote"
)

var placeholderNames = []string{
	PlaceholderAgencyName,
	PlaceholderAgencySlug,
	PlaceholderMeetingID,
	PlaceholderMeetingTitle,
	PlaceholderMeetingStartsAt,
	PlaceholderMeetingSourceURL,
	PlaceholderMeetingLink,
	PlaceholderEventID,
	PlaceholderEventType,
	PlaceholderEventLabel,
	PlaceholderEventSummary,
	PlaceholderEventOccurredAt,
	PlaceholderEventTest,
	PlaceholderAgendaVersion,
	PlaceholderAgendaChangeNote,
}

var ErrRenderedTemplateTooLarge = errors.New("the rendered template is too large")

func Placeholders() []string { return slices.Clone(placeholderNames) }

type TemplateError struct {
	Code    string
	Message string
}

func (e *TemplateError) Error() string { return e.Message }

func templateError(code, format string, args ...any) *TemplateError {
	return &TemplateError{Code: code, Message: fmt.Sprintf(format, args...)}
}

type nodeKind int

const (
	nodeLiteral nodeKind = iota
	nodeString
	nodeArray
	nodeObject
)

type stringPart struct {
	text        string
	placeholder string
}

type node struct {
	kind    nodeKind
	literal string
	parts   []stringPart
	keys    []string
	items   []node
}

type Template struct {
	root node
}

func ParseTemplate(src string) (*Template, error) {
	if len(src) > MaxTemplateBytes {
		return nil, templateError(TemplateTooLarge, "the template is %d bytes; it may be at most %d", len(src), MaxTemplateBytes)
	}
	if !utf8.ValidString(src) {
		return nil, templateError(TemplateInvalid, "the template is not valid UTF-8")
	}
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	p := templateParser{dec: dec}
	tok, err := p.token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, templateError(TemplateInvalid, "the template must be a JSON object")
	}
	root, err := p.object("", 1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, templateError(TemplateInvalidJSON, "the template has data after the JSON object, at byte %d", dec.InputOffset())
	}
	return &Template{root: root}, nil
}

type templateParser struct {
	dec *json.Decoder
}

func (p templateParser) token() (json.Token, error) {
	tok, err := p.dec.Token()
	if err == nil {
		return tok, nil
	}
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		return nil, templateError(TemplateInvalidJSON, "the template is not valid JSON: %s, at byte %d", syntax.Error(), syntax.Offset)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, templateError(TemplateInvalidJSON, "the template is not valid JSON: it ends early, at byte %d", p.dec.InputOffset())
	}
	return nil, templateError(TemplateInvalidJSON, "the template is not valid JSON: %s", err.Error())
}

func (p templateParser) value(tok json.Token, path string, depth int) (node, error) {
	switch v := tok.(type) {
	case json.Delim:
		if depth >= maxTemplateDepth {
			return node{}, templateError(TemplateInvalid, "the template nests deeper than %d levels at %s", maxTemplateDepth, displayPath(path))
		}
		if v == '{' {
			return p.object(path, depth+1)
		}
		return p.array(path, depth+1)
	case string:
		parts, err := parsePlaceholders(v, path)
		return node{kind: nodeString, parts: parts}, err
	case json.Number:
		return node{kind: nodeLiteral, literal: v.String()}, nil
	case bool:
		return node{kind: nodeLiteral, literal: strconv.FormatBool(v)}, nil
	case nil:
		return node{kind: nodeLiteral, literal: "null"}, nil
	default:
		return node{}, templateError(TemplateInvalidJSON, "the template is not valid JSON at %s", displayPath(path))
	}
}

func (p templateParser) object(path string, depth int) (node, error) {
	n := node{kind: nodeObject}
	seen := make(map[string]bool)
	for p.dec.More() {
		tok, err := p.token()
		if err != nil {
			return node{}, err
		}
		key, ok := tok.(string)
		if !ok {
			return node{}, templateError(TemplateInvalidJSON, "the template is not valid JSON at %s", displayPath(path))
		}
		child := joinPath(path, key)
		if strings.Contains(key, placeholderOpen) {
			return node{}, templateError(TemplateInvalid, "placeholders may only appear in string values, not in the key %s", displayPath(child))
		}
		if seen[key] {
			return node{}, templateError(TemplateInvalid, "the key %s appears more than once", displayPath(child))
		}
		seen[key] = true
		tok, err = p.token()
		if err != nil {
			return node{}, err
		}
		item, err := p.value(tok, child, depth)
		if err != nil {
			return node{}, err
		}
		n.keys = append(n.keys, key)
		n.items = append(n.items, item)
	}
	if _, err := p.token(); err != nil {
		return node{}, err
	}
	return n, nil
}

func (p templateParser) array(path string, depth int) (node, error) {
	n := node{kind: nodeArray}
	for i := 0; p.dec.More(); i++ {
		tok, err := p.token()
		if err != nil {
			return node{}, err
		}
		item, err := p.value(tok, path+"["+strconv.Itoa(i)+"]", depth)
		if err != nil {
			return node{}, err
		}
		n.items = append(n.items, item)
	}
	if _, err := p.token(); err != nil {
		return node{}, err
	}
	return n, nil
}

func joinPath(path, key string) string {
	if isPlainKey(key) {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	quoted, _ := json.Marshal(key)
	return path + "[" + string(quoted) + "]"
}

func isPlainKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func displayPath(path string) string {
	if path == "" {
		return "the top level"
	}
	return "`" + path + "`"
}

func parsePlaceholders(s, path string) ([]stringPart, error) {
	var parts []stringPart
	rest := s
	for {
		open := strings.Index(rest, placeholderOpen)
		if open < 0 {
			if rest != "" {
				parts = append(parts, stringPart{text: rest})
			}
			return parts, nil
		}
		if open > 0 {
			parts = append(parts, stringPart{text: rest[:open]})
		}
		rest = rest[open+len(placeholderOpen):]
		end := strings.Index(rest, placeholderClose)
		if end < 0 {
			return nil, templateError(TemplateInvalid, "a placeholder in %s is not closed with }}", displayPath(path))
		}
		raw := rest[:end]
		name := strings.TrimSpace(raw)
		if !slices.Contains(placeholderNames, name) {
			return nil, templateError(TemplateUnknownName, "unknown placeholder {{%s}} in %s; the placeholders are %s",
				raw, displayPath(path), strings.Join(placeholderNames, ", "))
		}
		parts = append(parts, stringPart{placeholder: name})
		rest = rest[end+len(placeholderClose):]
	}
}

func (t *Template) Render(e Event) ([]byte, error) {
	values, err := templateValues(e)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeNode(&buf, t.root, values); err != nil {
		return nil, err
	}
	if buf.Len() > MaxRenderedTemplateBytes {
		return nil, fmt.Errorf("%w: %d bytes, at most %d", ErrRenderedTemplateTooLarge, buf.Len(), MaxRenderedTemplateBytes)
	}
	return buf.Bytes(), nil
}

func templateValues(e Event) (map[string]string, error) {
	p, err := presetFor(e)
	if err != nil {
		return nil, err
	}
	ev := p.event
	values := map[string]string{
		PlaceholderAgencyName:       ev.Agency.Name,
		PlaceholderAgencySlug:       ev.Agency.Slug,
		PlaceholderMeetingID:        ev.Meeting.Id.String(),
		PlaceholderMeetingTitle:     ev.Meeting.Title,
		PlaceholderMeetingStartsAt:  ev.Meeting.StartsAt.UTC().Format(time.RFC3339),
		PlaceholderMeetingSourceURL: "",
		PlaceholderMeetingLink:      p.link,
		PlaceholderEventID:          ev.Id.String(),
		PlaceholderEventType:        string(ev.Type),
		PlaceholderEventLabel:       p.label,
		PlaceholderEventSummary:     p.summary,
		PlaceholderEventOccurredAt:  ev.OccurredAt.UTC().Format(time.RFC3339),
		PlaceholderEventTest:        strconv.FormatBool(e.Test),
		PlaceholderAgendaVersion:    "",
		PlaceholderAgendaChangeNote: "",
	}
	if ev.Meeting.SourceUrl != nil {
		values[PlaceholderMeetingSourceURL] = *ev.Meeting.SourceUrl
	}
	if ev.Agenda != nil {
		values[PlaceholderAgendaVersion] = strconv.Itoa(ev.Agenda.Version)
		if ev.Agenda.ChangeNote != nil {
			values[PlaceholderAgendaChangeNote] = *ev.Agenda.ChangeNote
		}
	}
	return values, nil
}

func writeNode(buf *bytes.Buffer, n node, values map[string]string) error {
	switch n.kind {
	case nodeLiteral:
		buf.WriteString(n.literal)
	case nodeString:
		var s strings.Builder
		for _, part := range n.parts {
			if part.placeholder != "" {
				s.WriteString(values[part.placeholder])
			} else {
				s.WriteString(part.text)
			}
		}
		return writeJSONString(buf, s.String())
	case nodeArray:
		buf.WriteByte('[')
		for i, item := range n.items {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeNode(buf, item, values); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case nodeObject:
		buf.WriteByte('{')
		for i, key := range n.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSONString(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeNode(buf, n.items[i], values); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	}
	return nil
}

func writeJSONString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1)
	return nil
}

func customBody(template string, e Event) ([]byte, error) {
	t, err := ParseTemplate(template)
	if err != nil {
		return nil, fmt.Errorf("parse the subscription's template: %w", err)
	}
	return t.Render(e)
}
