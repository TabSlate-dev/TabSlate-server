package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/search"
)

type SearchHandler struct {
	search *search.Client
}

func NewSearchHandler(sc *search.Client) *SearchHandler {
	return &SearchHandler{search: sc}
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

	c.JSON(http.StatusOK, gin.H{"bookmarks": docs})
}
