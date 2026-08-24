package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/gin-gonic/gin"
)

func TestWorkspaceRoutes_Lifecycle(t *testing.T) {
	t.Run("workspace deleted maps to conflict", func(t *testing.T) {
		if status := workspaceLifecycleStatus(model.RejectionReasonWorkspaceDeleted); status != http.StatusConflict {
			t.Fatalf("status = %d, want 409", status)
		}
	})

	t.Run("soft delete maps the last active rejection to conflict", func(t *testing.T) {
		testDB, userID, handler := newWorkspaceRouteTest(t, "workspace-route-last-active@example.com")
		insertWorkspaceLifecycleRoot(t, testDB, userID, "last-active", 0)

		recorder := performWorkspaceRoute(t, userID, http.MethodDelete, "/api/workspaces/:id", "/api/workspaces/last-active", handler.Delete)

		assertWorkspaceRouteRejection(t, recorder, http.StatusConflict, "last-active", model.RejectionReasonLastActiveWorkspace)
	})

	t.Run("restore exposes the retained root", func(t *testing.T) {
		testDB, userID, handler := newWorkspaceRouteTest(t, "workspace-route-restore@example.com")
		insertWorkspaceLifecycleRoot(t, testDB, userID, "restore-target", 1)

		recorder := performWorkspaceRoute(
			t,
			userID,
			http.MethodPost,
			"/api/workspaces/:id/restore",
			"/api/workspaces/restore-target/restore",
			handler.Restore,
		)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
		assertWorkspaceDeletionState(t, testDB, "restore-target", 0)
	})

	t.Run("permanent delete does not report success when the service rejects it", func(t *testing.T) {
		testDB, userID, handler := newWorkspaceRouteTest(t, "workspace-route-rejected-purge@example.com")
		insertWorkspaceLifecycleFixture(t, testDB, userID, "active-target", 0)
		insertWorkspaceLifecycleRoot(t, testDB, userID, "active-sibling", 0)

		recorder := performWorkspaceRoute(
			t,
			userID,
			http.MethodDelete,
			"/api/workspaces/:id/permanent",
			"/api/workspaces/active-target/permanent",
			handler.PermanentlyDelete,
		)

		assertWorkspaceRouteRejection(t, recorder, http.StatusNotFound, "active-target", "stale")
		assertWorkspaceDeletionState(t, testDB, "active-target", 0)
		assertWorkspaceLifecycleDescendantCounts(t, testDB, "active-target", workspaceLifecycleDescendantCounts{
			collections: 1,
			bookmarks:   3,
			groups:      1,
			groupTabs:   1,
		})
	})

	t.Run("permanent delete purges a retained aggregate", func(t *testing.T) {
		testDB, userID, handler := newWorkspaceRouteTest(t, "workspace-route-purge@example.com")
		insertWorkspaceLifecycleFixture(t, testDB, userID, "purge-target", 1)

		recorder := performWorkspaceRoute(
			t,
			userID,
			http.MethodDelete,
			"/api/workspaces/:id/permanent",
			"/api/workspaces/purge-target/permanent",
			handler.PermanentlyDelete,
		)

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d body=%s, want 204", recorder.Code, recorder.Body.String())
		}
		assertWorkspaceDeletionState(t, testDB, "purge-target", 2)
		assertWorkspaceLifecycleDescendantCounts(t, testDB, "purge-target", workspaceLifecycleDescendantCounts{})
	})

	t.Run("restore maps terminal and missing roots to stable statuses", func(t *testing.T) {
		testDB, userID, handler := newWorkspaceRouteTest(t, "workspace-route-errors@example.com")
		insertWorkspaceLifecycleRoot(t, testDB, userID, "terminal-target", 2)

		terminalRecorder := performWorkspaceRoute(
			t,
			userID,
			http.MethodPost,
			"/api/workspaces/:id/restore",
			"/api/workspaces/terminal-target/restore",
			handler.Restore,
		)
		assertWorkspaceRouteRejection(t, terminalRecorder, http.StatusGone, "terminal-target", model.RejectionReasonPermanentlyDeleted)

		missingRecorder := performWorkspaceRoute(
			t,
			userID,
			http.MethodPost,
			"/api/workspaces/:id/restore",
			"/api/workspaces/missing-target/restore",
			handler.Restore,
		)
		assertWorkspaceRouteRejection(t, missingRecorder, http.StatusNotFound, "missing-target", "stale")
	})
}

