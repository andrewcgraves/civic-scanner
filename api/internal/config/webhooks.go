package config

import (
	"encoding/base64"
	"os"
	"strings"
)

const WebhookSecretKeyBytes = 32

type Webhooks struct {
	SecretKey Secret
	Problem   string
}

func (w Webhooks) Configured() bool { return w.SecretKey.IsSet() && w.Problem == "" }

func loadWebhooks() Webhooks {
	raw := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET_KEY"))
	if raw == "" {
		return Webhooks{}
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != WebhookSecretKeyBytes {
		return Webhooks{Problem: "WEBHOOK_SECRET_KEY must be 32 random bytes in standard base64, such as the output of openssl rand -base64 32"}
	}
	return Webhooks{SecretKey: NewSecret(string(key))}
}
