package config

import (
	"strings"
	"testing"
)

func TestValidate_AcceptsValidConfig(t *testing.T) {
	c := &Config{
		Env:                  EnvDevelopment,
		Addr:                 ":8080",
		LogLevel:             "info",
		DatabaseURL:          "postgres://localhost/x",
		RedisURL:             "redis://localhost",
		AdminKey:             strings.Repeat("a", 32),
		KeyPepper:            strings.Repeat("b", 32),
		MasterKey:            strings.Repeat("c", 32),
		DefaultMaxRequests:   60,
		DefaultWindowSeconds: 60,
	}
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
	c := &Config{Env: EnvDevelopment} // everything else empty
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

func validConfig() *Config {
	return &Config{
		Env:                  EnvDevelopment,
		Addr:                 ":8080",
		LogLevel:             "info",
		DatabaseURL:          "postgres://localhost/x",
		RedisURL:             "redis://localhost",
		AdminKey:             strings.Repeat("a", 32),
		KeyPepper:            strings.Repeat("b", 32),
		MasterKey:            strings.Repeat("c", 32),
		DefaultMaxRequests:   60,
		DefaultWindowSeconds: 60,
	}
}

func TestValidate_RejectsZeroDefaults(t *testing.T) {
	c := validConfig()
	c.DefaultMaxRequests = 0
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "PORTCULLIS_DEFAULT_MAX_REQUESTS") {
		t.Errorf("expected error for zero max_requests, got %v", c.DefaultMaxRequests)
	}

	c = validConfig()
	c.DefaultWindowSeconds = -1
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "PORTCULLIS_DEFAULT_WINDOW_SECONDS") {
		t.Errorf("expected error for negative window_seconds, got %v", err)
	}
}
