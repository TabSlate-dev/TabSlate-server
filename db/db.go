// Package db provides a pgxpool-based database wrapper for PostgreSQL 17+.
package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.pg.sql
var schema string

// DB wraps pgxpool.Pool. Embedding makes all pool methods available directly.
type DB struct {
	*pgxpool.Pool
}

// Open connects to a PostgreSQL database and verifies connectivity.
// dsn must start with postgres:// or postgresql://.
func Open(dsn string) (*DB, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil, fmt.Errorf("open db: DATABASE_URL must start with postgres:// or postgresql://")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Migrate runs the embedded schema against the database (idempotent).
func Migrate(d *DB) error {
	if _, err := d.Exec(context.Background(), schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
