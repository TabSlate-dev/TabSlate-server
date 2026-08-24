package handler

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

type SyncHandler struct {
	db        *db.DB
	search    *search.Client
	hub       pubsub.Hub
	billing   billing.Provider
	lifecycle *WorkspaceLifecycleService
}

func NewSyncHandler(
	d *db.DB,
	sc *search.Client,
	hub pubsub.Hub,
	bp billing.Provider,
	lifecycle ...*WorkspaceLifecycleService,
) *SyncHandler {
	lifecycleService := firstWorkspaceLifecycleService(lifecycle)
	if lifecycleService == nil {
		lifecycleService = NewWorkspaceLifecycleService(d, hub, sc)
	}
	return &SyncHandler{
		db:        d,
		search:    sc,
		hub:       hub,
		billing:   bp,
		lifecycle: lifecycleService,
	}
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
	workspaceIDs := make([]string, 0, len(req.Entities.Workspaces))
	collectionIDs := make([]string, 0, len(req.Entities.Collections))
	bookmarkIDs := make([]string, 0, len(req.Entities.Bookmarks))
	tagIDs := make([]string, 0, len(req.Entities.Tags))
	groupIDs := make([]string, 0, len(req.Entities.Groups))
	entityIDs := make([]string, 0, total)
	for _, workspace := range req.Entities.Workspaces {
		workspaceIDs = append(workspaceIDs, workspace.ID)
	}
	for _, collection := range req.Entities.Collections {
		collectionIDs = append(collectionIDs, collection.ID)
	}
	for _, bookmark := range req.Entities.Bookmarks {
		bookmarkIDs = append(bookmarkIDs, bookmark.ID)
	}
	for _, tag := range req.Entities.Tags {
		tagIDs = append(tagIDs, tag.ID)
	}
	for _, group := range req.Entities.Groups {
		groupIDs = append(groupIDs, group.ID)
	}
	entityIDs = append(entityIDs, workspaceIDs...)
	entityIDs = append(entityIDs, collectionIDs...)
	entityIDs = append(entityIDs, bookmarkIDs...)
	entityIDs = append(entityIDs, tagIDs...)
	entityIDs = append(entityIDs, groupIDs...)
	workspaceChildIDs := make([]string, 0, len(collectionIDs)+len(groupIDs))
	workspaceChildIDs = append(workspaceChildIDs, collectionIDs...)
	workspaceChildIDs = append(workspaceChildIDs, groupIDs...)

	limits, err := h.billing.GetLimits(ctx, userID)
	if err != nil {
		log.Printf("sync push operation=%q user_id=%q entity_ids=%q error=%v", "quota check", userID, boundedSyncEntityIDs(entityIDs), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota check failed"})
		return
	}

	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		respondSyncDatabaseError(c, "transaction begin", userID, entityIDs, err)
		return
	}
	defer tx.Rollback(ctx)

	seq, err := incrementSeq(ctx, tx, userID)
	if err != nil {
		respondSyncDatabaseError(c, "sequence increment", userID, entityIDs, err)
		return
	}

	now := time.Now().UnixMilli()
	var rejected []model.Rejected
	ownedWorkspaceIDs := entityIDSet{}
	unavailableWorkspaceIDs := parentAvailability{}
	if len(req.Entities.Workspaces) > 0 || len(req.Entities.Collections) > 0 || len(req.Entities.Groups) > 0 {
		rows, queryErr := tx.Query(ctx, `SELECT id, is_deleted FROM workspaces WHERE user_id = $1`, userID)
		if queryErr != nil {
			respondSyncDatabaseError(c, "workspace parent query", userID, workspaceChildIDs, queryErr)
			return
		}
		for rows.Next() {
			var (
				id        string
				isDeleted int
			)
			if scanErr := rows.Scan(&id, &isDeleted); scanErr != nil {
				rows.Close()
				respondSyncDatabaseError(c, "workspace parent scan", userID, workspaceChildIDs, scanErr)
				return
			}
			if req.ProtocolVersion != 2 {
				ownedWorkspaceIDs.Add(id)
				continue
			}
			switch isDeleted {
			case 0:
				ownedWorkspaceIDs.Add(id)
			case 1:
				unavailableWorkspaceIDs.Set(id, model.RejectionReasonParentDeleted)
			case 2:
				unavailableWorkspaceIDs.Set(id, model.RejectionReasonPermanentlyDeleted)
			}
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			respondSyncDatabaseError(c, "workspace parent iteration", userID, workspaceChildIDs, rowsErr)
			return
		}
	}

	ownedCollectionIDs := entityIDSet{}
	unavailableCollectionIDs := parentAvailability{}
	collectionStates := map[string]int{}
	collectionWorkspaceIDs := map[string]string{}
	if len(req.Entities.Bookmarks) > 0 {
		rows, queryErr := tx.Query(ctx, `
			SELECT c.id, c.is_deleted, c.workspace_id, w.is_deleted
			FROM collections c
			LEFT JOIN workspaces w ON w.id = c.workspace_id AND w.user_id = c.user_id
			WHERE c.user_id = $1`, userID)
		if queryErr != nil {
			respondSyncDatabaseError(c, "collection parent query", userID, bookmarkIDs, queryErr)
			return
		}
		for rows.Next() {
			var (
				id             string
				isDeleted      int
				workspaceID    *string
				workspaceState *int
			)
			if scanErr := rows.Scan(&id, &isDeleted, &workspaceID, &workspaceState); scanErr != nil {
				rows.Close()
				respondSyncDatabaseError(c, "collection parent scan", userID, bookmarkIDs, scanErr)
				return
			}
			collectionStates[id] = isDeleted
			if workspaceID != nil {
				collectionWorkspaceIDs[id] = *workspaceID
			}
			if req.ProtocolVersion != 2 {
				ownedCollectionIDs.Add(id)
				continue
			}
			switch {
			case isDeleted == 2:
				unavailableCollectionIDs.Set(id, model.RejectionReasonPermanentlyDeleted)
			case isDeleted == 1:
				unavailableCollectionIDs.Set(id, model.RejectionReasonParentDeleted)
			case workspaceID == nil:
				ownedCollectionIDs.Add(id)
			case workspaceState == nil:
				// The collection points at an unowned or missing Workspace.
			case *workspaceState == 1:
				unavailableCollectionIDs.Set(id, model.RejectionReasonParentDeleted)
			case *workspaceState == 2:
				unavailableCollectionIDs.Set(id, model.RejectionReasonPermanentlyDeleted)
			default:
				ownedCollectionIDs.Add(id)
			}
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			respondSyncDatabaseError(c, "collection parent iteration", userID, bookmarkIDs, rowsErr)
			return
		}
	}

	acceptedWorkspaceIDs := entityIDSet{}
	acceptedCollectionIDs := entityIDSet{}
	refreshWorkspaceCollections := func(workspaceID string, workspaceReason string) {
		for collectionID, parentWorkspaceID := range collectionWorkspaceIDs {
			if parentWorkspaceID != workspaceID {
				continue
			}
			ownedCollectionIDs.Delete(collectionID)
			acceptedCollectionIDs.Delete(collectionID)
			switch {
			case workspaceReason == model.RejectionReasonPermanentlyDeleted || collectionStates[collectionID] == 2:
				unavailableCollectionIDs.Set(collectionID, model.RejectionReasonPermanentlyDeleted)
			case workspaceReason == model.RejectionReasonParentDeleted || collectionStates[collectionID] == 1:
				unavailableCollectionIDs.Set(collectionID, model.RejectionReasonParentDeleted)
			default:
				ownedCollectionIDs.Add(collectionID)
				unavailableCollectionIDs.Delete(collectionID)
			}
		}
	}
	var searchUpserts []search.BookmarkDoc
	var searchDeletes []string

	legacyMetadataWorkspaceIDs := []string{}
	legacyMetadataWorkspaceIDSet := entityIDSet{}
	if req.ProtocolVersion == 0 {
		for _, workspace := range req.Entities.Workspaces {
			if workspace.DeletedAt == nil {
				legacyMetadataWorkspaceIDs = append(legacyMetadataWorkspaceIDs, workspace.ID)
				legacyMetadataWorkspaceIDSet.Add(workspace.ID)
			}
		}
	}
	deletedLegacyMetadataWorkspaces := entityIDSet{}
	if len(legacyMetadataWorkspaceIDs) > 0 {
		rows, queryErr := tx.Query(ctx, `
			SELECT id, is_deleted
			FROM workspaces
			WHERE user_id = $1 AND is_deleted < 2
			ORDER BY id
			FOR UPDATE`, userID)
		if queryErr != nil {
			respondSyncDatabaseError(c, "legacy workspace metadata query", userID, legacyMetadataWorkspaceIDs, queryErr)
			return
		}
		for rows.Next() {
			var (
				id        string
				isDeleted int
			)
			if scanErr := rows.Scan(&id, &isDeleted); scanErr != nil {
				rows.Close()
				respondSyncDatabaseError(c, "legacy workspace metadata scan", userID, legacyMetadataWorkspaceIDs, scanErr)
				return
			}
			if legacyMetadataWorkspaceIDSet.Has(id) && isDeleted == 1 {
				deletedLegacyMetadataWorkspaces.Add(id)
			}
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			respondSyncDatabaseError(c, "legacy workspace metadata iteration", userID, legacyMetadataWorkspaceIDs, rowsErr)
			return
		}
	}

	// ── Pre-fetch quota baselines ─────────────────────────────────────────────
	// One query per quota-limited entity type, regardless of push size.
	// Replaces the previous O(n) per-entity COUNT(*) pattern.

	workspaceQuota := newRetainedQuota(limits.MaxWorkspaces)
	if limits.MaxWorkspaces != -1 && len(req.Entities.Workspaces) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id, updated_at FROM workspaces WHERE user_id = $1 AND is_deleted < 2`, userID)
		if err != nil {
			respondSyncDatabaseError(c, "workspace quota query", userID, workspaceIDs, err)
			return
		}
		for rows.Next() {
			var (
				id        string
				updatedAt int64
			)
			if err := rows.Scan(&id, &updatedAt); err != nil {
				rows.Close()
				respondSyncDatabaseError(c, "workspace quota scan", userID, workspaceIDs, err)
				return
			}
			workspaceQuota.AddRetained(id, updatedAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			respondSyncDatabaseError(c, "workspace quota iteration", userID, workspaceIDs, err)
			return
		}
	}

	collectionQuota := newRetainedQuota(limits.MaxCollections)
	if limits.MaxCollections != -1 && len(req.Entities.Collections) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id, updated_at FROM collections WHERE user_id = $1 AND is_deleted < 2`, userID)
		if err != nil {
			respondSyncDatabaseError(c, "collection quota query", userID, collectionIDs, err)
			return
		}
		for rows.Next() {
			var (
				id        string
				updatedAt int64
			)
			if err := rows.Scan(&id, &updatedAt); err != nil {
				rows.Close()
				respondSyncDatabaseError(c, "collection quota scan", userID, collectionIDs, err)
				return
			}
			collectionQuota.AddRetained(id, updatedAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			respondSyncDatabaseError(c, "collection quota iteration", userID, collectionIDs, err)
			return
		}
	}

	groupQuota := newRetainedQuota(limits.MaxSavedGroups)
	if limits.MaxSavedGroups != -1 && len(req.Entities.Groups) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id, updated_at FROM groups WHERE user_id = $1 AND is_deleted < 2`, userID)
		if err != nil {
			respondSyncDatabaseError(c, "saved group quota query", userID, groupIDs, err)
			return
		}
		for rows.Next() {
			var (
				id        string
				updatedAt int64
			)
			if err := rows.Scan(&id, &updatedAt); err != nil {
				rows.Close()
				respondSyncDatabaseError(c, "saved group quota scan", userID, groupIDs, err)
				return
			}
			groupQuota.AddRetained(id, updatedAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			respondSyncDatabaseError(c, "saved group quota iteration", userID, groupIDs, err)
			return
		}
	}

	bookmarkQuota := newRetainedQuota(limits.MaxBookmarks)
	if limits.MaxBookmarks != -1 && len(req.Entities.Bookmarks) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT id, updated_at FROM bookmarks WHERE user_id = $1 AND is_trashed < 2`, userID)
		if err != nil {
			respondSyncDatabaseError(c, "bookmark quota query", userID, bookmarkIDs, err)
			return
		}
		for rows.Next() {
			var (
				id        string
				updatedAt int64
			)
			if err := rows.Scan(&id, &updatedAt); err != nil {
				rows.Close()
				respondSyncDatabaseError(c, "bookmark quota scan", userID, bookmarkIDs, err)
				return
			}
			bookmarkQuota.AddRetained(id, updatedAt)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			respondSyncDatabaseError(c, "bookmark quota iteration", userID, bookmarkIDs, err)
			return
		}
	}

	// ── Workspaces ────────────────────────────────────────────────────────────
	var wsUpserts []model.SyncWorkspaceMutation
	legacyDeletedWorkspaceIDs := []string{}
	queueWorkspaceUpsert := func(ws model.SyncWorkspaceMutation) {
		if !workspaceQuota.Admit(ws.ID, false, now) {
			rejected = append(rejected, model.Rejected{ID: ws.ID, Reason: "quota_exceeded", Type: "workspace"})
			unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
			return
		}
		wsUpserts = append(wsUpserts, ws)
	}
	for _, ws := range req.Entities.Workspaces {
		if req.ProtocolVersion != 0 || ws.DeletedAt == nil {
			continue
		}
		effect, rejection, lifecycleErr := h.lifecycle.ApplyInTx(
			ctx, tx, userID, ws.ID, model.WorkspaceLifecycleDelete, 0, seq, now,
		)
		if lifecycleErr != nil {
			respondSyncDatabaseError(c, "legacy workspace delete", userID, []string{ws.ID}, lifecycleErr)
			return
		}
		if rejection != nil {
			rejected = append(rejected, *rejection)
			if !legacyMetadataWorkspaceIDSet.Has(ws.ID) {
				unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
			}
			continue
		}
		acceptedWorkspaceIDs.Add(ws.ID)
		ownedWorkspaceIDs.Add(ws.ID)
		unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
		deletedLegacyMetadataWorkspaces.Add(ws.ID)
		legacyDeletedWorkspaceIDs = append(legacyDeletedWorkspaceIDs, ws.ID)
		searchDeletes = append(searchDeletes, effect.SearchDeletes...)
		searchUpserts = append(searchUpserts, effect.SearchUpserts...)
	}
	for _, ws := range req.Entities.Workspaces {
		if req.ProtocolVersion == 0 {
			if ws.DeletedAt != nil {
				continue
			}
			if deletedLegacyMetadataWorkspaces.Has(ws.ID) {
				rejected = append(rejected, model.Rejected{
					ID: ws.ID, Reason: model.RejectionReasonWorkspaceDeleted, Type: "workspace",
				})
				unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
				continue
			}
			queueWorkspaceUpsert(ws)
			continue
		}

		if req.ProtocolVersion != 2 {
			queueWorkspaceUpsert(ws)
			continue
		}

		if ws.LifecycleAction != "" {
			effect, rejection, lifecycleErr := h.lifecycle.ApplyInTx(
				ctx, tx, userID, ws.ID, ws.LifecycleAction, 1, seq, now,
			)
			if lifecycleErr != nil {
				respondSyncDatabaseError(c, "workspace lifecycle", userID, []string{ws.ID}, lifecycleErr)
				return
			}
			if rejection != nil {
				rejected = append(rejected, *rejection)
				if !ownedWorkspaceIDs.Has(ws.ID) {
					if _, exists := unavailableWorkspaceIDs[ws.ID]; !exists {
						unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
					}
				}
				continue
			}

			searchDeletes = append(searchDeletes, effect.SearchDeletes...)
			searchUpserts = append(searchUpserts, effect.SearchUpserts...)
			switch ws.LifecycleAction {
			case model.WorkspaceLifecycleDelete:
				ownedWorkspaceIDs.Delete(ws.ID)
				acceptedWorkspaceIDs.Delete(ws.ID)
				unavailableWorkspaceIDs.Set(ws.ID, model.RejectionReasonParentDeleted)
				refreshWorkspaceCollections(ws.ID, model.RejectionReasonParentDeleted)
			case model.WorkspaceLifecycleRestore:
				ownedWorkspaceIDs.Add(ws.ID)
				acceptedWorkspaceIDs.Add(ws.ID)
				unavailableWorkspaceIDs.Delete(ws.ID)
				refreshWorkspaceCollections(ws.ID, "")
			case model.WorkspaceLifecyclePurge:
				ownedWorkspaceIDs.Delete(ws.ID)
				acceptedWorkspaceIDs.Delete(ws.ID)
				unavailableWorkspaceIDs.Set(ws.ID, model.RejectionReasonPermanentlyDeleted)
				refreshWorkspaceCollections(ws.ID, model.RejectionReasonPermanentlyDeleted)
				workspaceQuota.ReleaseApplied(ws.ID)
			}
			continue
		}

		if reason, exists := unavailableWorkspaceIDs[ws.ID]; exists {
			if reason == model.RejectionReasonParentDeleted {
				reason = model.RejectionReasonWorkspaceDeleted
			}
			rejected = append(rejected, model.Rejected{ID: ws.ID, Reason: reason, Type: "workspace"})
			continue
		}
		ws.DeletedAt = nil
		queueWorkspaceUpsert(ws)
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
				respondSyncDatabaseError(c, "workspace upsert", userID, workspaceIDs, err)
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, staleRejection(ws.ID, "workspace"))
				if !ownedWorkspaceIDs.Has(ws.ID) {
					if _, exists := unavailableWorkspaceIDs[ws.ID]; !exists {
						unavailableWorkspaceIDs.Set(ws.ID, "parent_rejected")
					}
				}
				continue
			}
			acceptedWorkspaceIDs.Add(ws.ID)
			ownedWorkspaceIDs.Add(ws.ID)
		}
		if closeErr := br.Close(); closeErr != nil {
			respondSyncDatabaseError(c, "close workspace batch", userID, workspaceIDs, closeErr)
			return
		}
	}
	if len(legacyDeletedWorkspaceIDs) > 0 {
		rows, queryErr := tx.Query(ctx, `
			SELECT id
			FROM collections
			WHERE user_id = $1 AND workspace_id = ANY($2)
			ORDER BY id`, userID, legacyDeletedWorkspaceIDs)
		if queryErr != nil {
			respondSyncDatabaseError(c, "legacy workspace collection query", userID, legacyDeletedWorkspaceIDs, queryErr)
			return
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				rows.Close()
				respondSyncDatabaseError(c, "legacy workspace collection scan", userID, legacyDeletedWorkspaceIDs, scanErr)
				return
			}
			unavailableCollectionIDs.Set(id, "parent_rejected")
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			respondSyncDatabaseError(c, "legacy workspace collection iteration", userID, legacyDeletedWorkspaceIDs, rowsErr)
			return
		}
	}

	// ── Collections ───────────────────────────────────────────────────────────
	var colUpserts []model.Collection
	for _, col := range req.Entities.Collections {
		if dependency := classifyParentRejection(
			col.ID, "collection", col.WorkspaceID, "workspace", true,
			ownedWorkspaceIDs, acceptedWorkspaceIDs, unavailableWorkspaceIDs,
		); dependency != nil {
			rejected = append(rejected, *dependency)
			if _, inheritedLifecycleReason := unavailableCollectionIDs[col.ID]; !inheritedLifecycleReason {
				unavailableCollectionIDs.Set(col.ID, "parent_rejected")
			}
			continue
		}
		if !collectionQuota.Admit(col.ID, col.IsDeleted == 2, now) {
			rejected = append(rejected, model.Rejected{ID: col.ID, Reason: "quota_exceeded", Type: "collection"})
			unavailableCollectionIDs.Set(col.ID, "parent_rejected")
			continue
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
				respondSyncDatabaseError(c, "collection upsert", userID, collectionIDs, err)
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, staleRejection(col.ID, "collection"))
				if !ownedCollectionIDs.Has(col.ID) {
					if _, exists := unavailableCollectionIDs[col.ID]; !exists {
						unavailableCollectionIDs.Set(col.ID, "parent_rejected")
					}
				}
			} else if col.IsDeleted == 2 {
				// Cascade permanent deletion to any remaining bookmarks in this collection.
				// The client pushes individual is_trashed:2 tombstones, but if that push
				// was skipped (e.g. empty local IDB on a fresh session), bookmarks would
				// stay at is_trashed=1 forever. This ensures the server is the final authority.
				cascadeIDs = append(cascadeIDs, col.ID)
			}
			if ct.RowsAffected() > 0 {
				if req.ProtocolVersion != 2 || col.IsDeleted == 0 {
					acceptedCollectionIDs.Add(col.ID)
					ownedCollectionIDs.Add(col.ID)
					unavailableCollectionIDs.Delete(col.ID)
				} else if col.IsDeleted == 1 {
					acceptedCollectionIDs.Delete(col.ID)
					ownedCollectionIDs.Delete(col.ID)
					unavailableCollectionIDs.Set(col.ID, model.RejectionReasonParentDeleted)
				} else {
					acceptedCollectionIDs.Delete(col.ID)
					ownedCollectionIDs.Delete(col.ID)
					unavailableCollectionIDs.Set(col.ID, model.RejectionReasonPermanentlyDeleted)
				}
			}
		}
		if closeErr := br.Close(); closeErr != nil {
			respondSyncDatabaseError(c, "close collection batch", userID, collectionIDs, closeErr)
			return
		}
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
					respondSyncDatabaseError(c, "bookmark cascade", userID, cascadeIDs, err)
					return
				}
			}
			if closeErr := cbr.Close(); closeErr != nil {
				respondSyncDatabaseError(c, "close bookmark cascade batch", userID, cascadeIDs, closeErr)
				return
			}
		}
	}

	// ── Bookmarks ─────────────────────────────────────────────────────────────
	var bookmarkUpserts []model.Bookmark
	for _, bookmark := range req.Entities.Bookmarks {
		if dependency := classifyParentRejection(
			bookmark.ID, "bookmark", bookmark.CollectionID, "collection", true,
			ownedCollectionIDs, acceptedCollectionIDs, unavailableCollectionIDs,
		); dependency != nil {
			rejected = append(rejected, *dependency)
			continue
		}
		if !bookmarkQuota.Admit(bookmark.ID, bookmark.IsTrashed == 2, now) {
			rejected = append(rejected, model.Rejected{
				ID: bookmark.ID, Reason: "quota_exceeded", Type: "bookmark",
			})
			continue
		}
		bookmarkUpserts = append(bookmarkUpserts, bookmark)
	}
	if len(bookmarkUpserts) > 0 {
		batch := &pgx.Batch{}
		for _, bm := range bookmarkUpserts {
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
		for _, bm := range bookmarkUpserts {
			ct, err := br.Exec()
			if err != nil {
				br.Close()
				respondSyncDatabaseError(c, "bookmark upsert", userID, bookmarkIDs, err)
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, staleRejection(bm.ID, "bookmark"))
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
		if closeErr := br.Close(); closeErr != nil {
			respondSyncDatabaseError(c, "close bookmark batch", userID, bookmarkIDs, closeErr)
			return
		}
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
				respondSyncDatabaseError(c, "tag upsert", userID, tagIDs, err)
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, staleRejection(t.ID, "tag"))
			}
		}
		if closeErr := br.Close(); closeErr != nil {
			respondSyncDatabaseError(c, "close tag batch", userID, tagIDs, closeErr)
			return
		}
	}

	// ── Groups ────────────────────────────────────────────────────────────────
	var groupUpserts []model.Group
	for _, g := range req.Entities.Groups {
		if dependency := classifyParentRejection(
			g.ID, "saved_group", g.WorkspaceID, "workspace", false,
			ownedWorkspaceIDs, acceptedWorkspaceIDs, unavailableWorkspaceIDs,
		); dependency != nil {
			rejected = append(rejected, *dependency)
			continue
		}
		if !groupQuota.Admit(g.ID, g.IsDeleted == 2, now) {
			rejected = append(rejected, model.Rejected{ID: g.ID, Reason: "quota_exceeded", Type: "saved_group"})
			continue
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
				respondSyncDatabaseError(c, "saved group upsert", userID, groupIDs, err)
				return
			}
			if ct.RowsAffected() == 0 {
				rejected = append(rejected, staleRejection(g.ID, "saved_group"))
			} else {
				acceptedGroups = append(acceptedGroups, g)
			}
		}
		if closeErr := br.Close(); closeErr != nil {
			respondSyncDatabaseError(c, "close saved group batch", userID, groupIDs, closeErr)
			return
		}
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
					respondSyncDatabaseError(c, "group tab clear", userID, groupIDs, err)
					return
				}
				for range g.Tabs {
					if _, err := tbr.Exec(); err != nil { // INSERT tab
						tbr.Close()
						respondSyncDatabaseError(c, "group tab insert", userID, groupIDs, err)
						return
					}
				}
			}
			if closeErr := tbr.Close(); closeErr != nil {
				respondSyncDatabaseError(c, "close group tab batch", userID, groupIDs, closeErr)
				return
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		respondSyncDatabaseError(c, "transaction commit", userID, entityIDs, err)
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

	resp := model.SyncPullResponse{
		Capabilities: model.SyncCapabilities{WorkspaceParentTombstone: true},
	}

	// Workspaces
	wsRows, err := h.db.Query(ctx,
		`SELECT id, user_id, name, icon, color, position, seq, deleted_at, is_deleted, deletion_model, created_at, updated_at
         FROM workspaces WHERE user_id=$1 AND seq>$2 ORDER BY seq ASC`,
		userID, afterSeq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "workspaces query failed"})
		return
	}
	defer wsRows.Close()
	for wsRows.Next() {
		var ws model.SyncWorkspace
		if err := wsRows.Scan(&ws.ID, &ws.UserID, &ws.Name, &ws.Icon, &ws.Color, &ws.Position,
			&ws.Seq, &ws.DeletedAt, &ws.IsDeleted, &ws.DeletionModel, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
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
		resp.Entities.Workspaces = []model.SyncWorkspace{}
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