func TestRetainedWorkspaceVisibility(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "retained-workspace-visibility@example.com",
		password: "password123",
	})
	insertWorkspaceVisibilityFixture(t, testDB, userID)

	hub := pubsub.NewInMemoryHub()
	limits := fixedLimitsProvider{limits: syncTestLimits()}
	workspaceHandler := NewWorkspaceHandler(testDB, hub, limits, NewWorkspaceLifecycleService(testDB, hub, nil))
	collectionHandler := NewCollectionHandler(testDB, hub, limits)
	bookmarkHandler := NewBookmarkHandler(testDB, nil, hub, limits)

	t.Run("workspace list returns only state zero", func(t *testing.T) {
		recorder := performHandlerRequest(t, userID, http.MethodGet, "/workspaces", nil, workspaceHandler.List)
		var workspaces []model.Workspace
		decodeHandlerResponse(t, recorder, http.StatusOK, &workspaces)
		if len(workspaces) != 1 || workspaces[0].ID != "active-workspace" {
			t.Fatalf("workspaces = %#v, want only active-workspace", workspaces)
		}
	})

	t.Run("workspace update hides a retained root", func(t *testing.T) {
		body := []byte(`{"name":"Changed retained workspace","position":3}`)
		recorder := performHandlerRequestWithParam(t, userID, http.MethodPut, "/workspaces/retained-workspace", body, "id", "retained-workspace", workspaceHandler.Update)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
		}
		assertStoredName(t, testDB, "workspaces", "retained-workspace", "Retained workspace")
	})

	t.Run("collection list hides retained workspace descendants", func(t *testing.T) {
		recorder := performHandlerRequest(t, userID, http.MethodGet, "/collections", nil, collectionHandler.List)
		var collections []model.Collection
		decodeHandlerResponse(t, recorder, http.StatusOK, &collections)
		if len(collections) != 1 || collections[0].ID != "active-collection" {
			t.Fatalf("collections = %#v, want only active-collection", collections)
		}

		filteredRecorder := performHandlerRequest(t, userID, http.MethodGet, "/collections?workspace_id=retained-workspace", nil, collectionHandler.List)
		var filtered []model.Collection
		decodeHandlerResponse(t, filteredRecorder, http.StatusOK, &filtered)
		if len(filtered) != 0 {
			t.Fatalf("retained workspace collections = %#v, want empty", filtered)
		}
	})

	t.Run("collection create rejects a retained workspace", func(t *testing.T) {
		body := []byte(`{"workspace_id":"retained-workspace","name":"Rejected collection","position":2}`)
		recorder := performHandlerRequest(t, userID, http.MethodPost, "/collections", body, collectionHandler.Create)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
		}
		assertRowMissing(t, testDB, "collections", "Rejected collection")
	})

	t.Run("collection update hides a retained workspace descendant", func(t *testing.T) {
		body := []byte(`{"workspace_id":"retained-workspace","name":"Changed retained collection","position":3}`)
		recorder := performHandlerRequestWithParam(t, userID, http.MethodPut, "/collections/retained-collection", body, "id", "retained-collection", collectionHandler.Update)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
		}
		assertStoredName(t, testDB, "collections", "retained-collection", "Retained collection")
	})

	t.Run("bookmark list hides retained workspace descendants", func(t *testing.T) {
		recorder := performHandlerRequest(t, userID, http.MethodGet, "/bookmarks", nil, bookmarkHandler.List)
		var bookmarks []model.Bookmark
		decodeHandlerResponse(t, recorder, http.StatusOK, &bookmarks)
		if len(bookmarks) != 2 || bookmarks[0].ID != "active-bookmark-first" || bookmarks[1].ID != "active-bookmark-second" {
			t.Fatalf("bookmarks = %#v, want only ordered active workspace bookmarks", bookmarks)
		}
	})

	t.Run("bookmark create rejects a retained workspace collection", func(t *testing.T) {
		body := []byte(`{"collection_id":"retained-collection","title":"Rejected bookmark","url":"https://rejected.example.com","position":3}`)
		recorder := performHandlerRequest(t, userID, http.MethodPost, "/bookmarks", body, bookmarkHandler.Create)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
		}
		assertRowMissing(t, testDB, "bookmarks", "Rejected bookmark")
	})

	t.Run("bookmark update hides a retained workspace descendant", func(t *testing.T) {
		body := []byte(`{"collection_id":"retained-collection","title":"Changed retained bookmark","url":"https://retained.example.com","position":4}`)
		recorder := performHandlerRequestWithParam(t, userID, http.MethodPut, "/bookmarks/retained-bookmark", body, "id", "retained-bookmark", bookmarkHandler.Update)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
		}
		assertStoredName(t, testDB, "bookmarks", "retained-bookmark", "Retained bookmark")
	})
}

