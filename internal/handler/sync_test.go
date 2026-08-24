package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/gin-gonic/gin"
)

type fixedLimitsProvider struct {
	limits billing.Limits
}

func (provider fixedLimitsProvider) OnUserCreated(context.Context, billing.UserInfo) error {
	return nil
}

func (provider fixedLimitsProvider) GetLimits(context.Context, string) (*billing.Limits, error) {
	limits := provider.limits
	return &limits, nil
}

func (provider fixedLimitsProvider) GetSubscription(context.Context, string) (*billing.Subscription, error) {
	return &billing.Subscription{Plan: billing.PlanFree, Status: "active"}, nil
}

func (provider fixedLimitsProvider) ChangePlan(context.Context, string, string) error { return nil }

func (provider fixedLimitsProvider) CancelSubscription(context.Context, string) error { return nil }

func (provider fixedLimitsProvider) ListInvoices(context.Context, string, int, int) ([]billing.Invoice, error) {
	return []billing.Invoice{}, nil
}

func syncTestLimits() billing.Limits {
	return billing.Limits{
		MaxWorkspaces:  -1,
		MaxBookmarks:   -1,
		MaxCollections: -1,
		MaxTags:        -1,
		MaxSavedGroups: -1,
	}
}

func openSyncTestDB(t *testing.T) *db.DB {
	t.Helper()
	testDB := openAuthTestDB(t)
	if _, err := testDB.Exec(t.Context(), `TRUNCATE TABLE bookmarks, collections, group_tabs, groups, tags, workspaces, refresh_tokens, subscriptions, user_sync_seq, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate sync tables: %v", err)
	}
	return testDB
}

func pushSync(
	t *testing.T,
	testDB *db.DB,
	userID string,
	limits billing.Limits,
	entities model.SyncPushEntities,
) *httptest.ResponseRecorder {
	t.Helper()
	return pushSyncRequest(t, testDB, userID, limits, model.SyncPushRequest{Entities: entities})
}

func pushSyncRequest(
	t *testing.T,
	testDB *db.DB,
	userID string,
	limits billing.Limits,
	request model.SyncPushRequest,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal sync request: %v", err)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	hub := pubsub.NewInMemoryHub()
	lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)
	handler := NewSyncHandler(testDB, nil, hub, fixedLimitsProvider{limits: limits}, lifecycle)
	handler.Push(ginContext)
	return recorder
}

func decodeSyncPushResponse(t *testing.T, recorder *httptest.ResponseRecorder) model.SyncPushResponse {
	t.Helper()
	var response model.SyncPushResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode sync response: %v body=%s", err, recorder.Body.String())
	}
	return response
}

func pullSync(t *testing.T, testDB *db.DB, userID string, afterSeq int64) model.SyncPullResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/sync/pull?after_seq=%d", afterSeq), nil)
	ginContext.Set(middleware.UserIDKey, userID)
	NewSyncHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: syncTestLimits()}).Pull(ginContext)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response model.SyncPullResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	return response
}

func TestSyncPullCapabilities(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-pull-capabilities@example.com", password: "password123"})
	deletedAt := int64(123)
	if _, err := testDB.Exec(t.Context(), `INSERT INTO user_sync_seq (user_id, seq) VALUES ($1, 1)`, userID); err != nil {
		t.Fatalf("insert user sync sequence: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO workspaces (id, user_id, name, icon, color, position, seq, deleted_at, is_deleted, deletion_model, created_at, updated_at)
		VALUES ('parent-tombstone', $1, 'Deleted', NULL, NULL, 0, 1, $2, 2, 1, 1, 1)`, userID, deletedAt); err != nil {
		t.Fatalf("insert workspace parent tombstone: %v", err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/sync/pull?after_seq=0", nil)
	ginContext.Set(middleware.UserIDKey, userID)
	NewSyncHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: syncTestLimits()}).Pull(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response model.SyncPullResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode pull response: %v", err)
	}
	if !response.Capabilities.WorkspaceParentTombstone {
		t.Fatal("workspace_parent_tombstone capability = false, want true")
	}
	if len(response.Entities.Workspaces) != 1 {
		t.Fatalf("workspace count = %d, want 1", len(response.Entities.Workspaces))
	}
	workspace := response.Entities.Workspaces[0]
	if workspace.IsDeleted != 2 {
		t.Fatalf("workspace is_deleted = %d, want 2", workspace.IsDeleted)
	}
	if workspace.DeletionModel != 1 {
		t.Fatalf("workspace deletion_model = %d, want 1", workspace.DeletionModel)
	}
	if workspace.DeletedAt == nil || *workspace.DeletedAt != deletedAt {
		t.Fatalf("workspace deleted_at = %v, want %d", workspace.DeletedAt, deletedAt)
	}
	if workspace.Icon != nil || workspace.Color != nil {
		t.Fatalf("workspace icon/color = %v/%v, want nil/nil", workspace.Icon, workspace.Color)
	}
}

