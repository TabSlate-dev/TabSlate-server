package db

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// Open connects to Turso (remote) or a local SQLite file (dev).
// DSN examples:
//
//	libsql://your-db.turso.io?authToken=your-token   (production)
//	file:./dev.db                                     (development)
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("libsql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

// Migrate runs the schema.sql file against the database.
// Idempotent — all statements use CREATE TABLE IF NOT EXISTS.
func Migrate(db *sql.DB, schemaPath string) error {
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
