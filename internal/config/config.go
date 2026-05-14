package config

import (
	"errors"
	"fmt"
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
	if len(c.MasterKey) < 32 {
		problems = append(problems,
			"PORTCULLIS_MASTER_KEY is required and must be at least 32 chars (use: openssl rand -base64 32)")
	}
	if c.DefaultMaxRequests <= 0 {
		problems = append(problems,
			"PORTCULLIS_DEFAULT_MAX_REQUESTS must be a positive integer")
	}
	if c.DefaultWindowSeconds <= 0 {
		problems = append(problems,
			"PORTCULLIS_DEFAULT_WINDOW_SECONDS must be a positive integer")
	}

	if len(problems) > 0 {
		return errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil

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