func TestSyncPushV2WorkspaceMetadataCannotTransitionLifecycle(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-v2-metadata@example.com", password: "password123"})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "v2-active", 0)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "v2-deleted", 1)
	deletedAt := int64(9000)
	beforeDeleted := workspaceLifecycleRootSnapshot(t, testDB, "v2-deleted")

	recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		ProtocolVersion: 2,
		Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{
			{ID: "v2-active", Name: "Metadata only", Position: 91, DeletedAt: &deletedAt},
			{ID: "v2-deleted", Name: "Must not restore", Position: 92},
		}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{
		ID: "v2-deleted", Reason: model.RejectionReasonWorkspaceDeleted, Type: "workspace",
	})
	if len(response.Rejected) != 1 {
		t.Fatalf("rejected = %#v, want only workspace_deleted", response.Rejected)
	}

	var (
		name      string
		position  int
		isDeleted int
		gotDelete *int64
	)
	if err := testDB.QueryRow(t.Context(), `SELECT name, position, is_deleted, deleted_at FROM workspaces WHERE id = 'v2-active'`).Scan(
		&name, &position, &isDeleted, &gotDelete,
	); err != nil {
		t.Fatalf("query active workspace: %v", err)
	}
	if name != "Metadata only" || position != 91 || isDeleted != 0 || gotDelete != nil {
		t.Fatalf("active metadata result = {name:%q position:%d state:%d deletedAt:%v}", name, position, isDeleted, gotDelete)
	}
	if afterDeleted := workspaceLifecycleRootSnapshot(t, testDB, "v2-deleted"); afterDeleted != beforeDeleted {
		t.Fatalf("ordinary metadata changed deleted workspace\nbefore=%s\nafter=%s", beforeDeleted, afterDeleted)
	}
}

func TestSyncPushV2WorkspaceLifecycleActionsUseLifecycleTransaction(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-v2-actions@example.com", password: "password123"})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "v2-action", 0)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "v2-action-sibling", 0)
	beforeDescendants := workspaceLifecycleDescendantSnapshot(t, testDB, "v2-action")

	deleteRecorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		ProtocolVersion: 2,
		Entities: model.SyncPushEntities{
			Workspaces: []model.SyncWorkspaceMutation{{
				ID: "v2-action", Name: "Ignored lifecycle metadata", Position: 99,
				LifecycleAction: model.WorkspaceLifecycleDelete,
			}},
			Collections: []model.Collection{{
				ID: "v2-action-collection", WorkspaceID: stringPtr("v2-action"), Name: "Must follow lifecycle action",
			}},
		},
	})
	assertSyncStatusOK(t, deleteRecorder)
	deleteResponse := decodeSyncPushResponse(t, deleteRecorder)
	assertRejected(t, deleteResponse, model.Rejected{
		ID: "v2-action-collection", Reason: model.RejectionReasonParentDeleted, Type: "collection",
		ParentID: "v2-action", ParentType: "workspace",
	})
	if len(deleteResponse.Rejected) != 1 {
		t.Fatalf("delete rejected = %#v, want only descendant parent_deleted", deleteResponse.Rejected)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "v2-action", workspaceLifecycleRootExpectation{
		name: "Workspace v2-action", icon: stringPtr("icon-v2-action"), color: stringPtr("color-v2-action"),
		position: 17, seq: deleteResponse.ServerSeq, isDeleted: 1, deletionModel: 1, deletedAtSet: true,
	})
	if afterDescendants := workspaceLifecycleDescendantSnapshot(t, testDB, "v2-action"); !reflect.DeepEqual(afterDescendants, beforeDescendants) {
		t.Fatalf("parent-only delete changed descendants\nbefore=%#v\nafter=%#v", beforeDescendants, afterDescendants)
	}

	restoreRecorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		ProtocolVersion: 2,
		Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{{
			ID: "v2-action", LifecycleAction: model.WorkspaceLifecycleRestore,
		}}},
	})
	assertSyncStatusOK(t, restoreRecorder)
	restoreResponse := decodeSyncPushResponse(t, restoreRecorder)
	if len(restoreResponse.Rejected) != 0 {
		t.Fatalf("restore rejected = %#v, want none", restoreResponse.Rejected)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "v2-action", workspaceLifecycleRootExpectation{
		name: "Workspace v2-action", icon: stringPtr("icon-v2-action"), color: stringPtr("color-v2-action"),
		position: 17, seq: restoreResponse.ServerSeq, isDeleted: 0, deletionModel: 1, deletedAtSet: false,
	})
}

