package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tabslate/server/db"
)

// incrementSeq atomically increments the per-user sync sequence counter inside
// an existing transaction and returns the new seq value.
// The user_sync_seq row must already exist (created during registration).
func incrementSeq(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx,
		`UPDATE user_sync_seq SET seq = seq + 1 WHERE user_id = $1 RETURNING seq`,
		userID,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("incrementSeq: %w", err)
	}
	return seq, nil
}

// currentSeq returns the current seq for a user without incrementing.
func currentSeq(ctx context.Context, d *db.DB, userID string) (int64, error) {
	var seq int64
	err := d.QueryRow(ctx,
		`SELECT seq FROM user_sync_seq WHERE user_id = $1`,
		userID,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("currentSeq: %w", err)
	}
	return seq, nil
}
