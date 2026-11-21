package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
	signatureScheme = "v1,"
)

func signingKey(secret config.Secret) ([]byte, error) {
	encoded, ok := strings.CutPrefix(secret.Reveal(), SecretPrefix)
	if !ok {
		return nil, ErrUnreadable
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 {
		return nil, ErrUnreadable
	}
	return key, nil
}

func Sign(secret config.Secret, id string, at time.Time, body []byte) (string, error) {
	key, err := signingKey(secret)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte{'.'})
	mac.Write([]byte(strconv.FormatInt(at.Unix(), 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signatureScheme + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