func TestSyncPushV2WorkspaceParentStatesRejectDescendants(t *testing.T) {
	tests := []struct {
		name       string
		state      int
		wantReason string
	}{
		{name: "soft deleted", state: 1, wantReason: model.RejectionReasonParentDeleted},
		{name: "permanently deleted", state: 2, wantReason: model.RejectionReasonPermanentlyDeleted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testDB := openSyncTestDB(t)
			userID := insertAuthTestUser(t, testDB, authTestUserSeed{
				email: "sync-parent-state-" + fmt.Sprint(test.state) + "@example.com", password: "password123",
			})
			workspaceID := "parent-state-" + fmt.Sprint(test.state)
			collectionID := workspaceID + "-collection"
			insertWorkspaceLifecycleRoot(t, testDB, userID, workspaceID, test.state)
			if _, err := testDB.Exec(t.Context(), `
				INSERT INTO collections (id, user_id, workspace_id, name, position, seq, is_deleted, created_at, updated_at)
				VALUES ($1, $2, $3, 'Retained collection', 0, 1, 0, 1, 1)`,
				collectionID, userID, workspaceID,
			); err != nil {
				t.Fatalf("insert retained collection: %v", err)
			}
			if _, err := testDB.Exec(t.Context(), `
				INSERT INTO bookmarks (id, user_id, collection_id, title, url, position, seq, is_trashed, created_at, updated_at)
				VALUES ($1, $2, $3, 'Retained bookmark', 'https://retained.example.com', 0, 1, 0, 1, 1)`,
				workspaceID+"-bookmark", userID, collectionID,
			); err != nil {
				t.Fatalf("insert retained bookmark: %v", err)
			}
			if _, err := testDB.Exec(t.Context(), `
				INSERT INTO groups (id, user_id, workspace_id, name, color, seq, is_deleted, created_at, updated_at)
				VALUES ($1, $2, $3, 'Retained group', 'blue', 1, 0, 1, 1)`,
				workspaceID+"-group", userID, workspaceID,
			); err != nil {
				t.Fatalf("insert retained group: %v", err)
			}

			recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
				ProtocolVersion: 2,
				Entities: model.SyncPushEntities{
					Workspaces:  []model.SyncWorkspaceMutation{{ID: workspaceID, Name: "Rejected metadata"}},
					Collections: []model.Collection{{ID: collectionID, WorkspaceID: &workspaceID, Name: "Rejected collection"}},
					Bookmarks:   []model.Bookmark{{ID: workspaceID + "-bookmark", CollectionID: &collectionID, Title: "Rejected bookmark", URL: "https://retained.example.com"}},
					Groups:      []model.Group{{ID: workspaceID + "-group", WorkspaceID: &workspaceID, Name: "Rejected group", Color: "blue"}},
				},
			})
			assertSyncStatusOK(t, recorder)
			response := decodeSyncPushResponse(t, recorder)
			rootReason := test.wantReason
			if test.state == 1 {
				rootReason = model.RejectionReasonWorkspaceDeleted
			}
			assertRejected(t, response, model.Rejected{ID: workspaceID, Reason: rootReason, Type: "workspace"})
			assertRejected(t, response, model.Rejected{ID: collectionID, Reason: test.wantReason, Type: "collection", ParentID: workspaceID, ParentType: "workspace"})
			assertRejected(t, response, model.Rejected{ID: workspaceID + "-bookmark", Reason: test.wantReason, Type: "bookmark", ParentID: collectionID, ParentType: "collection"})
			assertRejected(t, response, model.Rejected{ID: workspaceID + "-group", Reason: test.wantReason, Type: "saved_group", ParentID: workspaceID, ParentType: "workspace"})
			if len(response.Rejected) != 4 {
				t.Fatalf("rejected = %#v, want four lifecycle rejections", response.Rejected)
			}
		})
	}
}

func TestSyncPushV2WorkspacePurgeIsIdempotent(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-v2-purge@example.com", password: "password123"})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "v2-purge", 1)

	request := model.SyncPushRequest{
		ProtocolVersion: 2,
		Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{{
			ID: "v2-purge", LifecycleAction: model.WorkspaceLifecyclePurge,
		}}},
	}
	firstRecorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), request)
	assertSyncStatusOK(t, firstRecorder)
	if rejected := decodeSyncPushResponse(t, firstRecorder).Rejected; len(rejected) != 0 {
		t.Fatalf("first purge rejected = %#v, want none", rejected)
	}
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "v2-purge", workspaceLifecycleDescendantCounts{})

	secondRecorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), request)
	assertSyncStatusOK(t, secondRecorder)
	if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
		t.Fatalf("repeated purge rejected = %#v, want idempotent success", rejected)
	}
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "v2-purge", workspaceLifecycleDescendantCounts{})
}

