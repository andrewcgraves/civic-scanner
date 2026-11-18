package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const defaultWorkOSBaseURL = "https://api.workos.com"

type WorkOS struct {
	ClientID string
	BaseURL  string
	Issuers  []string
	Audience string
}

func (w WorkOS) Configured() bool { return w.ClientID != "" }

func (w WorkOS) JWKSURL() string {
	return strings.TrimSuffix(w.BaseURL, "/") + "/sso/jwks/" + url.PathEscape(w.ClientID)
}

func loadWorkOS() (WorkOS, error) {
	w := WorkOS{
		ClientID: strings.TrimSpace(os.Getenv("WORKOS_CLIENT_ID")),
		BaseURL:  envOr("WORKOS_BASE_URL", defaultWorkOSBaseURL),
		Audience: strings.TrimSpace(os.Getenv("WORKOS_AUDIENCE")),
	}
	if err := checkHTTPSOrigin(w.BaseURL); err != nil {
		return WorkOS{}, fmt.Errorf("WORKOS_BASE_URL: %w", err)
	}
	if !w.Configured() {
		return w, nil
	}
	w.Issuers = splitList(os.Getenv("WORKOS_ISSUER"))
	if len(w.Issuers) == 0 {
		w.Issuers = []string{strings.TrimSuffix(w.BaseURL, "/") + "/user_management/" + w.ClientID}
	}
	return w, nil
}

func checkHTTPSOrigin(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", raw)
	}
	if u.Host == "" {
		return errors.New("host is required")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("must be a bare origin such as %s, got %q", defaultWorkOSBaseURL, raw)
	}
	return nil
}
