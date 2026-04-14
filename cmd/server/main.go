package main

import (
	"log"

	"github.com/joho/godotenv"
	"github.com/tabslate/server/app"
	"github.com/tabslate/server/billing/local"
	"github.com/tabslate/server/db"
)

func main() {
	// Load .env when present (ignored in production where real env vars are set)
	_ = godotenv.Load()

	cfg := app.LoadConfig()

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("database ready")

	// ── Billing provider ──────────────────────────────────────────────────────
	// OSS edition: quota is derived from the installed License JWT (or free
	// defaults when no LICENSE_KEY is set). No external calls are made.
	bp, err := local.New(cfg.LicenseKey, nil /* no public key in dev */)
	if err != nil {
		log.Fatalf("billing provider: %v", err)
	}

	// ── HTTP server ───────────────────────────────────────────────────────────
	// Captcha verifier and mailer are created inside app.New() from Config.
	srv := app.New(cfg, database, bp)
	srv.Run()
}