func TestSyncPullWorkspaceLifecycleStatesRetainDescendantsAndCapability(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-pull-lifecycle@example.com", password: "password123"})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "pull-active", 0)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "pull-deleted", 1)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "pull-terminal", 2)
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections (id, user_id, workspace_id, name, icon, position, seq, is_deleted, created_at, updated_at)
		VALUES ('pull-retained-collection', $1, 'pull-deleted', 'Retained collection', '', 0, 41, 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert retained pull collection: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO bookmarks (id, user_id, collection_id, title, url, favicon_url, description, position, seq, is_trashed, created_at, updated_at)
		VALUES ('pull-retained-bookmark', $1, 'pull-retained-collection', 'Retained bookmark', 'https://retained.example.com', '', '', 0, 42, 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert retained pull bookmark: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO groups (id, user_id, workspace_id, name, color, seq, is_deleted, created_at, updated_at)
		VALUES ('pull-retained-group', $1, 'pull-deleted', 'Retained group', 'blue', 43, 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert retained pull group: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `INSERT INTO user_sync_seq (user_id, seq) VALUES ($1, 43)`, userID); err != nil {
		t.Fatalf("insert pull sequence: %v", err)
	}

	response := pullSync(t, testDB, userID, 0)
	if !response.Capabilities.WorkspaceParentTombstone {
		t.Fatal("workspace_parent_tombstone capability = false, want true")
	}
	states := map[string]int{}
	for _, workspace := range response.Entities.Workspaces {
		states[workspace.ID] = workspace.IsDeleted
	}
	if !reflect.DeepEqual(states, map[string]int{"pull-active": 0, "pull-deleted": 1, "pull-terminal": 2}) {
		t.Fatalf("workspace states = %#v", states)
	}
	if len(response.Entities.Collections) != 1 || response.Entities.Collections[0].ID != "pull-retained-collection" ||
		len(response.Entities.Bookmarks) != 1 || response.Entities.Bookmarks[0].ID != "pull-retained-bookmark" ||
		len(response.Entities.Groups) != 1 || response.Entities.Groups[0].ID != "pull-retained-group" {
		t.Fatalf("retained descendants missing: collections=%#v bookmarks=%#v groups=%#v",
			response.Entities.Collections, response.Entities.Bookmarks, response.Entities.Groups)
	}

	emptyResponse := pullSync(t, testDB, userID, response.ServerSeq)
	if !emptyResponse.Capabilities.WorkspaceParentTombstone {
		t.Fatal("empty delta workspace_parent_tombstone capability = false, want true")
	}
	if len(emptyResponse.Entities.Workspaces) != 0 || len(emptyResponse.Entities.Collections) != 0 ||
		len(emptyResponse.Entities.Bookmarks) != 0 || len(emptyResponse.Entities.Groups) != 0 {
		t.Fatalf("empty delta entities = %#v, want empty", emptyResponse.Entities)
	}
}

func TestSyncPush_LegacyWorkspaceDeleteCascade(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "sync-legacy-workspace-delete@example.com",
		password: "password123",
	})
	insertLegacyWorkspaceLifecycleFixture(t, testDB, userID, "sync-legacy-delete", 0, 1, 40)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "sync-legacy-delete-sibling", 0)
	deletedAt := int64(8000)

	recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		Entities: model.SyncPushEntities{
			Workspaces: []model.SyncWorkspaceMutation{{
				ID:        "sync-legacy-delete",
				Name:      "Ignored legacy metadata",
				Position:  99,
				DeletedAt: &deletedAt,
			}},
		},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	if len(response.Rejected) != 0 {
		t.Fatalf("legacy delete rejected = %#v, want none", response.Rejected)
	}

	var (
		rootSeq       int64
		isDeleted     int
		deletionModel int
		rootDeletedAt *int64
		name          string
		position      int
	)
	if err := testDB.QueryRow(t.Context(), `
		SELECT seq, is_deleted, deletion_model, deleted_at, name, position
		FROM workspaces WHERE id = 'sync-legacy-delete'`,
	).Scan(&rootSeq, &isDeleted, &deletionModel, &rootDeletedAt, &name, &position); err != nil {
		t.Fatalf("query legacy-deleted workspace: %v", err)
	}
	if rootSeq != response.ServerSeq || isDeleted != 1 || deletionModel != 0 || rootDeletedAt == nil ||
		name != "Workspace sync-legacy-delete" || position != 17 {
		t.Fatalf("legacy-deleted workspace = {seq:%d state:%d model:%d deletedAt:%v name:%q position:%d}, server seq=%d",
			rootSeq, isDeleted, deletionModel, rootDeletedAt, name, position, response.ServerSeq)
	}
	assertLegacyWorkspaceLifecycleState(t, testDB, "sync-legacy-delete", legacyWorkspaceLifecycleExpectation{
		activeCollection:           lifecycleRowState{state: 1, seq: response.ServerSeq, deletedAt: rootDeletedAt},
		archivedCollection:         lifecycleRowState{state: 1, seq: response.ServerSeq, deletedAt: rootDeletedAt},
		trashedCollection:          lifecycleRowState{state: 1, seq: 12, deletedAt: int64Ptr(7012)},
		activeBookmark:             lifecycleRowState{state: 1, seq: response.ServerSeq, deletedAt: rootDeletedAt},
		archivedBookmarkState:      lifecycleRowState{state: 1, seq: response.ServerSeq, deletedAt: rootDeletedAt},
		trashedBookmark:            lifecycleRowState{state: 1, seq: 13, deletedAt: int64Ptr(7013)},
		contradictoryBookmark:      lifecycleRowState{state: 1, seq: 40, deletedAt: int64Ptr(7090)},
		activeGroup:                lifecycleRowState{state: 1, seq: response.ServerSeq, deletedAt: rootDeletedAt},
		trashedGroup:               lifecycleRowState{state: 1, seq: 14, deletedAt: int64Ptr(7014)},
		archivedAt:                 int64Ptr(6032),
		archivedBookmarkIsArchived: true,
	})
}

func TestSyncPush_LegacyWorkspaceMetadataCannotRestoreParentTombstone(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "sync-legacy-workspace-metadata@example.com",
		password: "password123",
	})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "sync-parent-tombstone", 1)
	beforeRoot := workspaceLifecycleRootSnapshot(t, testDB, "sync-parent-tombstone")
	beforeDescendants := workspaceLifecycleDescendantSnapshot(t, testDB, "sync-parent-tombstone")

	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Workspaces: []model.SyncWorkspaceMutation{{
			ID:       "sync-parent-tombstone",
			Name:     "Must not restore",
			Position: 99,
		}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{
		ID: "sync-parent-tombstone", Reason: model.RejectionReasonWorkspaceDeleted, Type: "workspace",
	})
	if len(response.Rejected) != 1 {
		t.Fatalf("legacy metadata rejected = %#v, want only workspace_deleted", response.Rejected)
	}
	afterRoot := workspaceLifecycleRootSnapshot(t, testDB, "sync-parent-tombstone")
	afterDescendants := workspaceLifecycleDescendantSnapshot(t, testDB, "sync-parent-tombstone")
	if afterRoot != beforeRoot || !reflect.DeepEqual(afterDescendants, beforeDescendants) {
		t.Fatalf("legacy metadata restored or changed parent tombstone\nroot before=%s\nroot after=%s\ndescendants before=%#v\ndescendants after=%#v",
			beforeRoot, afterRoot, beforeDescendants, afterDescendants)
	}
}

