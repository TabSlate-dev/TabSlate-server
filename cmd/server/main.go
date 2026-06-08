package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

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

	// ── Process lifetime context ──────────────────────────────────────────────
	// Cancelled on SIGINT / SIGTERM so background goroutines (e.g. cleanup
	// tasks) stop cleanly before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Billing provider ──────────────────────────────────────────────────────
	// OSS edition: unlimited users, all features unlocked. Registration gating
	// is controlled by ALLOW_REGISTRATION env var (handled in AuthHandler).
	bp := local.New(database)

	// ── HTTP server ───────────────────────────────────────────────────────────
	// Captcha verifier and mailer are created inside app.New() from Config.
	srv := app.New(cfg, database, bp, ctx)
	srv.Run()
}
