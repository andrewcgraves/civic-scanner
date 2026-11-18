package config

import "testing"

func clearStorageEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	for _, k := range []string{"STORAGE_ENDPOINT", "STORAGE_REGION", "STORAGE_BUCKET", "STORAGE_ACCESS_KEY_ID", "STORAGE_SECRET_ACCESS_KEY", "STORAGE_PATH_STYLE"} {
		t.Setenv(k, "")
	}
}

func TestLoadStorageUnsetIsNotConfigured(t *testing.T) {
	clearStorageEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load without storage settings: %v", err)
	}
	if cfg.Storage != (Storage{}) {
		t.Errorf("Storage = %+v, want zero", cfg.Storage)
	}
	if cfg.Storage.Configured() {
		t.Error("Configured() = true, want false")
	}
}

func TestLoadStorageFromEnv(t *testing.T) {
	clearStorageEnv(t)
	t.Setenv("STORAGE_ENDPOINT", "https://t3.storageapi.dev")
	t.Setenv("STORAGE_REGION", "auto")
	t.Setenv("STORAGE_BUCKET", "audio-abc123")
	t.Setenv("STORAGE_ACCESS_KEY_ID", "key")
	t.Setenv("STORAGE_SECRET_ACCESS_KEY", "secret")
	t.Setenv("STORAGE_PATH_STYLE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Storage{
		Endpoint:        "https://t3.storageapi.dev",
		Region:          "auto",
		Bucket:          "audio-abc123",
		AccessKeyID:     "key",
		SecretAccessKey: "secret",
		PathStyle:       true,
	}
	if cfg.Storage != want {
		t.Errorf("Storage = %+v, want %+v", cfg.Storage, want)
	}
	if !cfg.Storage.Configured() {
		t.Error("Configured() = false, want true")
	}
}

func TestLoadStorageRegionDefaultsToAuto(t *testing.T) {
	clearStorageEnv(t)
	t.Setenv("STORAGE_ENDPOINT", "http://localhost:9000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Region != "auto" {
		t.Errorf("Region = %q, want auto", cfg.Storage.Region)
	}
}

func TestLoadStorageErrors(t *testing.T) {
	cases := map[string]map[string]string{
		"endpoint without scheme": {"STORAGE_ENDPOINT": "t3.storageapi.dev"},
		"endpoint with path":      {"STORAGE_ENDPOINT": "https://t3.storageapi.dev/bucket"},
		"bad path style":          {"STORAGE_ENDPOINT": "https://t3.storageapi.dev", "STORAGE_PATH_STYLE": "sometimes"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			clearStorageEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Error("Load succeeded, want error")
			}
		})
	}
}
