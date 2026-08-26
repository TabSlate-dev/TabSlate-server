package handler

import (
	"net/http"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
)

// TestCollectionDeleteSetsIsDeleted covers a bug where REST DELETE
// /collections/:id only wrote deleted_at, leaving is_deleted at 0. That split
// state made the collection invisible to normal reads (which check
// deleted_at IS NULL) while every quota/cleanup/trash query — which classify
// by is_deleted, matching the Sync path — still saw it as active: the
// collection could never expire, never released quota, and never appeared in
// trash listings.
func TestCollectionDeleteSetsIsDeleted(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "collection-delete-is-deleted@example.com", password: "password123",
	})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "collection-delete-workspace", 0)
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections (id, user_id, workspace_id, name, position, seq, is_deleted, created_at, updated_at)
		VALUES ('collection-delete-target', $1, 'collection-delete-workspace', 'Deletable collection', 0, 1, 0, 1, 1)`,
		userID,
	); err != nil {
		t.Fatalf("insert collection: %v", err)
	}

	handler := NewCollectionHandler(testDB, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: syncTestLimits()})
	recorder := performWorkspaceRoute(
		t, userID, http.MethodDelete, "/api/collections/:id",
		"/api/collections/collection-delete-target", handler.Delete,
	)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want 204", recorder.Code, recorder.Body.String())
	}

	var deletedAtSet bool
	var isDeleted int
	if err := testDB.QueryRow(t.Context(),
		`SELECT deleted_at IS NOT NULL, is_deleted FROM collections WHERE id = 'collection-delete-target'`,
	).Scan(&deletedAtSet, &isDeleted); err != nil {
		t.Fatalf("query collection: %v", err)
	}
	if !deletedAtSet || isDeleted != 1 {
		t.Fatalf("collection = {deletedAtSet:%v isDeleted:%d}, want {true 1}", deletedAtSet, isDeleted)
	}
}
