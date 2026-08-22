package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	entities model.SyncEntities,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(model.SyncPushRequest{Entities: entities})
	if err != nil {
		t.Fatalf("marshal sync request: %v", err)
	}
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	handler := NewSyncHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: limits})
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
	}(), model.SyncEntities{
		Workspaces: []model.Workspace{{ID: guestWorkspaceID, Name: "Guest", Position: 1}},
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
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncEntities{
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
	recorder := pushSync(t, testDB, userID, limits, model.SyncEntities{
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
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncEntities{
		Workspaces:  []model.Workspace{{ID: workspaceID, Name: "New", Position: 0}},
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
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncEntities{
		Workspaces:  []model.Workspace{{ID: workspaceID, Name: "Stale", Position: 0}},
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
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncEntities{
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
	recorder := pushSync(t, testDB, userID, syncTestLimits(), model.SyncEntities{
		Groups: []model.Group{{ID: "no-workspace", Name: "Group", Color: "grey"}},
	})
	assertSyncStatusOK(t, recorder)
	response := decodeSyncPushResponse(t, recorder)
	assertRejected(t, response, model.Rejected{ID: "no-workspace", Reason: "invalid_parent", Type: "saved_group", ParentType: "workspace"})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM groups WHERE id = 'no-workspace'`, 0)
}

func TestCollectionQuotaCountsOnlyActiveCollections(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "sync-active-collections@example.com", password: "password123"})
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections (id, user_id, name, position, deleted_at, archived_at, is_deleted, created_at, updated_at)
		VALUES ('archived-collection', $1, 'Archived', 0, NULL, 1, 0, 1, 1),
		       ('trashed-collection', $1, 'Trashed', 1, 1, NULL, 1, 1, 1)`, userID); err != nil {
		t.Fatalf("insert inactive collections: %v", err)
	}

	limits := syncTestLimits()
	limits.MaxCollections = 1
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
	if createRecorder.Code != http.StatusCreated {
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
	if plan.Usage.Collections != 1 {
		t.Fatalf("collection usage = %d, want 1", plan.Usage.Collections)
	}

	recorder := pushSync(t, testDB, userID, limits, model.SyncEntities{
		Collections: []model.Collection{{ID: "archived-collection", Name: "Restored", Position: 0}},
	})
	assertSyncStatusOK(t, recorder)
	assertRejected(t, decodeSyncPushResponse(t, recorder), model.Rejected{
		ID: "archived-collection", Reason: "quota_exceeded", Type: "collection",
	})
	assertEntityCount(t, testDB, `SELECT COUNT(*) FROM collections WHERE id = 'archived-collection' AND archived_at IS NOT NULL`, 1)
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
