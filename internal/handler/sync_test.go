package handler

import (
	"bytes"
	"context"
	"encoding/json"
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