func TestSyncPush_LegacyWorkspaceDuplicateDeleteWins(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "sync-legacy-workspace-duplicate-delete@example.com",
		password: "password123",
	})
	insertLegacyWorkspaceLifecycleFixture(t, testDB, userID, "sync-legacy-duplicate", 0, 1, 40)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "sync-legacy-duplicate-sibling", 0)
	deletedAt := int64(8000)

	recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		Entities: model.SyncPushEntities{
			Workspaces: []model.SyncWorkspaceMutation{
				{
					ID:        "sync-legacy-duplicate",
					Name:      "Delete payload",
					Position:  98,
					DeletedAt: &deletedAt,
				},
				{
					ID:       "sync-legacy-duplicate",
					Name:     "Must not replace tombstone",
					Position: 99,
				},
			},
		},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{
		ID: "sync-legacy-duplicate", Reason: model.RejectionReasonWorkspaceDeleted, Type: "workspace",
	})
	if len(response.Rejected) != 1 {
		t.Fatalf("duplicate legacy mutations rejected = %#v, want only workspace_deleted", response.Rejected)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "sync-legacy-duplicate", workspaceLifecycleRootExpectation{
		name:          "Workspace sync-legacy-duplicate",
		icon:          stringPtr("icon-sync-legacy-duplicate"),
		color:         stringPtr("color-sync-legacy-duplicate"),
		position:      17,
		seq:           response.ServerSeq,
		isDeleted:     1,
		deletionModel: 0,
		deletedAtSet:  true,
	})
}

func TestSyncPush_LegacyWorkspaceDuplicateDeleteWinsInReverseOrder(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "sync-legacy-workspace-reverse-duplicate@example.com",
		password: "password123",
	})
	insertLegacyWorkspaceLifecycleFixture(t, testDB, userID, "sync-legacy-reverse-duplicate", 0, 1, 40)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "sync-legacy-unrelated", 0)
	deletedAt := int64(8000)

	recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		Entities: model.SyncPushEntities{
			Workspaces: []model.SyncWorkspaceMutation{
				{
					ID:       "sync-legacy-reverse-duplicate",
					Name:     "Must not replace tombstone",
					Position: 99,
				},
				{
					ID:       "sync-legacy-unrelated",
					Name:     "Updated unrelated workspace",
					Position: 18,
				},
				{
					ID:        "sync-legacy-reverse-duplicate",
					Name:      "Delete payload",
					Position:  98,
					DeletedAt: &deletedAt,
				},
			},
		},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{
		ID: "sync-legacy-reverse-duplicate", Reason: model.RejectionReasonWorkspaceDeleted, Type: "workspace",
	})
	if len(response.Rejected) != 1 {
		t.Fatalf("reverse duplicate legacy mutations rejected = %#v, want only workspace_deleted", response.Rejected)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "sync-legacy-reverse-duplicate", workspaceLifecycleRootExpectation{
		name:          "Workspace sync-legacy-reverse-duplicate",
		icon:          stringPtr("icon-sync-legacy-reverse-duplicate"),
		color:         stringPtr("color-sync-legacy-reverse-duplicate"),
		position:      17,
		seq:           response.ServerSeq,
		isDeleted:     1,
		deletionModel: 0,
		deletedAtSet:  true,
	})
	assertWorkspaceLifecycleRoot(t, testDB, "sync-legacy-unrelated", workspaceLifecycleRootExpectation{
		name:          "Updated unrelated workspace",
		icon:          nil,
		color:         nil,
		position:      18,
		seq:           response.ServerSeq,
		isDeleted:     0,
		deletionModel: 1,
		deletedAtSet:  false,
	})
}

func TestSyncPush_LegacyWorkspaceRejectedDeleteAllowsMetadata(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "sync-legacy-workspace-rejected-delete@example.com",
		password: "password123",
	})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "sync-legacy-rejected-delete", 0)
	deletedAt := int64(8000)

	recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
		Entities: model.SyncPushEntities{
			Workspaces: []model.SyncWorkspaceMutation{
				{
					ID:        "sync-legacy-rejected-delete",
					Name:      "Rejected delete payload",
					Position:  98,
					DeletedAt: &deletedAt,
				},
				{
					ID:        "sync-legacy-rejected-delete",
					Name:      "Accepted metadata after rejected delete",
					Position:  99,
					UpdatedAt: 9000,
				},
			},
		},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{
		ID: "sync-legacy-rejected-delete", Reason: model.RejectionReasonLastActiveWorkspace, Type: "workspace",
	})
	if len(response.Rejected) != 1 {
		t.Fatalf("rejected delete plus metadata rejections = %#v, want only last_active_workspace", response.Rejected)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "sync-legacy-rejected-delete", workspaceLifecycleRootExpectation{
		name:          "Accepted metadata after rejected delete",
		icon:          nil,
		color:         nil,
		position:      99,
		seq:           response.ServerSeq,
		isDeleted:     0,
		deletionModel: 1,
		deletedAtSet:  false,
	})
}

