package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/tabslate/server/db"
)

// incrementSeq atomically increments the per-user sync sequence counter inside
// an existing transaction and returns the new seq value.
// Creates the row on first use so users registered before the sync feature work correctly.
func incrementSeq(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx,
		`INSERT INTO user_sync_seq (user_id, seq) VALUES ($1, 1)
		 ON CONFLICT (user_id) DO UPDATE SET seq = user_sync_seq.seq + 1
		 RETURNING seq`,
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
