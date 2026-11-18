package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

type Storage struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool
}

func (s Storage) Configured() bool { return s != Storage{} }

func loadStorage() (Storage, error) {
	pathStyle, err := strconv.ParseBool(envOr("STORAGE_PATH_STYLE", "false"))
	if err != nil {
		return Storage{}, fmt.Errorf("STORAGE_PATH_STYLE: %w", err)
	}
	s := Storage{
		Endpoint:        os.Getenv("STORAGE_ENDPOINT"),
		Bucket:          os.Getenv("STORAGE_BUCKET"),
		AccessKeyID:     os.Getenv("STORAGE_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("STORAGE_SECRET_ACCESS_KEY"),
		PathStyle:       pathStyle,
	}
	if s == (Storage{}) {
		return s, nil
	}
	s.Region = envOr("STORAGE_REGION", "auto")
	if err := checkHTTPURL(s.Endpoint); err != nil {
		return Storage{}, fmt.Errorf("STORAGE_ENDPOINT: %w", err)
	}
	if u, _ := url.Parse(s.Endpoint); u.Path != "" && u.Path != "/" {
		return Storage{}, fmt.Errorf("STORAGE_ENDPOINT: must not have a path, got %q", s.Endpoint)
	}
	return s, nil
}
