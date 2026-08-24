package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/gin-gonic/gin"
)

func TestPlanUsage_EffectiveContainment(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "billing-usage@example.com",
		password: "password123",
	})

	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO workspaces
			(id, user_id, name, position, deleted_at, is_deleted, created_at, updated_at)
		VALUES
			('usage-workspace-active', $1, 'Active', 0, NULL, 0, 1, 1),
			('usage-workspace-trash', $1, 'Trash', 1, 1, 1, 1, 1),
			('usage-workspace-terminal', $1, '', 0, 1, 2, 1, 1)`, userID); err != nil {
		t.Fatalf("insert workspace usage fixtures: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections
			(id, user_id, workspace_id, name, position, deleted_at, archived_at, is_deleted, created_at, updated_at)
		VALUES
			('usage-collection-active', $1, 'usage-workspace-active', 'Active', 0, NULL, NULL, 0, 1, 1),
			('usage-collection-archived', $1, 'usage-workspace-active', 'Archived', 1, NULL, 1, 0, 1, 1),
			('usage-collection-trash', $1, 'usage-workspace-active', 'Trash', 2, 1, NULL, 1, 1, 1),
			('usage-collection-parent-trash', $1, 'usage-workspace-trash', 'Parent trash', 3, NULL, NULL, 0, 1, 1),
			('usage-collection-nested-trash', $1, 'usage-workspace-trash', 'Nested trash', 4, 1, NULL, 1, 1, 1),
			('usage-collection-terminal', $1, 'usage-workspace-active', 'Terminal', 5, 1, NULL, 2, 1, 1)`, userID); err != nil {
		t.Fatalf("insert collection usage fixtures: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO bookmarks
			(id, user_id, collection_id, title, url, is_archived, is_trashed, position, created_at, updated_at)
		VALUES
			('usage-bookmark-active', $1, 'usage-collection-active', 'Active', 'https://active.example.com', FALSE, 0, 0, 1, 1),
			('usage-bookmark-archived', $1, 'usage-collection-active', 'Archived', 'https://archived.example.com', TRUE, 0, 1, 1, 1),
			('usage-bookmark-trash', $1, 'usage-collection-active', 'Trash', 'https://trash.example.com', FALSE, 1, 2, 1, 1),
			('usage-bookmark-collection-trash', $1, 'usage-collection-trash', 'Collection trash', 'https://collection-trash.example.com', FALSE, 0, 3, 1, 1),
			('usage-bookmark-workspace-trash', $1, 'usage-collection-parent-trash', 'Workspace trash', 'https://workspace-trash.example.com', FALSE, 0, 4, 1, 1),
			('usage-bookmark-nested-trash', $1, 'usage-collection-trash', 'Nested trash', 'https://nested-trash.example.com', FALSE, 1, 5, 1, 1),
			('usage-bookmark-terminal', $1, 'usage-collection-active', 'Terminal', 'https://terminal.example.com', FALSE, 2, 6, 1, 1)`, userID); err != nil {
		t.Fatalf("insert bookmark usage fixtures: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO tags (id, user_id, name, deleted_at, updated_at)
		VALUES
			('usage-tag-active', $1, 'Active', NULL, 1),
			('usage-tag-deleted', $1, 'Deleted', 1, 1)`, userID); err != nil {
		t.Fatalf("insert tag usage fixtures: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO groups
			(id, user_id, workspace_id, name, color, deleted_at, is_deleted, created_at, updated_at)
		VALUES
			('usage-group-active', $1, 'usage-workspace-active', 'Active', 'blue', NULL, 0, 1, 1),
			('usage-group-trash', $1, 'usage-workspace-active', 'Trash', 'red', 1, 1, 1, 1),
			('usage-group-parent-trash', $1, 'usage-workspace-trash', 'Parent trash', 'green', NULL, 0, 1, 1),
			('usage-group-nested-trash', $1, 'usage-workspace-trash', 'Nested trash', 'yellow', 1, 1, 1, 1),
			('usage-group-terminal', $1, 'usage-workspace-active', 'Terminal', 'grey', 1, 2, 1, 1)`, userID); err != nil {
		t.Fatalf("insert group usage fixtures: %v", err)
	}

	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/api/plan", nil)
	ginContext.Set(middleware.UserIDKey, userID)
	NewBillingHandler(fixedLimitsProvider{limits: syncTestLimits()}, nil, testDB).GetPlan(ginContext)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Usage      planUsage `json:"usage"`
		TrashUsage planUsage `json:"trash_usage"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode plan response: %v", err)
	}

	wantUsage := planUsage{Workspaces: 2, Collections: 5, Bookmarks: 6, Tags: 1, SavedGroups: 4}
	wantTrashUsage := planUsage{Workspaces: 1, Collections: 3, Bookmarks: 4, Tags: 0, SavedGroups: 3}
	wantInUse := planUsage{Workspaces: 1, Collections: 2, Bookmarks: 2, Tags: 1, SavedGroups: 1}
	assertPlanUsage(t, "usage", response.Usage, wantUsage)
	assertPlanUsage(t, "trash usage", response.TrashUsage, wantTrashUsage)
	assertPlanUsage(t, "in-use breakdown", subtractPlanUsage(response.Usage, response.TrashUsage), wantInUse)
	assertPlanUsage(t, "usage partition", addPlanUsage(wantInUse, response.TrashUsage), response.Usage)
}

func TestCreationQuotaCountsRetainedRows(t *testing.T) {
	t.Run("workspace", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "workspace-rest-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "workspace-rest-retained", 1)
		insertWorkspaceLifecycleRoot(t, testDB, userID, "workspace-rest-terminal", 2)
		limits := syncTestLimits()
		limits.MaxWorkspaces = 1
		hub := pubsub.NewInMemoryHub()
		handler := NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: limits}, NewWorkspaceLifecycleService(testDB, hub, nil))

		assertCreateStatus(t, userID, `{"name":"Blocked"}`, handler.Create, http.StatusForbidden)
		if _, err := testDB.Exec(t.Context(), `UPDATE workspaces SET is_deleted=2 WHERE id='workspace-rest-retained'`); err != nil {
			t.Fatalf("terminalize retained workspace: %v", err)
		}
		assertCreateStatus(t, userID, `{"name":"Allowed"}`, handler.Create, http.StatusCreated)
	})

	t.Run("collection", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "collection-rest-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "collection-rest-workspace", 0)
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO collections (id, user_id, workspace_id, name, position, deleted_at, is_deleted, created_at, updated_at)
			VALUES ('collection-rest-retained', $1, 'collection-rest-workspace', 'Retained', 0, 1, 1, 1, 1),
			       ('collection-rest-terminal', $1, 'collection-rest-workspace', 'Terminal', 1, 1, 2, 1, 1)`, userID); err != nil {
			t.Fatalf("insert collection quota fixtures: %v", err)
		}
		limits := syncTestLimits()
		limits.MaxCollections = 1
		handler := NewCollectionHandler(testDB, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: limits})

		body := `{"workspace_id":"collection-rest-workspace","name":"Blocked"}`
		assertCreateStatus(t, userID, body, handler.Create, http.StatusForbidden)
		if _, err := testDB.Exec(t.Context(), `UPDATE collections SET is_deleted=2 WHERE id='collection-rest-retained'`); err != nil {
			t.Fatalf("terminalize retained collection: %v", err)
		}
		assertCreateStatus(t, userID, body, handler.Create, http.StatusCreated)
	})

	t.Run("bookmark", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "bookmark-rest-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "bookmark-rest-workspace", 0)
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO collections (id, user_id, workspace_id, name, position, created_at, updated_at)
			VALUES ('bookmark-rest-collection', $1, 'bookmark-rest-workspace', 'Active', 0, 1, 1)`, userID); err != nil {
			t.Fatalf("insert bookmark parent fixture: %v", err)
		}
		if _, err := testDB.Exec(t.Context(), `
			INSERT INTO bookmarks (id, user_id, collection_id, title, url, position, is_trashed, created_at, updated_at)
			VALUES ('bookmark-rest-retained', $1, 'bookmark-rest-collection', 'Retained', 'https://retained.example.com', 0, 1, 1, 1),
			       ('bookmark-rest-terminal', $1, 'bookmark-rest-collection', 'Terminal', 'https://terminal.example.com', 1, 2, 1, 1)`, userID); err != nil {
			t.Fatalf("insert bookmark quota fixtures: %v", err)
		}
		limits := syncTestLimits()
		limits.MaxBookmarks = 1
		handler := NewBookmarkHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: limits})

		body := `{"collection_id":"bookmark-rest-collection","title":"New","url":"https://new.example.com","is_trashed":true}`
		assertCreateStatus(t, userID, body, handler.Create, http.StatusForbidden)
		if _, err := testDB.Exec(t.Context(), `UPDATE bookmarks SET is_trashed=2 WHERE id='bookmark-rest-retained'`); err != nil {
			t.Fatalf("terminalize retained bookmark: %v", err)
		}
		assertCreateStatus(t, userID, body, handler.Create, http.StatusCreated)
	})

	t.Run("workspace restore does not capacity check", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: "workspace-restore-quota@example.com", password: "password123"})
		insertWorkspaceLifecycleRoot(t, testDB, userID, "workspace-restore-retained", 1)
		limits := syncTestLimits()
		limits.MaxWorkspaces = 0
		hub := pubsub.NewInMemoryHub()
		handler := NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: limits}, NewWorkspaceLifecycleService(testDB, hub, nil))

		recorder := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(recorder)
		ginContext.Request = httptest.NewRequest(http.MethodPost, "/api/workspaces/workspace-restore-retained/restore", nil)
		ginContext.Params = gin.Params{{Key: "id", Value: "workspace-restore-retained"}}
		ginContext.Set(middleware.UserIDKey, userID)
		handler.Restore(ginContext)
		if recorder.Code != http.StatusOK {
			t.Fatalf("restore status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
	})
}

func assertCreateStatus(
	t *testing.T,
	userID string,
	body string,
	handler gin.HandlerFunc,
	wantStatus int,
) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	handler(ginContext)
	if recorder.Code != wantStatus {
		t.Fatalf("create status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), wantStatus)
	}
}

func assertPlanUsage(t *testing.T, label string, got, want planUsage) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v, want %#v", label, got, want)
	}
}

func subtractPlanUsage(total, trash planUsage) planUsage {
	return planUsage{
		Workspaces:  total.Workspaces - trash.Workspaces,
		Collections: total.Collections - trash.Collections,
		Bookmarks:   total.Bookmarks - trash.Bookmarks,
		Tags:        total.Tags - trash.Tags,
		SavedGroups: total.SavedGroups - trash.SavedGroups,
	}
}

func addPlanUsage(left, right planUsage) planUsage {
	return planUsage{
		Workspaces:  left.Workspaces + right.Workspaces,
		Collections: left.Collections + right.Collections,
		Bookmarks:   left.Bookmarks + right.Bookmarks,
		Tags:        left.Tags + right.Tags,
		SavedGroups: left.SavedGroups + right.SavedGroups,
	}
}
