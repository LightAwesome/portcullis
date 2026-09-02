package config

import (
	"errors"
	"fmt"
	"github.com/LightAwesome/portcullis/internal/crypto"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
)

type Config struct {
	Env      Environment
	Addr     string
	LogLevel string

	DatabaseURL string
	RedisURL    string

	AdminKey  string
	KeyPepper string
	MasterKey string

	DefaultMaxRequests   int
	DefaultWindowSeconds int

	DashboardToken string
	AllowedOrigins []string
}

func Load() (*Config, error) {

	_ = godotenv.Load()

	cfg := &Config{
		Env:                  Environment(getEnvOr("PORTCULLIS_ENV", "development")),
		Addr:                 getEnvOr("PORTCULLIS_ADDR", ":8080"),
		LogLevel:             getEnvOr("PORTCULLIS_LOG_LEVEL", "info"),
		DatabaseURL:          os.Getenv("PORTCULLIS_DATABASE_URL"),
		RedisURL:             os.Getenv("PORTCULLIS_REDIS_URL"),
		AdminKey:             os.Getenv("PORTCULLIS_ADMIN_KEY"),
		KeyPepper:            os.Getenv("PORTCULLIS_KEY_PEPPER"),
		MasterKey:            os.Getenv("PORTCULLIS_MASTER_KEY"),
		DefaultMaxRequests:   getEnvIntOr("PORTCULLIS_DEFAULT_MAX_REQUESTS", 60),
		DefaultWindowSeconds: getEnvIntOr("PORTCULLIS_DEFAULT_WINDOW_SECONDS", 60),
		DashboardToken:       os.Getenv("PORTCULLIS_DASHBOARD_TOKEN"),
		AllowedOrigins:       splitCSV(os.Getenv("PORTCULLIS_ALLOWED_ORIGINS")),
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return cfg, nil

}

func (c *Config) validate() error {
	var problems []string

	if c.Env != EnvDevelopment && c.Env != EnvProduction {
		problems = append(problems,
			fmt.Sprintf("PORTCULLIS_ENV must be 'development' or 'production', got %q", c.Env))
	}

	if c.DatabaseURL == "" {
		problems = append(problems, "PORTCULLIS_DATABASE_URL is required")
	}
	if c.RedisURL == "" {
		problems = append(problems, "PORTCULLIS_REDIS_URL is required")
	}

	// Secrets: required, and minimum-length-checked to catch placeholder values.
	if len(c.AdminKey) < 32 {
		problems = append(problems,
			"PORTCULLIS_ADMIN_KEY is required and must be at least 32 chars (use: openssl rand -hex 32)")
	}
	if len(c.KeyPepper) < 32 {
		problems = append(problems,
			"PORTCULLIS_KEY_PEPPER is required and must be at least 32 chars (use: openssl rand -hex 32)")
	}
	if c.MasterKey == "" {
		problems = append(problems,
			"PORTCULLIS_MASTER_KEY is required (generate with: openssl rand -base64 32)")
	} else if _, err := crypto.ParseMasterKey(c.MasterKey); err != nil {
		problems = append(problems,
			fmt.Sprintf("PORTCULLIS_MASTER_KEY invalid: %v (expected base64 of 32 random bytes; generate with: openssl rand -base64 32)", err))
	}
	if c.DefaultMaxRequests <= 0 {

		problems = append(problems,
			"PORTCULLIS_DEFAULT_MAX_REQUESTS must be a positive integer")
	}
	if c.DefaultWindowSeconds <= 0 {
		problems = append(problems,
			"PORTCULLIS_DEFAULT_WINDOW_SECONDS must be a positive integer")
	}

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems,
			fmt.Sprintf("PORTCULLIS_LOG_LEVEL must be debug/info/warn/error, got %q", c.LogLevel))
	}

	if len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil

}

// SlogLevel returns the parsed log level. Falls back to Info if the
// string is unrecognised — validate() should have caught that, but
// defensive default is cheap.
func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// IsProduction returns true when running in the production environment.
// Equivalent to c.Env == EnvProduction; provided as a method for readability.
func (c *Config) IsProduction() bool {
	return c.Env == EnvProduction
}

// getEnvOr returns the value of the environment variable named key,
// or fallback if the variable is unset or empty.
func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// MasterKeyBytes returns the raw 32-byte AES-256 key decoded from
// PORTCULLIS_MASTER_KEY. Panics if called before validate() has
// confirmed the key is parseable; Load guarantees this.
//
// The returned slice is freshly allocated on each call — callers can
// retain it without worrying about aliasing.
func (c *Config) MasterKeyBytes() []byte {
	key, err := crypto.ParseMasterKey(c.MasterKey)
	if err != nil {
		// Unreachable: validate() runs ParseMasterKey at startup and
		// refuses to construct a Config that wouldn't parse. If this
		// panics, validate is broken.
		panic(fmt.Sprintf("config: MasterKeyBytes after validate: %v", err))
	}
	return key
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
