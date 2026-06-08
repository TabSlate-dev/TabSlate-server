package handler

import (
	"context"
	"log"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/db"
)

// tombstoneWindowDays is the maximum expected delta-sync lag across devices.
// It is intentionally fixed (not operator-configurable) — changing it is a protocol decision,
// not an operational one. Operators should adjust TRASH_GRACE_DAYS instead.
const tombstoneWindowDays = 7

// CleanupHandler runs a background goroutine that:
//   - Phase 1: auto-expires state=1 items to state=2 after the grace period,
//     bumping seq so tombstones appear in delta pulls.
//   - Phase 2: hard-deletes state=2 items after the tombstone window.
type CleanupHandler struct {
	db             *db.DB
	trashGraceDays int
}

func NewCleanupHandler(d *db.DB, trashGraceDays int) *CleanupHandler {
	return &CleanupHandler{db: d, trashGraceDays: trashGraceDays}
}

// Run starts the cleanup loop. Call as a goroutine; exits when ctx is cancelled.
func (h *CleanupHandler) Run(ctx context.Context) {
	h.runOnce(ctx) // run immediately on start
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runOnce(ctx)
		}
	}
}

func (h *CleanupHandler) runOnce(ctx context.Context) {
	nowMs := time.Now().UnixMilli()
	graceMs := int64(h.trashGraceDays) * 24 * 60 * 60 * 1000
	tombstoneMs := int64(tombstoneWindowDays) * 24 * 60 * 60 * 1000

	h.phase1(ctx, nowMs, graceMs)
	h.phase2(ctx, nowMs, graceMs, tombstoneMs)
}

// phase1 promotes state=1 items past the grace period to state=2.
// Each affected user gets a seq bump so the tombstones appear in delta pulls.
func (h *CleanupHandler) phase1(ctx context.Context, nowMs, graceMs int64) {
	threshold := nowMs - graceMs

	rows, err := h.db.Query(ctx,
		`SELECT DISTINCT user_id FROM bookmarks   WHERE is_trashed = 1 AND deleted_at < $1
		 UNION
		 SELECT DISTINCT user_id FROM collections WHERE is_deleted = 1  AND deleted_at < $1
		 UNION
		 SELECT DISTINCT user_id FROM groups      WHERE is_deleted = 1  AND deleted_at < $1`,
		threshold)
	if err != nil {
		log.Printf("cleanup phase1 users query: %v", err)
		return
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			log.Printf("cleanup phase1 users scan: %v", err)
			continue
		}
		userIDs = append(userIDs, uid)
	}
	if err := rows.Err(); err != nil {
		log.Printf("cleanup phase1 rows err: %v", err)
		return
	}

	for _, uid := range userIDs {
		h.phase1ForUser(ctx, uid, threshold)
	}
}

func (h *CleanupHandler) phase1ForUser(ctx context.Context, userID string, threshold int64) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		log.Printf("cleanup phase1 begin tx for %s: %v", userID, err)
		return
	}
	defer tx.Rollback(ctx)

	newSeq, err := incrementSeq(ctx, tx, userID)
	if err != nil {
		log.Printf("cleanup phase1 incrementSeq for %s: %v", userID, err)
		return
	}

	if _, err := tx.Exec(ctx,
		`UPDATE bookmarks SET is_trashed = 2, seq = $1
		 WHERE user_id = $2 AND is_trashed = 1 AND deleted_at < $3`,
		newSeq, userID, threshold); err != nil {
		log.Printf("cleanup phase1 bookmarks for %s: %v", userID, err)
		return
	}
	if _, err := tx.Exec(ctx,
		`UPDATE collections SET is_deleted = 2, seq = $1
		 WHERE user_id = $2 AND is_deleted = 1 AND deleted_at < $3`,
		newSeq, userID, threshold); err != nil {
		log.Printf("cleanup phase1 collections for %s: %v", userID, err)
		return
	}
	if _, err := tx.Exec(ctx,
		`UPDATE groups SET is_deleted = 2, seq = $1
		 WHERE user_id = $2 AND is_deleted = 1 AND deleted_at < $3`,
		newSeq, userID, threshold); err != nil {
		log.Printf("cleanup phase1 groups for %s: %v", userID, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("cleanup phase1 commit for %s: %v", userID, err)
	}
}

// phase2 hard-deletes state=2 items past the tombstone window.
// Deletion order: bookmarks first (FK collection_id ON DELETE SET NULL),
// then collections, then groups (group_tabs cascade automatically via FK).
func (h *CleanupHandler) phase2(ctx context.Context, nowMs, graceMs, tombstoneMs int64) {
	cutoff := nowMs - graceMs - tombstoneMs

	if _, err := h.db.Exec(ctx,
		`DELETE FROM bookmarks WHERE is_trashed = 2 AND deleted_at < $1`, cutoff); err != nil {
		log.Printf("cleanup phase2 bookmarks: %v", err)
		return
	}
	if _, err := h.db.Exec(ctx,
		`DELETE FROM collections WHERE is_deleted = 2 AND deleted_at < $1`, cutoff); err != nil {
		log.Printf("cleanup phase2 collections: %v", err)
		return
	}
	if _, err := h.db.Exec(ctx,
		`DELETE FROM groups WHERE is_deleted = 2 AND deleted_at < $1`, cutoff); err != nil {
		log.Printf("cleanup phase2 groups: %v", err)
	}
}
