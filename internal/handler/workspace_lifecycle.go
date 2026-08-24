package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/jackc/pgx/v5"
)

type WorkspaceLifecycleEffect struct {
	Changed       bool
	Seq           int64
	SearchDeletes []string
	SearchUpserts []search.BookmarkDoc
}

type WorkspaceLifecycleService struct {
	db     *db.DB
	hub    pubsub.Hub
	search *search.Client
}

func NewWorkspaceLifecycleService(d *db.DB, hub pubsub.Hub, searchClient *search.Client) *WorkspaceLifecycleService {
	return &WorkspaceLifecycleService{db: d, hub: hub, search: searchClient}
}

func firstWorkspaceLifecycleService(services []*WorkspaceLifecycleService) *WorkspaceLifecycleService {
	if len(services) == 0 {
		return nil
	}
	return services[0]
}

func (s *WorkspaceLifecycleService) Apply(
	ctx context.Context,
	userID string,
	workspaceID string,
	action model.WorkspaceLifecycleAction,
	deletionModel int,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("begin workspace lifecycle transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	seq, err := incrementSeq(ctx, tx, userID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, err
	}
	effect, rejection, err := s.ApplyInTx(
		ctx, tx, userID, workspaceID, action, deletionModel, seq, time.Now().UnixMilli(),
	)
	if err != nil || rejection != nil || !effect.Changed {
		return effect, rejection, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("commit workspace lifecycle transaction: %w", err)
	}

	s.hub.Broadcast(userID, effect.Seq)
	s.search.BulkDeleteAsync(effect.SearchDeletes)
	s.search.BulkUpsertAsync(effect.SearchUpserts)
	return effect, nil, nil
}

