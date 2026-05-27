package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/pubsub"
	"github.com/tabslate/server/internal/search"
)

type SyncHandler struct {
	db      *db.DB
	search  *search.Client
	hub     pubsub.Hub
	billing billing.Provider
}

func NewSyncHandler(d *db.DB, sc *search.Client, hub pubsub.Hub, bp billing.Provider) *SyncHandler {
	return &SyncHandler{db: d, search: sc, hub: hub, billing: bp}
}

// POST /sync/push
// Accepts client changes, applies LWW upserts, stamps with server seq.
func (h *SyncHandler) Push(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var req model.SyncPushRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Enforce entity count limit.
	total := len(req.Entities.Workspaces) + len(req.Entities.Collections) +
		len(req.Entities.Bookmarks) + len(req.Entities.Tags) + len(req.Entities.Groups)
	if total > 1000 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "too many entities in one push (max 1000)"})
		return
	}

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tx begin failed"})
		return
	}
	defer tx.Rollback(ctx)

	seq, err := incrementSeq(ctx, tx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seq increment failed"})
		return
	}

	now := time.Now().UnixMilli()
	var rejected []model.Rejected

	// ── Pre-fetch quota baselines ─────────────────────────────────────────────
	// One query per quota-limited entity type, regardless of push size.
	// Replaces the previous O(n) per-entity COUNT(*) pattern.

	activeWSIDs := make(map[string]struct{})
	wsQuotaCount := 0
	if limits.MaxWorkspaces != -1 && len(req.Entities.Workspaces) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id FROM workspaces WHERE user_id = $1 AND deleted_at IS NULL`, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
				return
			}
			activeWSIDs[id] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		wsQuotaCount = len(activeWSIDs)
	}

	activeColIDs := make(map[string]struct{})
	colQuotaCount := 0
	if limits.MaxCollections != -1 && len(req.Entities.Collections) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id FROM collections WHERE user_id = $1 AND is_deleted < 2`, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
				return
			}
			activeColIDs[id] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		colQuotaCount = len(activeColIDs)
	}

	activeGroupIDs := make(map[string]struct{})
	groupQuotaCount := 0
	if limits.MaxSavedGroups != -1 && len(req.Entities.Groups) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id FROM groups WHERE user_id = $1 AND deleted_at IS NULL`, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
				return
			}
			activeGroupIDs[id] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
			return
		}
		groupQuotaCount = len(activeGroupIDs)
	}

	// ── Workspaces ────────────────────────────────────────────────────────────
	var wsUpserts []model.Workspace
	for _, ws := range req.Entities.Workspaces {
		if ws.DeletedAt == nil && limits.MaxWorkspaces != -1 {
			if _, exists := activeWSIDs[ws.ID]; !exists {
				if wsQuotaCount >= limits.MaxWorkspaces {
					rejected = append(rejected, model.Rejected{ID: ws.ID, Reason: "quota_exceeded", Type: "workspace"})
					continue
				}
				wsQuotaCount++
			}
		}
		wsUpserts = append(wsUpserts, ws)
	}
	if len(wsUpserts) > 0 {
		batch := &pgx.Batch{}
		for _, ws := range wsUpserts {
			batch.Queue(`
				INSERT INTO workspaces (id, user_id, name, icon, color, position, seq, deleted_at, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
				ON CONFLICT (id) DO UPDATE
				  SET name=$3, icon=$4, color=$5, position=$6, seq=$7, deleted_at=$8, updated_at=$9
				WHERE workspaces.user_id = $2 AND workspaces.updated_at < $9`,
				ws.ID, userID, ws.Name, ws.Icon, ws.Color, ws.Position, seq, ws.DeletedAt, now)
		}
		br := tx.SendBatch(ctx, batch)
		for _, ws := range wsUpserts {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace upsert failed"})
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, model.Rejected{ID: ws.ID, Reason: "stale"})
			}
		}
		br.Close()
	}

	// ── Collections ───────────────────────────────────────────────────────────
	var colUpserts []model.Collection
	for _, col := range req.Entities.Collections {
		if col.DeletedAt == nil && limits.MaxCollections != -1 {
			if _, exists := activeColIDs[col.ID]; !exists {
				if colQuotaCount >= limits.MaxCollections {
					rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "quota_exceeded", Type: "collection"})
					continue
				}
				colQuotaCount++
			}
		}
		colUpserts = append(colUpserts, col)
	}
	if len(colUpserts) > 0 {
		batch := &pgx.Batch{}
		for _, col := range colUpserts {
			batch.Queue(`
				INSERT INTO collections (id, user_id, workspace_id, name, icon, position, seq, deleted_at, archived_at, is_deleted, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
				ON CONFLICT (id) DO UPDATE
				  SET workspace_id=$3, name=$4, icon=$5, position=$6, seq=$7, deleted_at=$8, archived_at=$9, is_deleted=$10, updated_at=$11
				WHERE collections.user_id = $2 AND collections.updated_at < $11`,
				col.ID, userID, col.WorkspaceID, col.Name, col.Icon, col.Position, seq, col.DeletedAt, col.ArchivedAt, col.IsDeleted, now)
		}
		br := tx.SendBatch(ctx, batch)
		var cascadeIDs []string
		for _, col := range colUpserts {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "collection upsert failed"})
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "stale"})
			} else if col.IsDeleted == 2 {
				// Cascade permanent deletion to any remaining bookmarks in this collection.
				// The client pushes individual is_trashed:2 tombstones, but if that push
				// was skipped (e.g. empty local IDB on a fresh session), bookmarks would
				// stay at is_trashed=1 forever. This ensures the server is the final authority.
				cascadeIDs = append(cascadeIDs, col.ID)
			}
		}
		br.Close()
		if len(cascadeIDs) > 0 {
			cb := &pgx.Batch{}
			for _, colID := range cascadeIDs {
				cb.Queue(`UPDATE bookmarks SET is_trashed = 2, deleted_at = $1, seq = $2, updated_at = $1
					 WHERE user_id = $3 AND collection_id = $4 AND is_trashed < 2`,
					now, seq, userID, colID)
			}
			cbr := tx.SendBatch(ctx, cb)
			for range cascadeIDs {
				if _, err := cbr.Exec(); err != nil {
					cbr.Close()
					c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmark cascade failed"})
					return
				}
			}
			cbr.Close()
		}
	}

	var searchUpserts []search.BookmarkDoc
	var searchDeletes []string

	// ── Bookmarks ─────────────────────────────────────────────────────────────
	if len(req.Entities.Bookmarks) > 0 {
		batch := &pgx.Batch{}
		for _, bm := range req.Entities.Bookmarks {
			tagIDs := bm.TagIDs
			if tagIDs == nil {
				tagIDs = []string{}
			}
			batch.Queue(`
				INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url, description,
				                       is_favorite, is_archived, is_trashed, position, seq, deleted_at, created_at, updated_at, tag_ids)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,$15)
				ON CONFLICT (id) DO UPDATE
				  SET collection_id=$3, title=$4, url=$5, favicon_url=$6, description=$7,
				      is_favorite=$8, is_archived=$9, is_trashed=$10, position=$11, seq=$12, deleted_at=$13, updated_at=$14, tag_ids=$15
				WHERE bookmarks.user_id = $2 AND bookmarks.updated_at < $14`,
				bm.ID, userID, bm.CollectionID, bm.Title, bm.URL, bm.FaviconURL, bm.Description,
				bm.IsFavorite, bm.IsArchived, bm.IsTrashed, bm.Position, seq, bm.DeletedAt, now, tagIDs)
		}
		br := tx.SendBatch(ctx, batch)
		for _, bm := range req.Entities.Bookmarks {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmark upsert failed"})
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, model.Rejected{ID: bm.ID, Reason: "stale"})
			} else {
				if bm.DeletedAt != nil || bm.IsTrashed > 0 {
					searchDeletes = append(searchDeletes, bm.ID)
				} else {
					searchUpserts = append(searchUpserts, search.BookmarkDoc{
						ID:           bm.ID,
						UserID:       userID,
						Title:        bm.Title,
						URL:          bm.URL,
						Description:  bm.Description,
						CollectionID: derefStr(bm.CollectionID),
						IsArchived:   bm.IsArchived,
					})
				}
			}
		}
		br.Close()
	}

	// ── Tags ──────────────────────────────────────────────────────────────────
	if len(req.Entities.Tags) > 0 {
		batch := &pgx.Batch{}
		for _, t := range req.Entities.Tags {
			batch.Queue(`
				INSERT INTO tags (id, user_id, name, color, seq, deleted_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (id) DO UPDATE
				  SET name=$3, color=$4, seq=$5, deleted_at=$6, updated_at=$7
				WHERE tags.user_id = $2 AND tags.updated_at < $7`,
				t.ID, userID, t.Name, t.Color, seq, t.DeletedAt, now)
		}
		br := tx.SendBatch(ctx, batch)
		for _, t := range req.Entities.Tags {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "tag upsert failed"})
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, model.Rejected{ID: t.ID, Reason: "stale"})
			}
		}
		br.Close()
	}

	// ── Groups ────────────────────────────────────────────────────────────────
	var groupUpserts []model.Group
	for _, g := range req.Entities.Groups {
		if g.DeletedAt == nil && limits.MaxSavedGroups != -1 {
			if _, exists := activeGroupIDs[g.ID]; !exists {
				if groupQuotaCount >= limits.MaxSavedGroups {
					rejected = append(rejected, model.Rejected{ID: g.ID, Reason: "quota_exceeded", Type: "saved_group"})
					continue
				}
				groupQuotaCount++
			}
		}
		groupUpserts = append(groupUpserts, g)
	}
	if len(groupUpserts) > 0 {
		batch := &pgx.Batch{}
		for _, g := range groupUpserts {
			batch.Queue(`
				INSERT INTO groups (id, user_id, name, color, is_compact, seq, deleted_at, is_deleted, created_at, updated_at, workspace_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10)
				ON CONFLICT (id) DO UPDATE
				  SET name=$3, color=$4, is_compact=$5, seq=$6, deleted_at=$7, is_deleted=$8, updated_at=$9, workspace_id=$10
				WHERE groups.user_id = $2 AND groups.updated_at < $9`,
				g.ID, userID, g.Name, g.Color, g.IsCompact, seq, g.DeletedAt, g.IsDeleted, now, g.WorkspaceID)
		}
		br := tx.SendBatch(ctx, batch)
		var acceptedGroups []model.Group
		for _, g := range groupUpserts {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "group upsert failed"})
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, model.Rejected{ID: g.ID, Reason: "stale"})
			} else {
				acceptedGroups = append(acceptedGroups, g)
			}
		}
		br.Close()
		// Atomically replace tab snapshots for all accepted groups in one batch.
		if len(acceptedGroups) > 0 {
			tabBatch := &pgx.Batch{}
			for _, g := range acceptedGroups {
				tabBatch.Queue(`DELETE FROM group_tabs WHERE group_id = $1`, g.ID)
				for _, t := range g.Tabs {
					tabBatch.Queue(
						`INSERT INTO group_tabs (id, group_id, title, url, favicon, position) VALUES ($1,$2,$3,$4,$5,$6)`,
						t.ID, g.ID, t.Title, t.URL, t.Favicon, t.Position)
				}
			}
			tbr := tx.SendBatch(ctx, tabBatch)
			for _, g := range acceptedGroups {
				if _, err := tbr.Exec(); err != nil { // DELETE
					tbr.Close()
					c.JSON(http.StatusInternalServerError, gin.H{"error": "group_tabs clear failed"})
					return
				}
				for range g.Tabs {
					if _, err := tbr.Exec(); err != nil { // INSERT tab
						tbr.Close()
						c.JSON(http.StatusInternalServerError, gin.H{"error": "group_tab insert failed"})
						return
					}
				}
			}
			tbr.Close()
		}
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit failed"})
		return
	}

	h.hub.Broadcast(userID, seq)

	h.search.BulkUpsertAsync(searchUpserts)
	h.search.BulkDeleteAsync(searchDeletes)

	if rejected == nil {
		rejected = []model.Rejected{}
	}
	c.JSON(http.StatusOK, model.SyncPushResponse{ServerSeq: seq, Rejected: rejected})
}

// GET /sync/pull?after_seq=<N>
// Returns all entities (including soft-deleted) with seq > N for the caller.
func (h *SyncHandler) Pull(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var afterSeq int64
	if s := c.Query("after_seq"); s != "" {
		if _, err := fmt.Sscanf(s, "%d", &afterSeq); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid after_seq"})
			return
		}
		if afterSeq < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "after_seq must be non-negative"})
			return
		}
	}

	var resp model.SyncPullResponse

	// Workspaces
	wsRows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, icon, color, position, seq, deleted_at, created_at, updated_at
         FROM workspaces WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC`,
		userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspaces query failed"})
		return
	}
	defer wsRows.Close()
	for wsRows.Next() {
		var ws model.Workspace
		if err := wsRows.Scan(&ws.ID, &ws.UserID, &ws.Name, &ws.Icon, &ws.Color, &ws.Position,
			&ws.Seq, &ws.DeletedAt, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "workspace scan failed"})
			return
		}
		resp.Entities.Workspaces = append(resp.Entities.Workspaces, ws)
	}
	if err := wsRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspaces iteration failed"})
		return
	}

	// Collections — is_default is computed via CTE: among active (non-deleted,
	// non-archived) collections per workspace, the one with the lowest position
	// is flagged as the default. This is a response-time annotation; no DB column.
	colRows, err := h.db.Query(ctx,
		`WITH min_pos AS (
			SELECT workspace_id, MIN(position) AS min_position
			FROM collections
			WHERE user_id = $1 AND workspace_id IS NOT NULL
			  AND deleted_at IS NULL AND archived_at IS NULL AND is_deleted = 0
			GROUP BY workspace_id
		)
		SELECT c.id, c.user_id, c.workspace_id, c.name, c.icon, c.position,
		       c.seq, c.deleted_at, c.archived_at, c.is_deleted, c.created_at, c.updated_at,
		       (c.deleted_at IS NULL AND c.archived_at IS NULL AND c.is_deleted = 0
		        AND m.min_position IS NOT NULL AND c.position = m.min_position) AS is_default
		FROM collections c
		LEFT JOIN min_pos m ON m.workspace_id = c.workspace_id
		WHERE c.user_id = $2 AND c.seq > $3
		ORDER BY c.seq ASC`,
		userID, userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "collections query failed"})
		return
	}
	defer colRows.Close()
	for colRows.Next() {
		var col model.Collection
		if err := colRows.Scan(&col.ID, &col.UserID, &col.WorkspaceID, &col.Name, &col.Icon, &col.Position,
			&col.Seq, &col.DeletedAt, &col.ArchivedAt, &col.IsDeleted, &col.CreatedAt, &col.UpdatedAt,
			&col.IsDefault); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "collection scan failed"})
			return
		}
		resp.Entities.Collections = append(resp.Entities.Collections, col)
	}
	if err := colRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "collections iteration failed"})
		return
	}

	// Bookmarks
	bmRows, err := h.db.Query(ctx,
		`SELECT id, user_id, collection_id, title, url, favicon_url, description,
                is_favorite, is_archived, is_trashed, tag_ids, position, seq, deleted_at, created_at, updated_at
         FROM bookmarks WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC`,
		userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmarks query failed"})
		return
	}
	defer bmRows.Close()
	for bmRows.Next() {
		var bm model.Bookmark
		if err := bmRows.Scan(&bm.ID, &bm.UserID, &bm.CollectionID, &bm.Title, &bm.URL, &bm.FaviconURL,
			&bm.Description, &bm.IsFavorite, &bm.IsArchived, &bm.IsTrashed, &bm.TagIDs, &bm.Position,
			&bm.Seq, &bm.DeletedAt, &bm.CreatedAt, &bm.UpdatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmark scan failed"})
			return
		}
		if bm.TagIDs == nil {
			bm.TagIDs = []string{}
		}
		resp.Entities.Bookmarks = append(resp.Entities.Bookmarks, bm)
	}
	if err := bmRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bookmarks iteration failed"})
		return
	}

	// Tags — model.Tag has no UpdatedAt field; omit updated_at from SELECT
	tagRows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, color, seq, deleted_at
         FROM tags WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC`,
		userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tags query failed"})
		return
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var t model.Tag
		if err := tagRows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color, &t.Seq, &t.DeletedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tag scan failed"})
			return
		}
		resp.Entities.Tags = append(resp.Entities.Tags, t)
	}
	if err := tagRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tags iteration failed"})
		return
	}

	// Groups
	grpRows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, color, is_compact, seq, deleted_at, is_deleted, created_at, updated_at, workspace_id
         FROM groups WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC`,
		userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "groups query failed"})
		return
	}
	defer grpRows.Close()

	groupIdx := map[string]int{} // id → index in resp.Entities.Groups
	for grpRows.Next() {
		var g model.Group
		if err := grpRows.Scan(&g.ID, &g.UserID, &g.Name, &g.Color, &g.IsCompact,
			&g.Seq, &g.DeletedAt, &g.IsDeleted, &g.CreatedAt, &g.UpdatedAt, &g.WorkspaceID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "group scan failed"})
			return
		}
		g.Tabs = []model.GroupTab{}
		groupIdx[g.ID] = len(resp.Entities.Groups)
		resp.Entities.Groups = append(resp.Entities.Groups, g)
	}
	if err := grpRows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "groups iteration failed"})
		return
	}

	// Fetch tabs for all returned groups in one batch query.
	if len(resp.Entities.Groups) > 0 {
		ids := make([]string, len(resp.Entities.Groups))
		for i, g := range resp.Entities.Groups {
			ids[i] = g.ID
		}
		tabRows, err := h.db.Query(ctx,
			`SELECT id, group_id, title, url, favicon, position
             FROM group_tabs WHERE group_id = ANY($1)
             ORDER BY group_id ASC, position ASC, id ASC`,
			ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "group_tabs query failed"})
			return
		}
		defer tabRows.Close()
		for tabRows.Next() {
			var t model.GroupTab
			if err := tabRows.Scan(&t.ID, &t.GroupID, &t.Title, &t.URL, &t.Favicon, &t.Position); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "group_tab scan failed"})
				return
			}
			if idx, ok := groupIdx[t.GroupID]; ok {
				resp.Entities.Groups[idx].Tabs = append(resp.Entities.Groups[idx].Tabs, t)
			}
		}
		if err := tabRows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "group_tabs iteration failed"})
			return
		}
	}

	if resp.Entities.Groups == nil {
		resp.Entities.Groups = []model.Group{}
	}

	// Ensure slices are not nil in JSON output ([] not null)
	if resp.Entities.Workspaces == nil {
		resp.Entities.Workspaces = []model.Workspace{}
	}
	if resp.Entities.Collections == nil {
		resp.Entities.Collections = []model.Collection{}
	}
	if resp.Entities.Bookmarks == nil {
		resp.Entities.Bookmarks = []model.Bookmark{}
	}
	if resp.Entities.Tags == nil {
		resp.Entities.Tags = []model.Tag{}
	}

	serverSeq, err := currentSeq(ctx, h.db, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "sync failed"})
		return
	}
	resp.ServerSeq = serverSeq

	c.JSON(http.StatusOK, resp)
}
