package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Web struct {
	PublicURL string
}

func loadWeb() (Web, error) {
	raw := strings.TrimSpace(os.Getenv("PUBLIC_WEB_URL"))
	if raw == "" {
		return Web{}, nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
	case u.Scheme != "http" && u.Scheme != "https":
		err = errors.New("must be an http or https URL")
	case u.Host == "":
		err = errors.New("must have a host")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		err = errors.New("must not have user info, a query or a fragment")
	}
	if err != nil {
		return Web{}, fmt.Errorf("PUBLIC_WEB_URL: %w", err)
	}
	return Web{PublicURL: strings.TrimRight(u.String(), "/")}, nil
}
