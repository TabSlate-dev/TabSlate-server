package search

// BookmarkDoc is a MeiliSearch document representing one bookmark.
type BookmarkDoc struct {
	ID           string `json:"id"`
	UserID       string `json:"userId"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Description  string `json:"description"`
	CollectionID string `json:"collectionId"`
	IsArchived   bool   `json:"isArchived"`
}
