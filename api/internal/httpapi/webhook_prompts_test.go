package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const secretFocus = "Fare changes on the A Line, especially for students"

func directedEnv(t *testing.T) webhookEnv {
	t.Helper()
	return newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions, FlagDirectedPrompts))
}

func TestCreateWebhookSubscriptionWithAPrompt(t *testing.T) {
	env := directedEnv(t)
	body, _ := json.Marshal(map[string]string{"agency": "la-metro", "url": "https://hooks.example.com/x", "prompt": "  " + secretFocus + "\r\n "})

	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", string(body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	got, _ := decodeWebhookWithSecret(t, rec)
	if in := env.calls.created[0]; in.Prompt == nil || *in.Prompt != secretFocus {
		t.Errorf("stored prompt = %v, want it trimmed", in.Prompt)
	}
	if got.Prompt == nil || *got.Prompt != secretFocus {
		t.Errorf("response prompt = %v", got.Prompt)
	}
	if strings.Contains(env.logs.String(), "A Line") {
		t.Errorf("the logs hold the prompt: %s", env.logs)
	}

	rec = env.do(t, http.MethodGet, "/v1/webhook-subscriptions", "Bearer good", "")
	var list v1.WebhookSubscriptionList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Subscriptions) != 1 || list.Subscriptions[0].Prompt == nil {
		t.Errorf("list = %s, %v", rec.Body, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestCreateWebhookSubscriptionWithABlankPromptIsStandard(t *testing.T) {
	env := directedEnv(t)

	rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", `{"agency":"la-metro","url":"https://hooks.example.com/x","prompt":"   "}`)

	if rec.Code != http.StatusCreated || env.calls.created[0].Prompt != nil {
		t.Fatalf("status %d, stored prompt %v", rec.Code, env.calls.created[0].Prompt)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["prompt"]; ok {
		t.Errorf("a standard subscription has a prompt member: %s", rec.Body)
	}
}

func TestWebhookPromptIsRefused(t *testing.T) {
	long := strings.Repeat("é", store.MaxWebhookPromptChars+1)
	exact := strings.Repeat("é", store.MaxWebhookPromptChars)
	for _, tc := range []struct {
		name   string
		flags  []string
		prompt string
		code   string
	}{
		{"too long", nil, long, "prompt_too_long"},
		{"control character", nil, "fares\x07", "invalid_prompt"},
		{"nul", nil, "fares\x00", "invalid_prompt"},
		{"flag off", []string{FlagAccounts, FlagWebhookSubscriptions}, secretFocus, "prompts_unavailable"},
	} {
		flags := tc.flags
		if flags == nil {
			flags = []string{FlagAccounts, FlagWebhookSubscriptions, FlagDirectedPrompts}
		}
		env := newWebhookEnv(t, store.PlanPaid, webhookFlags(flags...))
		mine := env.add(t, env.user.ID)
		create, _ := json.Marshal(map[string]string{"agency": "la-metro", "url": "https://hooks.example.com/x", "prompt": tc.prompt})
		update, _ := json.Marshal(map[string]string{"prompt": tc.prompt})

		if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", string(create)); rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != tc.code {
			t.Errorf("%s: create status %d, body %s", tc.name, rec.Code, rec.Body)
		}
		if rec := env.do(t, http.MethodPatch, "/v1/webhook-subscriptions/"+mine.ID.String(), "Bearer good", string(update)); rec.Code != http.StatusBadRequest || decodeError(t, rec).Code != tc.code {
			t.Errorf("%s: update status %d, body %s", tc.name, rec.Code, rec.Body)
		}
		if len(env.calls.created) != 0 || len(env.calls.updates) != 0 {
			t.Errorf("%s: store called", tc.name)
		}
	}

	env := directedEnv(t)
	body, _ := json.Marshal(map[string]string{"agency": "la-metro", "url": "https://hooks.example.com/x", "prompt": exact})
	if rec := env.do(t, http.MethodPost, "/v1/webhook-subscriptions", "Bearer good", string(body)); rec.Code != http.StatusCreated {
		t.Errorf("a prompt of exactly %d characters: status %d, body %s", store.MaxWebhookPromptChars, rec.Code, rec.Body)
	}
}

func TestUpdateWebhookSubscriptionPrompt(t *testing.T) {
	env := directedEnv(t)
	mine := env.add(t, env.user.ID)
	path := "/v1/webhook-subscriptions/" + mine.ID.String()

	rec := env.do(t, http.MethodPatch, path, "Bearer good", `{"prompt":"Service changes\non the K Line\tand buses"}`)
	var got v1.WebhookSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Prompt == nil || *got.Prompt != "Service changes\non the K Line\tand buses" {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body)
	}
	if u := env.calls.updates[0]; u.Prompt == nil || u.URL != nil || u.Status != nil {
		t.Errorf("update = %+v", u)
	}

	rec = env.do(t, http.MethodPatch, path, "Bearer good", `{"prompt":""}`)
	got = v1.WebhookSubscription{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.Prompt != nil {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body)
	}
	if u := env.calls.updates[1]; u.Prompt == nil || *u.Prompt != "" {
		t.Errorf("clear update = %+v", u)
	}

	off := newWebhookEnv(t, store.PlanPaid, webhookFlags(FlagAccounts, FlagWebhookSubscriptions))
	theirs := off.add(t, off.user.ID)
	if rec := off.do(t, http.MethodPatch, "/v1/webhook-subscriptions/"+theirs.ID.String(), "Bearer good", `{"prompt":" "}`); rec.Code != http.StatusOK {
		t.Errorf("clearing with the flag off: status %d, body %s", rec.Code, rec.Body)
	}
}
