package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadWebhooks(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, WebhookSecretKeyBytes)
	encoded := base64.StdEncoding.EncodeToString(key)
	for name, tc := range map[string]struct {
		value      string
		configured bool
		problem    bool
	}{
		"unset":           {"", false, false},
		"valid":           {encoded, true, false},
		"padded":          {"  " + encoded + "\n", true, false},
		"too short":       {base64.StdEncoding.EncodeToString(key[:16]), false, true},
		"too long":        {base64.StdEncoding.EncodeToString(append(key, 1)), false, true},
		"not base64":      {"not base64 at all!", false, true},
		"url alphabet":    {base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, WebhookSecretKeyBytes)), false, true},
		"raw 32 byte key": {strings.Repeat("k", WebhookSecretKeyBytes), false, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("WEBHOOK_SECRET_KEY", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			w := cfg.Webhooks
			if w.Configured() != tc.configured || (w.Problem != "") != tc.problem {
				t.Fatalf("configured %v, problem %q", w.Configured(), w.Problem)
			}
			if tc.configured && w.SecretKey.Reveal() != string(key) {
				t.Error("the key was not decoded")
			}
			if tc.problem && (w.SecretKey.IsSet() || strings.Contains(w.Problem, tc.value)) {
				t.Errorf("a refused key was kept or named in the problem: %q", w.Problem)
			}
		})
	}
}

func TestWebhookKeyNeverPrints(t *testing.T) {
	marker := "WEBHOOKKEYMARKER-0123456789abcde"
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("WEBHOOK_SECRET_KEY", base64.StdEncoding.EncodeToString([]byte(marker)))
	cfg, err := Load()
	if err != nil || !cfg.Webhooks.Configured() {
		t.Fatalf("Load: %v, configured %v", err, cfg.Webhooks.Configured())
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("config", "webhooks", cfg.Webhooks)
	for _, out := range []string{fmt.Sprintf("%v %+v %#v %s", cfg, cfg, cfg, cfg.Webhooks.SecretKey), logs.String()} {
		if strings.Contains(out, marker) {
			t.Errorf("the webhook key was printed: %s", out)
		}
	}
}