func TestSearchFiltersRetainedWorkspace(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "retained-workspace-search@example.com",
		password: "password123",
	})
	insertWorkspaceVisibilityFixture(t, testDB, userID)

	searcher := fixedBookmarkSearcher{documents: []search.BookmarkDoc{
		{ID: "retained-bookmark", Title: "Retained"},
		{ID: "active-bookmark-second", Title: "Second"},
		{ID: "trashed-bookmark", Title: "Trashed"},
		{ID: "active-bookmark-first", Title: "First"},
	}}
	handler := NewSearchHandler(testDB, searcher)
	recorder := performHandlerRequest(t, userID, http.MethodGet, "/search?q=bookmark", nil, handler.Search)

	var response struct {
		Bookmarks []search.BookmarkDoc `json:"bookmarks"`
	}
	decodeHandlerResponse(t, recorder, http.StatusOK, &response)
	if len(response.Bookmarks) != 2 || response.Bookmarks[0].ID != "active-bookmark-second" || response.Bookmarks[1].ID != "active-bookmark-first" {
		t.Fatalf("bookmarks = %#v, want visible candidates in MeiliSearch order", response.Bookmarks)
	}
}

type fixedBookmarkSearcher struct {
	documents []search.BookmarkDoc
}

func (s fixedBookmarkSearcher) SearchBookmarks(string, string) ([]search.BookmarkDoc, error) {
	return s.documents, nil
}

func newWorkspaceRouteTest(t *testing.T, email string) (*db.DB, string, *WorkspaceHandler) {
	t.Helper()
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{email: email, password: "password123"})
	hub := pubsub.NewInMemoryHub()
	lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)
	return testDB, userID, NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: syncTestLimits()}, lifecycle)
}

func performWorkspaceRoute(
	t *testing.T,
	userID string,
	method string,
	pattern string,
	path string,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.UserIDKey, userID)
		c.Next()
	})
	router.Handle(method, pattern, handler)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

func performHandlerRequest(
	t *testing.T,
	userID string,
	method string,
	path string,
	body []byte,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	handler(ginContext)
	return recorder
}

func performHandlerRequestWithParam(
	t *testing.T,
	userID string,
	method string,
	path string,
	body []byte,
	paramKey string,
	paramValue string,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	ginContext.Params = gin.Params{{Key: paramKey, Value: paramValue}}
	handler(ginContext)
	return recorder
}

func decodeHandlerResponse(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, destination interface{}) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), wantStatus)
	}
	if err := json.NewDecoder(recorder.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
}

