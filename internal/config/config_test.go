package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// validMasterKeyB64 is a properly-encoded 32-byte master key for tests.
// Computed once at package init to avoid recomputing in every test.
var validMasterKeyB64 = base64.StdEncoding.EncodeToString(make([]byte, 32))

func TestValidate_AcceptsValidConfig(t *testing.T) {
	c := validConfig()
	if err := c.validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidate_RejectsBadEnv(t *testing.T) {
	c := validConfig()
	c.Env = "staging"
	err := c.validate()
	if err == nil {
		t.Fatal("expected error for invalid env")
	}
	if !strings.Contains(err.Error(), "PORTCULLIS_ENV") {
		t.Errorf("error should mention PORTCULLIS_ENV: %v", err)
	}
}

func TestValidate_AccumulatesAllErrors(t *testing.T) {
	c := &Config{Env: EnvDevelopment}
	err := c.validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	wants := []string{
		"PORTCULLIS_DATABASE_URL",
		"PORTCULLIS_REDIS_URL",
		"PORTCULLIS_ADMIN_KEY",
		"PORTCULLIS_KEY_PEPPER",
		"PORTCULLIS_MASTER_KEY",
	}
	for _, w := range wants {
		if !strings.Contains(msg, w) {
			t.Errorf("error should mention %s; got: %v", w, err)
		}
	}
}

func TestValidate_RejectsShortSecrets(t *testing.T) {
	c := validConfig()
	c.AdminKey = "too-short"
	err := c.validate()
	if err == nil || !strings.Contains(err.Error(), "PORTCULLIS_ADMIN_KEY") {
		t.Fatalf("expected admin-key error, got %v", err)
	}
}

func TestValidate_RejectsZeroDefaults(t *testing.T) {
	c := validConfig()
	c.DefaultMaxRequests = 0
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "PORTCULLIS_DEFAULT_MAX_REQUESTS") {
		t.Errorf("expected error for zero max_requests, got %v", err)
	}

	c = validConfig()
	c.DefaultWindowSeconds = -1
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "PORTCULLIS_DEFAULT_WINDOW_SECONDS") {
		t.Errorf("expected error for negative window_seconds, got %v", err)
	}
}

func validConfig() *Config {
	return &Config{
		Env:                  EnvDevelopment,
		Addr:                 ":8080",
		LogLevel:             "info",
		DatabaseURL:          "postgres://localhost/x",
		RedisURL:             "redis://localhost",
		AdminKey:             strings.Repeat("a", 32),
		KeyPepper:            strings.Repeat("b", 32),
		MasterKey:            validMasterKeyB64,
		DefaultMaxRequests:   60,
		DefaultWindowSeconds: 60,
	}
}

// setRequiredEnv populates every required env var with valid placeholders.
// Tests then override the one variable they're exercising.
func setRequiredEnv(t *testing.T) {
	t.Helper()

	envs := map[string]string{
		"PORTCULLIS_ENV":          "development",
		"PORTCULLIS_DATABASE_URL": "postgres://x@localhost/x",
		"PORTCULLIS_REDIS_URL":    "redis://localhost:6379",
		"PORTCULLIS_ADMIN_KEY":    strings.Repeat("a", 64),
		"PORTCULLIS_KEY_PEPPER":   strings.Repeat("b", 64),
		"PORTCULLIS_MASTER_KEY":   validMasterKeyB64,
	}
	for k, v := range envs {
		t.Setenv(k, v)
	}
}

func TestConfig_MasterKeyValidation(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		wantError string // substring of expected error; "" = expect success
	}{
		{"valid", validMasterKeyB64, ""},
		{"empty", "", "is required"},
		{"not base64", "this is not base64 !!!!!!!!!!!!!!!!!!!!!!!!!", "invalid"},
		{"wrong length 16", base64.StdEncoding.EncodeToString(make([]byte, 16)), "invalid"},
		{"wrong length 64", base64.StdEncoding.EncodeToString(make([]byte, 64)), "invalid"},
		// The old check would have passed this (32 'a' chars); the new
		// one rejects it because base64-decoding 32 'a's gives 24 bytes.
		{"old-check-bypass", strings.Repeat("a", 32), "invalid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("PORTCULLIS_MASTER_KEY", tc.key)

			_, err := Load()

			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("Load: unexpected error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Load: want error containing %q, got nil", tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("Load: error %q does not contain %q", err, tc.wantError)
			}
		})
	}

	t.Run("MasterKeyBytes returns 32 bytes", func(t *testing.T) {
		setRequiredEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := len(cfg.MasterKeyBytes()); got != 32 {
			t.Errorf("MasterKeyBytes length: got %d, want 32", got)
		}
	})
}

// TestMain prevents the test file from inheriting env vars from the user's
// shell or a .env file. Each test sets its own.
func TestMain(m *testing.M) {
	for _, k := range os.Environ() {
		if strings.HasPrefix(k, "PORTCULLIS_") {
			name := k[:strings.IndexByte(k, '=')]
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}
