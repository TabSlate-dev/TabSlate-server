package app

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
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

	// AllowRegistration controls whether new user registration is open.
	// Set ALLOW_REGISTRATION=false to close registration after initial setup.
	// Defaults to true.
	AllowRegistration bool

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

	// SES
	SESAccessKeyID string
	SESSecretKey   string
	SESRegion      string
	SESFrom        string

	// ── Registration captcha ─────────────────────────────────────────────────
	// RegisterCaptchaThreshold is the number of successful registrations from a
	// single IP within RegisterCaptchaWindow before captcha is required.
	// Set to 0 to always require captcha. Defaults to 3.
	RegisterCaptchaThreshold int

	// RegisterCaptchaWindow is the look-back period for per-IP registration
	// counting. Accepts any Go duration string (e.g. "24h", "1h"). Defaults to 24h.
	RegisterCaptchaWindow time.Duration

	// OTPCaptchaThreshold is the number of OTP email requests from a single IP
	// within OTPCaptchaWindow before captcha is required on resend/forgot-password.
	// Set to 0 to always require captcha. Defaults to 5.
	OTPCaptchaThreshold int

	// OTPCaptchaWindow is the look-back period for per-IP OTP request counting.
	// Accepts any Go duration string. Defaults to 15m.
	OTPCaptchaWindow time.Duration

	// ── MeiliSearch ──────────────────────────────────────────────────────────────
	// MeiliSearchHost is the internal URL of the MeiliSearch instance,
	// e.g. "http://meilisearch:7700". Leave empty to disable search indexing.
	MeiliSearchHost string

	// MeiliSearchAPIKey is the master or admin API key for MeiliSearch.
	MeiliSearchAPIKey string

	// ── Rate limiters ────────────────────────────────────────────────────────────
	// RateLimitAuth is the maximum number of requests per RateLimitAuthWindow
	// allowed per IP on auth endpoints (register, login, resend, verify, OTP).
	// Defaults to 10.
	RateLimitAuth       int
	RateLimitAuthWindow time.Duration

	// RateLimitSyncPush / RateLimitSyncPull control per-IP request budgets for
	// the sync endpoints. Defaults: 60 push / 120 pull per minute.
	RateLimitSyncPush       int
	RateLimitSyncPushWindow time.Duration
	RateLimitSyncPull       int
	RateLimitSyncPullWindow time.Duration

	// RateLimitSearch is the per-IP budget for GET /search. Defaults to 60/min.
	RateLimitSearch       int
	RateLimitSearchWindow time.Duration

	// RedisURL is the optional Redis connection URL (e.g. "redis://localhost:6379").
	// Leave empty to use in-memory implementations for all infra providers.
	RedisURL string

	// TrustedProxies is the list of trusted reverse-proxy IPs or CIDRs used by
	// Gin's ClientIP resolution. Defaults to RFC1918 private ranges, which covers
	// Docker + Traefik deployments. Set TRUSTED_PROXIES= (empty) to trust only
	// RemoteAddr (use when directly internet-exposed with no proxy).
	TrustedProxies []string

	// TrashGraceDays is the number of days before a soft-deleted item is
	// automatically promoted to permanently deleted (state=2). The cleanup
	// goroutine uses this value. Defaults to 7.
	TrashGraceDays int
}

// LoadConfig reads configuration from environment variables and fatals on any
// required variable that is missing.
func LoadConfig() *Config {
	return &Config{
		DatabaseURL: mustEnv("DATABASE_URL"),
		JWTSecret:   mustEnv("JWT_SECRET"),
		Port:        envOr("PORT", "8080"),
		GinMode:          os.Getenv("GIN_MODE"),
		AllowRegistration: os.Getenv("ALLOW_REGISTRATION") != "false",

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

		SESAccessKeyID: os.Getenv("SES_ACCESS_KEY_ID"),
		SESSecretKey:   os.Getenv("SES_SECRET_KEY"),
		SESRegion:      os.Getenv("SES_REGION"),
		SESFrom:        os.Getenv("SES_FROM"),

		// Registration captcha
		RegisterCaptchaThreshold: envInt("REGISTER_CAPTCHA_THRESHOLD", 3),
		RegisterCaptchaWindow:    envDuration("REGISTER_CAPTCHA_WINDOW", 24*time.Hour),

		// OTP resend captcha
		OTPCaptchaThreshold: envInt("OTP_CAPTCHA_THRESHOLD", 5),
		OTPCaptchaWindow:    envDuration("OTP_CAPTCHA_WINDOW", 15*time.Minute),

		MeiliSearchHost:   os.Getenv("MEILISEARCH_HOST"),
		MeiliSearchAPIKey: os.Getenv("MEILISEARCH_API_KEY"),

		// Rate limiters
		RateLimitAuth:           envInt("RATE_LIMIT_AUTH", 10),
		RateLimitAuthWindow:     envDuration("RATE_LIMIT_AUTH_WINDOW", 1*time.Minute),
		RateLimitSyncPush:       envInt("RATE_LIMIT_SYNC_PUSH", 60),
		RateLimitSyncPushWindow: envDuration("RATE_LIMIT_SYNC_PUSH_WINDOW", 1*time.Minute),
		RateLimitSyncPull:       envInt("RATE_LIMIT_SYNC_PULL", 120),
		RateLimitSyncPullWindow: envDuration("RATE_LIMIT_SYNC_PULL_WINDOW", 1*time.Minute),
		RateLimitSearch:         envInt("RATE_LIMIT_SEARCH", 60),
		RateLimitSearchWindow:   envDuration("RATE_LIMIT_SEARCH_WINDOW", 1*time.Minute),

		RedisURL: os.Getenv("REDIS_URL"),

		TrustedProxies: envStringSlice("TRUSTED_PROXIES", []string{
			"172.16.0.0/12",
			"10.0.0.0/8",
			"192.168.0.0/16",
		}),

		TrashGraceDays: envInt("TRASH_GRACE_DAYS", 7),
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

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("invalid integer for %s, using default %d", key, fallback)
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("invalid duration for %s, using default %s", key, fallback)
	}
	return fallback
}

// envStringSlice reads a comma-separated list of strings from the environment.
// If the variable is not set, defaultVal is returned.
// If the variable is set to an empty string, nil is returned (trust only RemoteAddr).
func envStringSlice(key string, defaultVal []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