func assertWorkspaceRouteRejection(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantID string, wantReason string) {
	t.Helper()
	var rejection model.Rejected
	decodeHandlerResponse(t, recorder, wantStatus, &rejection)
	if rejection.ID != wantID || rejection.Reason != wantReason || rejection.Type != "workspace" {
		t.Fatalf("rejection = %#v, want workspace %s reason %s", rejection, wantID, wantReason)
	}
}

func assertWorkspaceDeletionState(t *testing.T, testDB *db.DB, workspaceID string, want int) {
	t.Helper()
	var state int
	if err := testDB.QueryRow(t.Context(), `SELECT is_deleted FROM workspaces WHERE id = $1`, workspaceID).Scan(&state); err != nil {
		t.Fatalf("query workspace state: %v", err)
	}
	if state != want {
		t.Fatalf("workspace %s state = %d, want %d", workspaceID, state, want)
	}
}

func insertWorkspaceVisibilityFixture(t *testing.T, testDB *db.DB, userID string) {
	t.Helper()
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO workspaces
			(id, user_id, name, icon, color, position, seq, deleted_at, is_deleted, deletion_model, created_at, updated_at)
		VALUES
			('active-workspace', $1, 'Active workspace', 'active-icon', 'blue', 1, 1, NULL, 0, 1, 1, 1),
			('retained-workspace', $1, 'Retained workspace', 'retained-icon', 'red', 2, 2, NULL, 1, 1, 1, 1)`, userID); err != nil {
		t.Fatalf("insert visibility workspaces: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections
			(id, user_id, workspace_id, name, icon, position, seq, is_deleted, created_at, updated_at)
		VALUES
			('active-collection', $1, 'active-workspace', 'Active collection', 'active-icon', 1, 1, 0, 1, 1),
			('retained-collection', $1, 'retained-workspace', 'Retained collection', 'retained-icon', 1, 1, 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert visibility collections: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO bookmarks
			(id, user_id, collection_id, title, url, favicon_url, description, is_trashed, position, seq, created_at, updated_at)
		VALUES
			('active-bookmark-first', $1, 'active-collection', 'First bookmark', 'https://first.example.com', 'first-icon', 'First description', 0, 1, 1, 1, 1),
			('active-bookmark-second', $1, 'active-collection', 'Second bookmark', 'https://second.example.com', 'second-icon', 'Second description', 0, 2, 1, 1, 1),
			('retained-bookmark', $1, 'retained-collection', 'Retained bookmark', 'https://retained.example.com', 'retained-icon', 'Retained description', 0, 1, 1, 1, 1),
			('trashed-bookmark', $1, 'active-collection', 'Trashed bookmark', 'https://trashed.example.com', 'trashed-icon', 'Trashed description', 1, 3, 1, 1, 1)`, userID); err != nil {
		t.Fatalf("insert visibility bookmarks: %v", err)
	}
}

func assertRowMissing(t *testing.T, testDB *db.DB, table string, name string) {
	t.Helper()
	var count int
	query := `SELECT COUNT(*) FROM ` + table + ` WHERE name = $1`
	if table == "bookmarks" {
		query = `SELECT COUNT(*) FROM bookmarks WHERE title = $1`
	}
	if err := testDB.QueryRow(t.Context(), query, name).Scan(&count); err != nil {
		t.Fatalf("query %s row: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("%s row %q exists", table, name)
	}
}

func assertStoredName(t *testing.T, testDB *db.DB, table string, id string, want string) {
	t.Helper()
	column := "name"
	if table == "bookmarks" {
		column = "title"
	}
	var got string
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE id = $1`
	if err := testDB.QueryRow(t.Context(), query, id).Scan(&got); err != nil {
		t.Fatalf("query %s name: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s %s name = %q, want %q", table, id, got, want)
	}
}

var _ interface {
	SearchBookmarks(string, string) ([]search.BookmarkDoc, error)
} = fixedBookmarkSearcher{}