func TestSyncPushRejectsChildrenOfQuotaRejectedWorkspace(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-quota@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `INSERT INTO workspaces (id, user_id, name, position, created_at, updated_at) VALUES ('existing-ws', $1, 'Existing', 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert existing workspace: %v", err)
	}

	guestWorkspaceID := "guest-ws"
	recorder := pushSync(t, testDB, userID, func() billing.Limits {
		limits := syncTestLimits()
		limits.MaxWorkspaces = 1
		return limits
	}(), model.SyncPushEntities{
		Workspaces: []model.SyncWorkspaceMutation{{ID: guestWorkspaceID, Name: "Guest", Position: 1}},
		Collections: []model.Collection{{
			ID: "guest-default", WorkspaceID: &guestWorkspaceID, Name: "Default", Position: 0,
		}},
		Groups: []model.Group{{
			ID: "guest-group", WorkspaceID: &guestWorkspaceID, Name: "Group", Color: "grey",
		}},
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	response := decodeSyncPushResponse(t, recorder)
	want := map[string]string{
		"guest-ws":      "quota_exceeded",
		"guest-default": "parent_rejected",
		"guest-group":   "parent_rejected",
	}
	for _, rejection := range response.Rejected {
		if want[rejection.ID] != rejection.Reason {
			t.Fatalf("unexpected rejection: %#v", rejection)
		}
		delete(want, rejection.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing rejections: %#v", want)
	}

	var collections int
	if err := testDB.QueryRow(t.Context(), `SELECT COUNT(*) FROM collections WHERE id = 'guest-default'`).Scan(&collections); err != nil {
		t.Fatalf("count guest collections: %v", err)
	}
	if collections != 0 {
		t.Fatalf("guest collection count = %d, want 0", collections)
	}
	var groups int
	if err := testDB.QueryRow(t.Context(), `SELECT COUNT(*) FROM groups WHERE id = 'guest-group'`).Scan(&groups); err != nil {
		t.Fatalf("count guest groups: %v", err)
	}
	if groups != 0 {
		t.Fatalf("guest group count = %d, want 0", groups)
	}
}

func TestSyncPushRejectsBookmarkWithAnotherUsersCollection(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-owner@example.com", password: "password123"})
	otherUserID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-other@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `INSERT INTO collections (id, user_id, name, position, created_at, updated_at) VALUES ('other-collection', $1, 'Other', 0, 1, 1)`, otherUserID); err != nil {
		t.Fatalf("insert other collection: %v", err)
	}

	collectionID := "other-collection"
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Bookmarks: []model.Bookmark{{ID: "invalid-bookmark", CollectionID: &collectionID, Title: "Invalid", URL: "https://example.com"}},
	})
	assertSyncStatusOK(t, recorder)
	assertRejected(t, decodeSyncPushResponse(t, recorder), model.Rejected{
		ID: "invalid-bookmark", Reason: "invalid_parent", Type: "bookmark", ParentID: collectionID, ParentType: "collection",
	})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM bookmarks WHERE id = 'invalid-bookmark'`, 0)
}

func TestSyncPushRejectsBookmarkWithQuotaRejectedCollection(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-collection-quota@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `INSERT INTO collections (id, user_id, name, position, created_at, updated_at) VALUES ('existing-collection', $1, 'Existing', 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert existing collection: %v", err)
	}

	collectionID := "rejected-collection"
	limits := syncTestLimits()
	limits.MaxCollections = 1
	recorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
		Collections: []model.Collection{{ID: collectionID, Name: "Rejected", Position: 1}},
		Bookmarks:   []model.Bookmark{{ID: "rejected-bookmark", CollectionID: &collectionID, Title: "Child", URL: "https://example.com"}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{ID: collectionID, Reason: "quota_exceeded", Type: "collection"})
	assertRejected(t, response, model.Rejected{ID: "rejected-bookmark", Reason: "parent_rejected", Type: "bookmark", ParentID: collectionID, ParentType: "collection"})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM bookmarks WHERE id = 'rejected-bookmark'`, 0)
}

func TestSyncPushAcceptsCollectionWithNewWorkspace(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-new-parent@example.com", password: "password123"})
	workspaceID := "new-workspace"
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Workspaces:  []model.SyncWorkspaceMutation{{ID: workspaceID, Name: "New", Position: 0}},
		Collections: []model.Collection{{ID: "new-collection", WorkspaceID: &workspaceID, Name: "Child", Position: 0}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	if len(response.Rejected) != 0 {
		t.Fatalf("rejected = %#v, want none", response.Rejected)
	}
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'new-collection'`, 1)
}

func TestSyncPushAcceptsChildOfStaleOwnedWorkspace(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-stale-parent@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `INSERT INTO workspaces (id, user_id, name, position, created_at, updated_at) VALUES ('stale-workspace', $1, 'Existing', 0, 1, 4102444800000)`, userID); err != nil {
		t.Fatalf("insert stale workspace: %v", err)
	}
	workspaceID := "stale-workspace"
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Workspaces:  []model.SyncWorkspaceMutation{{ID: workspaceID, Name: "Stale", Position: 0}},
		Collections: []model.Collection{{ID: "child-collection", WorkspaceID: &workspaceID, Name: "Child", Position: 0}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{ID: workspaceID, Reason: "stale", Type: "workspace"})
	if len(response.Rejected) != 1 {
		t.Fatalf("rejected = %#v, want only stale workspace", response.Rejected)
	}
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'child-collection'`, 1)
}

func TestSyncPushAcceptsBookmarkWithoutCollection(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-uncategorized@example.com", password: "password123"})
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Bookmarks: []model.Bookmark{{ID: "uncategorized", Title: "Uncategorized", URL: "https://example.com"}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	if len(response.Rejected) != 0 {
		t.Fatalf("rejected = %#v, want none", response.Rejected)
	}
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM bookmarks WHERE id = 'uncategorized' AND collection_id IS NULL`, 1)
}

func TestSyncPushRejectsSavedGroupWithoutWorkspace(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-group-parent@example.com", password: "password123"})
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncPushEntities{
		Groups: []model.Group{{ID: "no-workspace", Name: "Group", Color: "grey"}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{ID: "no-workspace", Reason: "invalid_parent", Type: "saved_group", ParentType: "workspace"})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM groups WHERE id = 'no-workspace'`, 0)
}

