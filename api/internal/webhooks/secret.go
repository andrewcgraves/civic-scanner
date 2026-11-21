package webhooks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
)

const (
	SecretPrefix      = "whsec_"
	secretBytes       = 32
	sealVersion  byte = 1
	sealContext       = "civic-scanner webhook subscription secret"
)

var (
	ErrNotConfigured = errors.New("webhook secrets are not configured")
	ErrUnreadable    = errors.New("the stored webhook secret cannot be opened")
)

func NewSecret() config.Secret {
	b := make([]byte, secretBytes)
	_, _ = rand.Read(b)
	return config.NewSecret(SecretPrefix + base64.StdEncoding.EncodeToString(b))
}

type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(key config.Secret) (*Sealer, error) {
	if len(key.Reveal()) != config.WebhookSecretKeyBytes {
		return nil, fmt.Errorf("webhook secret key: want %d bytes: %w", config.WebhookSecretKeyBytes, ErrNotConfigured)
	}
	block, err := aes.NewCipher([]byte(key.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("webhook secret key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webhook secret key: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Configured() bool { return s != nil && s.aead != nil }

func (s *Sealer) Seal(subscriptionID uuid.UUID, secret config.Secret) ([]byte, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("webhook secret nonce: %w", err)
	}
	header := append([]byte{sealVersion}, nonce...)
	return s.aead.Seal(header, nonce, []byte(secret.Reveal()), additionalData(subscriptionID)), nil
}

func (s *Sealer) Open(subscriptionID uuid.UUID, sealed []byte) (config.Secret, error) {
	if !s.Configured() {
		return config.Secret{}, ErrNotConfigured
	}
	size := s.aead.NonceSize()
	if len(sealed) < 1+size+s.aead.Overhead() || sealed[0] != sealVersion {
		return config.Secret{}, ErrUnreadable
	}
	plain, err := s.aead.Open(nil, sealed[1:1+size], sealed[1+size:], additionalData(subscriptionID))
	if err != nil {
		return config.Secret{}, ErrUnreadable
	}
	return config.NewSecret(string(plain)), nil
}

func additionalData(subscriptionID uuid.UUID) []byte {
	return append([]byte(sealContext), subscriptionID[:]...)
}
