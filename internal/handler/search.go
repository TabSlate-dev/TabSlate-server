package handler

import (
	"context"
	"net/http"

	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/gin-gonic/gin"
)

type bookmarkSearcher interface {
	SearchBookmarks(userID, query string) ([]search.BookmarkDoc, error)
}

type SearchHandler struct {
	db     *db.DB
	search bookmarkSearcher
}

func NewSearchHandler(d *db.DB, sc bookmarkSearcher) *SearchHandler {
	return &SearchHandler{db: d, search: sc}
}

// GET /search?q=<query>
func (h *SearchHandler) Search(c *gin.Context) {
	q := c.Query("q")
	if len([]rune(q)) < 2 {
		c.JSON(http.StatusOK, gin.H{"bookmarks": []search.BookmarkDoc{}})
		return
	}

	userID := middleware.UserID(c)
	docs, err := h.search.SearchBookmarks(userID, q)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}
	candidateIDs := make([]string, 0, len(docs))
	for _, document := range docs {
		candidateIDs = append(candidateIDs, document.ID)
	}
	visibleIDs, err := h.visibleBookmarkIDs(c.Request.Context(), userID, candidateIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
		return
	}
	visibleDocs := make([]search.BookmarkDoc, 0, len(docs))
	for _, document := range docs {
		if _, visible := visibleIDs[document.ID]; visible {
			visibleDocs = append(visibleDocs, document)
		}
	}

	c.JSON(http.StatusOK, gin.H{"bookmarks": visibleDocs})
}

func (h *SearchHandler) visibleBookmarkIDs(
	ctx context.Context,
	userID string,
	candidateIDs []string,
) (map[string]struct{}, error) {
	visibleIDs := make(map[string]struct{}, len(candidateIDs))
	if len(candidateIDs) == 0 {
		return visibleIDs, nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT b.id
		FROM bookmarks b
		JOIN collections c ON c.id=b.collection_id AND c.user_id=b.user_id
		JOIN workspaces w ON w.id=c.workspace_id AND w.user_id=c.user_id
		WHERE b.user_id=$1
		  AND b.id=ANY($2)
		  AND b.deleted_at IS NULL
		  AND b.is_trashed=0
		  AND c.deleted_at IS NULL
		  AND c.is_deleted=0
		  AND w.is_deleted=0`, userID, candidateIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookmarkID string
		if err := rows.Scan(&bookmarkID); err != nil {
			return nil, err
		}
		visibleIDs[bookmarkID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return visibleIDs, nil
}