func (s *WorkspaceLifecycleService) ApplyInTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	workspaceID string,
	action model.WorkspaceLifecycleAction,
	deletionModel int,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, is_deleted, deletion_model, seq
		FROM workspaces
		WHERE user_id = $1 AND is_deleted < 2
		ORDER BY id
		FOR UPDATE`, userID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("lock workspace lifecycle rows: %w", err)
	}

	activeCount := 0
	targetState := -1
	targetDeletionModel := -1
	targetSeq := int64(0)
	for rows.Next() {
		var (
			id              string
			isDeleted       int
			currentDelModel int
			currentSeq      int64
		)
		if err := rows.Scan(&id, &isDeleted, &currentDelModel, &currentSeq); err != nil {
			rows.Close()
			return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("scan workspace lifecycle row: %w", err)
		}
		if isDeleted == 0 {
			activeCount++
		}
		if id == workspaceID {
			targetState = isDeleted
			targetDeletionModel = currentDelModel
			targetSeq = currentSeq
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("iterate workspace lifecycle rows: %w", err)
	}

	if targetState == -1 {
		err := tx.QueryRow(ctx, `
			SELECT is_deleted, deletion_model, seq
			FROM workspaces
			WHERE user_id = $1 AND id = $2
			FOR UPDATE`, userID, workspaceID).Scan(&targetState, &targetDeletionModel, &targetSeq)
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(workspaceID, "stale"), nil
		}
		if err != nil {
			return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("lock terminal workspace lifecycle row: %w", err)
		}
	}

	if targetState == 2 {
		if action == model.WorkspaceLifecyclePurge {
			return WorkspaceLifecycleEffect{Seq: targetSeq}, nil, nil
		}
		return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(
			workspaceID, model.RejectionReasonPermanentlyDeleted,
		), nil
	}

	switch action {
	case model.WorkspaceLifecycleDelete:
		if targetState == 1 {
			if targetDeletionModel != deletionModel {
				return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(workspaceID, "stale"), nil
			}
			return workspaceLifecycleNoop(ctx, tx, workspaceID)
		}
		if activeCount <= 1 {
			return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(
				workspaceID, model.RejectionReasonLastActiveWorkspace,
			), nil
		}
		if deletionModel == 0 {
			effect, err := s.applyLegacyDeleteInTx(ctx, tx, userID, workspaceID, seq, now)
			return effect, nil, err
		}
		return applyWorkspaceDelete(ctx, tx, userID, workspaceID, deletionModel, seq, now)

	case model.WorkspaceLifecycleRestore:
		if targetState == 0 {
			return workspaceLifecycleNoop(ctx, tx, workspaceID)
		}
		if targetDeletionModel == 0 {
			effect, err := s.applyLegacyRestoreInTx(ctx, tx, model.SyncWorkspace{
				ID:            workspaceID,
				UserID:        userID,
				Seq:           targetSeq,
				IsDeleted:     targetState,
				DeletionModel: targetDeletionModel,
			}, seq, now)
			return effect, nil, err
		}
		return applyWorkspaceRestore(ctx, tx, userID, workspaceID, seq, now)

	case model.WorkspaceLifecyclePurge:
		if targetState == 0 {
			return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(workspaceID, "stale"), nil
		}
		return applyWorkspacePurge(ctx, tx, workspaceID, seq, now)

	default:
		return WorkspaceLifecycleEffect{}, workspaceLifecycleRejection(workspaceID, "stale"), nil
	}
}

func (s *WorkspaceLifecycleService) applyLegacyDeleteInTx(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	workspaceID string,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, error) {
	searchDeletes, err := legacyWorkspaceBookmarkIDs(ctx, tx, userID, workspaceID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workspaces
		SET is_deleted = 1, deletion_model = 0, deleted_at = $1, seq = $2, updated_at = $1
		WHERE id = $3 AND user_id = $4 AND is_deleted = 0`, now, seq, workspaceID, userID); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("delete legacy workspace root: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookmarks b
		SET is_trashed = 1, deleted_at = $1, seq = $2, updated_at = $1
		FROM collections c
		WHERE b.collection_id = c.id
		  AND b.user_id = $3
		  AND c.user_id = $3
		  AND c.workspace_id = $4
		  AND c.is_deleted = 0
		  AND b.is_trashed = 0`, now, seq, userID, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("delete legacy workspace bookmarks: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collections
		SET is_deleted = 1, deleted_at = $1, seq = $2, updated_at = $1
		WHERE workspace_id = $3 AND user_id = $4 AND is_deleted = 0`, now, seq, workspaceID, userID); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("delete legacy workspace collections: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE groups
		SET is_deleted = 1, deleted_at = $1, seq = $2, updated_at = $1
		WHERE workspace_id = $3 AND user_id = $4 AND is_deleted = 0`, now, seq, workspaceID, userID); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("delete legacy workspace groups: %w", err)
	}
	return WorkspaceLifecycleEffect{Changed: true, Seq: seq, SearchDeletes: searchDeletes}, nil
}

func (s *WorkspaceLifecycleService) applyLegacyRestoreInTx(
	ctx context.Context,
	tx pgx.Tx,
	workspace model.SyncWorkspace,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, error) {
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM collections
		WHERE workspace_id = $1 AND user_id = $2 AND is_deleted = 1 AND seq >= $3
		ORDER BY id
		FOR UPDATE`, workspace.ID, workspace.UserID, workspace.Seq)
	if err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("query legacy restore collection evidence: %w", err)
	}
	candidateCollectionIDs := []string{}
	for rows.Next() {
		var collectionID string
		if err := rows.Scan(&collectionID); err != nil {
			rows.Close()
			return WorkspaceLifecycleEffect{}, fmt.Errorf("scan legacy restore collection evidence: %w", err)
		}
		candidateCollectionIDs = append(candidateCollectionIDs, collectionID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("iterate legacy restore collection evidence: %w", err)
	}

	searchUpserts, err := restoreLegacyWorkspaceBookmarks(
		ctx, tx, workspace.UserID, candidateCollectionIDs, workspace.Seq, seq, now,
	)
	if err != nil {
		return WorkspaceLifecycleEffect{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collections
		SET is_deleted = 0, deleted_at = NULL, seq = $1, updated_at = $2
		WHERE user_id = $3 AND id = ANY($4) AND is_deleted = 1 AND seq >= $5`,
		seq, now, workspace.UserID, candidateCollectionIDs, workspace.Seq,
	); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("restore legacy workspace collections: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE groups
		SET is_deleted = 0, deleted_at = NULL, seq = $1, updated_at = $2
		WHERE workspace_id = $3 AND user_id = $4 AND is_deleted = 1 AND seq >= $5`,
		seq, now, workspace.ID, workspace.UserID, workspace.Seq,
	); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("restore legacy workspace groups: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workspaces
		SET is_deleted = 0, deletion_model = 1, deleted_at = NULL, seq = $1, updated_at = $2
		WHERE id = $3 AND user_id = $4 AND is_deleted = 1 AND deletion_model = 0 AND seq = $5`,
		seq, now, workspace.ID, workspace.UserID, workspace.Seq,
	); err != nil {
		return WorkspaceLifecycleEffect{}, fmt.Errorf("restore legacy workspace root: %w", err)
	}
	return WorkspaceLifecycleEffect{Changed: true, Seq: seq, SearchUpserts: searchUpserts}, nil
}

func legacyWorkspaceBookmarkIDs(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	workspaceID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT b.id
		FROM bookmarks b
		JOIN collections c ON c.id = b.collection_id
		WHERE b.user_id = $1
		  AND c.user_id = $1
		  AND c.workspace_id = $2
		  AND c.is_deleted = 0
		  AND b.is_trashed = 0
		ORDER BY b.id`, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("query legacy workspace bookmark ids: %w", err)
	}
	defer rows.Close()

	var bookmarkIDs []string
	for rows.Next() {
		var bookmarkID string
		if err := rows.Scan(&bookmarkID); err != nil {
			return nil, fmt.Errorf("scan legacy workspace bookmark id: %w", err)
		}
		bookmarkIDs = append(bookmarkIDs, bookmarkID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy workspace bookmark ids: %w", err)
	}
	return bookmarkIDs, nil
}

func restoreLegacyWorkspaceBookmarks(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	candidateCollectionIDs []string,
	minimumSeq int64,
	seq int64,
	now int64,
) ([]search.BookmarkDoc, error) {
	rows, err := tx.Query(ctx, `
		UPDATE bookmarks
		SET is_trashed = 0, is_archived = FALSE, deleted_at = NULL, seq = $1, updated_at = $2
		WHERE user_id = $3
		  AND collection_id = ANY($4)
		  AND is_trashed = 1
		  AND seq >= $5
		RETURNING id, user_id, title, url, COALESCE(description, ''), collection_id, is_archived`,
		seq, now, userID, candidateCollectionIDs, minimumSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("restore legacy workspace bookmarks: %w", err)
	}
	defer rows.Close()

	var documents []search.BookmarkDoc
	for rows.Next() {
		var document search.BookmarkDoc
		if err := rows.Scan(
			&document.ID,
			&document.UserID,
			&document.Title,
			&document.URL,
			&document.Description,
			&document.CollectionID,
			&document.IsArchived,
		); err != nil {
			return nil, fmt.Errorf("scan restored legacy workspace bookmark: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate restored legacy workspace bookmarks: %w", err)
	}
	return documents, nil
}

func applyWorkspaceDelete(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	workspaceID string,
	deletionModel int,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE workspaces
		SET is_deleted = 1, deletion_model = $1, deleted_at = $2, seq = $3, updated_at = $2
		WHERE id = $4 AND user_id = $5`, deletionModel, now, seq, workspaceID, userID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("delete workspace root: %w", err)
	}
	searchDeletes, err := workspaceBookmarkIDs(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, err
	}
	return WorkspaceLifecycleEffect{Changed: true, Seq: seq, SearchDeletes: searchDeletes}, nil, nil
}

func applyWorkspaceRestore(
	ctx context.Context,
	tx pgx.Tx,
	userID string,
	workspaceID string,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE workspaces
		SET is_deleted = 0, deletion_model = 1, deleted_at = NULL, seq = $1, updated_at = $2
		WHERE id = $3 AND user_id = $4`, seq, now, workspaceID, userID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("restore workspace root: %w", err)
	}
	searchUpserts, err := workspaceSearchDocuments(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, err
	}
	return WorkspaceLifecycleEffect{Changed: true, Seq: seq, SearchUpserts: searchUpserts}, nil, nil
}

func applyWorkspacePurge(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
	seq int64,
	now int64,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE workspaces
		SET is_deleted = 2, name = '', icon = NULL, color = NULL, position = 0, seq = $1, updated_at = $2
		WHERE id = $3`, seq, now, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("scrub workspace root: %w", err)
	}
	searchDeletes, err := workspaceBookmarkIDs(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceLifecycleEffect{}, nil, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM group_tabs
		WHERE group_id IN (SELECT id FROM groups WHERE workspace_id = $1)`, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("purge workspace group tabs: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM groups WHERE workspace_id = $1`, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("purge workspace groups: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM bookmarks
		WHERE collection_id IN (SELECT id FROM collections WHERE workspace_id = $1)`, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("purge workspace bookmarks: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM collections WHERE workspace_id = $1`, workspaceID); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("purge workspace collections: %w", err)
	}
	return WorkspaceLifecycleEffect{Changed: true, Seq: seq, SearchDeletes: searchDeletes}, nil, nil
}

func workspaceBookmarkIDs(ctx context.Context, tx pgx.Tx, workspaceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT b.id
		FROM bookmarks b
		JOIN collections c ON c.id = b.collection_id
		WHERE c.workspace_id = $1
		ORDER BY b.id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("query workspace bookmark ids: %w", err)
	}
	defer rows.Close()

	var bookmarkIDs []string
	for rows.Next() {
		var bookmarkID string
		if err := rows.Scan(&bookmarkID); err != nil {
			return nil, fmt.Errorf("scan workspace bookmark id: %w", err)
		}
		bookmarkIDs = append(bookmarkIDs, bookmarkID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace bookmark ids: %w", err)
	}
	return bookmarkIDs, nil
}

func workspaceSearchDocuments(ctx context.Context, tx pgx.Tx, workspaceID string) ([]search.BookmarkDoc, error) {
	rows, err := tx.Query(ctx, `
		SELECT b.id, b.user_id, b.title, b.url, COALESCE(b.description, ''), b.collection_id, b.is_archived
		FROM bookmarks b
		JOIN collections c ON c.id = b.collection_id
		WHERE c.workspace_id = $1 AND b.is_trashed = 0
		ORDER BY b.id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("query workspace search documents: %w", err)
	}
	defer rows.Close()

	var documents []search.BookmarkDoc
	for rows.Next() {
		var document search.BookmarkDoc
		if err := rows.Scan(
			&document.ID,
			&document.UserID,
			&document.Title,
			&document.URL,
			&document.Description,
			&document.CollectionID,
			&document.IsArchived,
		); err != nil {
			return nil, fmt.Errorf("scan workspace search document: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace search documents: %w", err)
	}
	return documents, nil
}

func workspaceLifecycleNoop(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
) (WorkspaceLifecycleEffect, *model.Rejected, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT seq FROM workspaces WHERE id = $1`, workspaceID).Scan(&seq); err != nil {
		return WorkspaceLifecycleEffect{}, nil, fmt.Errorf("query idempotent workspace sequence: %w", err)
	}
	return WorkspaceLifecycleEffect{Seq: seq}, nil, nil
}

func workspaceLifecycleRejection(workspaceID string, reason string) *model.Rejected {
	return &model.Rejected{ID: workspaceID, Reason: reason, Type: "workspace"}
}
