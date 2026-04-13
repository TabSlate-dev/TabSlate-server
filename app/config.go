package app

import (
	"log"
	"os"
)

// Config holds all runtime configuration read from environment variables.
type Config struct {
	// DatabaseURL is the DSN for the primary database.
	// Use a postgres:// URL for PostgreSQL or a libsql:// / file: URL for Turso/SQLite.
	DatabaseURL string

	// JWTSecret is the HMAC secret used to sign access tokens.
	JWTSecret string

	// Port is the TCP port the HTTP server listens on (default "8080").
	Port string

	// GinMode sets the Gin framework mode ("debug", "release", "test").
	GinMode string

	// LicenseKey is the optional OSS License JWT. Leave empty for free-tier mode.
	LicenseKey string
}

// LoadConfig reads configuration from environment variables and fatals on any
// required variable that is missing.
func LoadConfig() *Config {
	return &Config{
		DatabaseURL: mustEnv("DATABASE_URL"),
		JWTSecret:   mustEnv("JWT_SECRET"),
		Port:        envOr("PORT", "8080"),
		GinMode:     os.Getenv("GIN_MODE"),
		LicenseKey:  os.Getenv("LICENSE_KEY"),
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