func TestCollectionQuotaCountsAllNonPermanentCollections(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-non-permanent-collections@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections (id, user_id, name, position, deleted_at, archived_at, is_deleted, created_at, updated_at)
		VALUES ('archived-collection', $1, 'Archived', 0, NULL, 1, 0, 1, 1),
		       ('trashed-collection', $1, 'Trashed', 1, 1, NULL, 1, 1, 1),
		       ('permanently-deleted-collection', $1, 'Permanent', 2, NULL, NULL, 2, 1, 1)`, userID); err != nil {
		t.Fatalf("insert collection lifecycle states: %v", err)
	}

	limits := syncTestLimits()
	limits.MaxCollections = 2
	gin.SetMode(gin.TestMode)
	createBody, err := json.Marshal(model.CollectionRequest{Name: "Active", Position: 2})
	if err != nil {
		t.Fatalf("marshal collection request: %v", err)
	}
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Request = httptest.NewRequest(http.MethodPost, "/collections", bytes.NewReader(createBody))
	createContext.Request.Header.Set("Content-Type", "application/json")
	createContext.Set(middleware.UserIDKey, userID)
	NewCollectionHandler(testDB, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: limits}).Create(createContext)
	if createRecorder.Code != http.StatusForbidden {
		t.Fatalf("create status = %d body=%s", createRecorder.Code, createRecorder.Body.String())
	}

	planRecorder := httptest.NewRecorder()
	planContext, _ := gin.CreateTestContext(planRecorder)
	planContext.Request = httptest.NewRequest(http.MethodGet, "/plan", nil)
	planContext.Set(middleware.UserIDKey, userID)
	NewBillingHandler(fixedLimitsProvider{limits: limits}, nil, testDB).GetPlan(planContext)
	if planRecorder.Code != http.StatusOK {
		t.Fatalf("plan status = %d body=%s", planRecorder.Code, planRecorder.Body.String())
	}
	var plan planResponse
	if err := json.NewDecoder(planRecorder.Body).Decode(&plan); err != nil {
		t.Fatalf("decode plan response: %v", err)
	}
	if plan.Usage.Collections != 2 {
		t.Fatalf("collection usage = %d, want 2", plan.Usage.Collections)
	}

	recorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
		Collections: []model.Collection{{ID: "archived-collection", Name: "Restored", Position: 0}},
	})
	assertSyncStatusOK(t, recorder)
	if rejected := decodeSyncPushResponse(t, recorder).Rejected; len(rejected) != 0 {
		t.Fatalf("restore rejected = %#v, want none", rejected)
	}
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'archived-collection' AND archived_at IS NULL`, 1)
}

