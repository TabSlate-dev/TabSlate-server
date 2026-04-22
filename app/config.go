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

	// ── Prosopo Captcha ──────────────────────────────────────────────────────
	// ProsopoSecret is the site secret for server-side verification.
	// Leave empty to disable captcha verification entirely.
	ProsopoSecret string

	// ProsopoServerURL is the verification endpoint. Defaults to the official
	// Prosopo API but can be overridden for self-hosted deployments.
	ProsopoServerURL string

	// ProsopoBundleURL is the URL of the procaptcha JS bundle served to the
	// browser widget iframe. Defaults to the official Prosopo CDN. Override for
	// self-hosted Prosopo deployments.
	ProsopoBundleURL string

	// ── Email ────────────────────────────────────────────────────────────────
	// MailProvider selects the email backend: "smtp" or "resend".
	// Leave empty to disable email verification (all users auto-verified).
	MailProvider string

	// SMTP settings (used when MailProvider == "smtp")
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string

	// Resend settings (used when MailProvider == "resend")
	ResendAPIKey string
	ResendFrom   string

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

		// Prosopo
		ProsopoSecret:    os.Getenv("PROSOPO_SECRET"),
		ProsopoServerURL: envOr("PROSOPO_SERVER_URL", "https://api.prosopo.io/siteverify"),
		ProsopoBundleURL: envOr("PROSOPO_BUNDLE_URL", "https://js.prosopo.io/js/procaptcha.bundle.js"),

		// Email
		MailProvider:  os.Getenv("MAIL_PROVIDER"),
		SMTPHost:      os.Getenv("SMTP_HOST"),
		SMTPPort:      envOr("SMTP_PORT", "587"),
		SMTPUser:      os.Getenv("SMTP_USER"),
		SMTPPassword:  os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:      os.Getenv("SMTP_FROM"),
		ResendAPIKey:  os.Getenv("RESEND_API_KEY"),
		ResendFrom:    os.Getenv("RESEND_FROM"),
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
