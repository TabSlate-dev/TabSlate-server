package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/gin-gonic/gin"
)

// TestBookmarkCreateWithoutCollectionIsUncategorized covers a bug where
// POST /bookmarks with a nil or empty collection_id ("uncategorized") always
// returned 404 "collection not found": the INSERT ... SELECT FROM collections
// join compared c.id against a NULL/empty parameter, which never matches any
// row regardless of the request's validity.
func TestBookmarkCreateWithoutCollectionIsUncategorized(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "bookmark-create-uncategorized@example.com", password: "password123",
	})
	handler := NewBookmarkHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: syncTestLimits()})

	tests := []struct {
		name         string
		collectionID *string
	}{
		{name: "nil collection_id", collectionID: nil},
		{name: "empty collection_id", collectionID: stringPtr("")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(model.BookmarkRequest{
				CollectionID: test.collectionID,
				Title:        "Uncategorized bookmark",
				URL:          "https://example.com",
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/bookmarks", bytes.NewReader(body))
			ginContext.Request.Header.Set("Content-Type", "application/json")
			ginContext.Set(middleware.UserIDKey, userID)
			handler.Create(ginContext)

			if recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d body=%s, want 201", recorder.Code, recorder.Body.String())
			}
			var created model.Bookmark
			if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			var storedCollectionID *string
			if err := testDB.QueryRow(t.Context(),
				`SELECT collection_id FROM bookmarks WHERE id = $1`, created.ID,
			).Scan(&storedCollectionID); err != nil {
				t.Fatalf("query created bookmark: %v", err)
			}
			if storedCollectionID != nil {
				t.Fatalf("stored collection_id = %v, want NULL", *storedCollectionID)
			}
		})
	}
}

// TestBookmarkCreateWithMissingCollectionIsRejected is the control case: a
// non-empty but nonexistent collection_id must still 404, distinguishing
// "no collection" from "collection that doesn't exist/isn't owned".
func TestBookmarkCreateWithMissingCollectionIsRejected(t *testing.T) {
	testDB := openSyncTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "bookmark-create-missing-collection@example.com", password: "password123",
	})
	handler := NewBookmarkHandler(testDB, nil, pubsub.NewInMemoryHub(), fixedLimitsProvider{limits: syncTestLimits()})

	body, err := json.Marshal(model.BookmarkRequest{
		CollectionID: stringPtr("does-not-exist"),
		Title:        "Should be rejected",
		URL:          "https://example.com",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/bookmarks", bytes.NewReader(body))
	ginContext.Request.Header.Set("Content-Type", "application/json")
	ginContext.Set(middleware.UserIDKey, userID)
	handler.Create(ginContext)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
	}
}