func TestSyncPushCollectionQuotaCountsAllNonPermanentCollections(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-push-non-permanent-collections@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections (id, user_id, name, position, deleted_at, archived_at, is_deleted, created_at, updated_at)
		VALUES ('sync-archived-collection', $1, 'Archived', 0, NULL, 1, 0, 1, 1),
		       ('sync-trashed-collection', $1, 'Trashed', 1, 1, NULL, 1, 1, 1),
		       ('sync-permanently-deleted-collection', $1, 'Permanent', 2, NULL, NULL, 2, 1, 1)`, userID); err != nil {
		t.Fatalf("insert collection lifecycle states: %v", err)
	}

	limits := syncTestLimits()
	limits.MaxCollections = 2
	firstRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
		Collections: []model.Collection{{ID: "sync-first-active", Name: "First", Position: 2}},
	})
	assertSyncStatusOK(t, firstRecorder)
	assertRejected(t, decodeSyncPushResponse(t, firstRecorder), model.Rejected{
		ID: "sync-first-active", Reason: "quota_exceeded", Type: "collection",
	})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'sync-first-active'`, 0)

	secondRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
		Collections: []model.Collection{{ID: "sync-archived-collection", Name: "Restored", Position: 0}},
	})
	assertSyncStatusOK(t, secondRecorder)
	if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
		t.Fatalf("restore rejected = %#v, want none", rejected)
	}
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'sync-archived-collection' AND archived_at IS NULL`, 1)
}

func TestSyncPushRetainedResourcesQuotaOnlyReleasesTerminalRecords(t *testing.T) {
	t.Run("workspaces", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-workspace-retained-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "quota-workspace-retained", 1)
		insertWorkspaceLifecycleRoot(t, testDB, userID, "quota-workspace-terminal", 2)
		limits := syncTestLimits()
		limits.MaxWorkspaces = 1

		firstRecorder := pushSyncRequest(t, testDB, userID, limits, model.SyncPushRequest{
			ProtocolVersion: 2,
			Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{{
				ID: "quota-workspace-first", Name: "First", Position: 1,
			}}},
		})
		assertSyncStatusOK(t, firstRecorder)
		assertRejected(t, decodeSyncPushResponse(t, firstRecorder), model.Rejected{
			ID: "quota-workspace-first", Reason: "quota_exceeded", Type: "workspace",
		})
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM workspaces WHERE id = 'quota-workspace-first'`, 0)

		if _, err := testDB.Exec(t.Context(), `UPDATE workspaces SET is_deleted = 2 WHERE id = 'quota-workspace-retained'`); err != nil {
			t.Fatalf("terminalize retained workspace: %v", err)
		}
		secondRecorder := pushSyncRequest(t, testDB, userID, limits, model.SyncPushRequest{
			ProtocolVersion: 2,
			Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{{
				ID: "quota-workspace-second", Name: "Second", Position: 2,
			}}},
		})
		assertSyncStatusOK(t, secondRecorder)
		if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
			t.Fatalf("workspace after terminal release rejected = %#v", rejected)
		}
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM workspaces WHERE id = 'quota-workspace-second'`, 1)
	})

	t.Run("collections", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-collection-retained-quota@example.com", password: "password123"})
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO collections (id, user_id, name, position, deleted_at, is_deleted, created_at, updated_at)
			VALUES ('quota-collection-retained', $1, 'Retained', 0, 1, 1, 1, 1),
			       ('quota-collection-terminal', $1, 'Terminal', 1, 1, 2, 1, 1)`, userID); err != nil {
			t.Fatalf("insert collection quota fixtures: %v", err)
		}
		limits := syncTestLimits()
		limits.MaxCollections = 1

		firstRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Collections: []model.Collection{{ID: "quota-collection-first", Name: "First"}},
		})
		assertSyncStatusOK(t, firstRecorder)
		assertRejected(t, decodeSyncPushResponse(t, firstRecorder), model.Rejected{
			ID: "quota-collection-first", Reason: "quota_exceeded", Type: "collection",
		})
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'quota-collection-first'`, 0)

		if _, err := testDB.Exec(t.Context(), `UPDATE collections SET is_deleted = 2 WHERE id = 'quota-collection-retained'`); err != nil {
			t.Fatalf("terminalize retained collection: %v", err)
		}
		secondRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Collections: []model.Collection{{ID: "quota-collection-second", Name: "Second"}},
		})
		assertSyncStatusOK(t, secondRecorder)
		if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
			t.Fatalf("collection after terminal release rejected = %#v", rejected)
		}
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'quota-collection-second'`, 1)
	})

	t.Run("bookmarks", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-bookmark-retained-quota@example.com", password: "password123"})
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO bookmarks (id, user_id, title, url, position, seq, deleted_at, is_trashed, created_at, updated_at)
			VALUES ('quota-bookmark-retained', $1, 'Retained', 'https://retained.example.com', 0, 1, 1, 1, 1, 1),
			       ('quota-bookmark-terminal', $1, 'Terminal', 'https://terminal.example.com', 1, 1, 1, 2, 1, 1)`, userID); err != nil {
			t.Fatalf("insert bookmark quota fixtures: %v", err)
		}
		limits := syncTestLimits()
		limits.MaxBookmarks = 1

		firstRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Bookmarks: []model.Bookmark{{ID: "quota-bookmark-first", Title: "First", URL: "https://first.example.com"}},
		})
		assertSyncStatusOK(t, firstRecorder)
		assertRejected(t, decodeSyncPushResponse(t, firstRecorder), model.Rejected{
			ID: "quota-bookmark-first", Reason: "quota_exceeded", Type: "bookmark",
		})
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM bookmarks WHERE id = 'quota-bookmark-first'`, 0)

		if _, err := testDB.Exec(t.Context(), `UPDATE bookmarks SET is_trashed = 2 WHERE id = 'quota-bookmark-retained'`); err != nil {
			t.Fatalf("terminalize retained bookmark: %v", err)
		}
		secondRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Bookmarks: []model.Bookmark{{ID: "quota-bookmark-second", Title: "Second", URL: "https://second.example.com"}},
		})
		assertSyncStatusOK(t, secondRecorder)
		if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
			t.Fatalf("bookmark after terminal release rejected = %#v", rejected)
		}
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM bookmarks WHERE id = 'quota-bookmark-second'`, 1)
	})

	t.Run("saved groups", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-group-retained-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "quota-group-workspace", 0)
		workspaceID := "quota-group-workspace"
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO groups (id, user_id, workspace_id, name, color, seq, deleted_at, is_deleted, created_at, updated_at)
			VALUES ('quota-group-retained', $1, $2, 'Retained', 'blue', 1, 1, 1, 1, 1),
			       ('quota-group-terminal', $1, $2, 'Terminal', 'red', 1, 1, 2, 1, 1)`, userID, workspaceID); err != nil {
			t.Fatalf("insert group quota fixtures: %v", err)
		}
		limits := syncTestLimits()
		limits.MaxSavedGroups = 1

		firstRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Groups: []model.Group{{ID: "quota-group-first", WorkspaceID: &workspaceID, Name: "First", Color: "blue"}},
		})
		assertSyncStatusOK(t, firstRecorder)
		assertRejected(t, decodeSyncPushResponse(t, firstRecorder), model.Rejected{
			ID: "quota-group-first", Reason: "quota_exceeded", Type: "saved_group",
		})
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM groups WHERE id = 'quota-group-first'`, 0)

		if _, err := testDB.Exec(t.Context(), `UPDATE groups SET is_deleted = 2 WHERE id = 'quota-group-retained'`); err != nil {
			t.Fatalf("terminalize retained group: %v", err)
		}
		secondRecorder := pushSync(t, testDB, userID, limits, model.SyncPushEntities{
			Groups: []model.Group{{ID: "quota-group-second", WorkspaceID: &workspaceID, Name: "Second", Color: "blue"}},
		})
		assertSyncStatusOK(t, secondRecorder)
		if rejected := decodeSyncPushResponse(t, secondRecorder).Rejected; len(rejected) != 0 {
			t.Fatalf("saved group after terminal release rejected = %#v", rejected)
		}
		assertEntityCount(t, testDB, `SELECT COUNT(*) FROM groups WHERE id = 'quota-group-second'`, 1)
	})
}

func assertSyncStatusOK(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func assertRejected(t *testing.T, response model.SyncPushResponse, want model.Rejected) {
	t.Helper()
	for _, got := range response.Rejected {
		if got == want {
			return
		}
	}
	t.Fatalf("missing rejection %#v in %#v", want, response.Rejected)
}

func assertEntityCount(t *testing.T, testDB *db.DB, query string, want int) {
	t.Helper()
	var got int
	if err := testDB.QueryRow(t.Context(), query).Scan(&got); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if got != want {
		t.Fatalf("entity count = %d, want %d", got, want)
	}
}
