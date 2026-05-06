package search

import (
	"fmt"
	"log"

	meilisearch "github.com/meilisearch/meilisearch-go"
)

const indexName = "bookmarks"

// Client wraps a MeiliSearch index. All methods are nil-safe —
// if the client is nil (search disabled), they are no-ops.
type Client struct {
	svc   meilisearch.ServiceManager
	index meilisearch.IndexManager
}

// New creates a Client. Returns nil when host is empty (search disabled).
func New(host, apiKey string) *Client {
	if host == "" {
		return nil
	}
	svc := meilisearch.New(host, meilisearch.WithAPIKey(apiKey))
	c := &Client{
		svc:   svc,
		index: svc.Index(indexName),
	}
	c.initIndex()
	return c
}

// initIndex creates the index and applies settings. Both are async tasks on the
// MeiliSearch side — the settings (notably FilterableAttributes) may not be applied
// before the first search request arrives at cold start. Transient 500 errors on the
// first search after a fresh deployment are expected; they resolve once MeiliSearch
// processes the settings task (usually <1s).
func (c *Client) initIndex() {
	if _, err := c.svc.CreateIndex(&meilisearch.IndexConfig{
		Uid:        indexName,
		PrimaryKey: "id",
	}); err != nil {
		log.Printf("[search] createIndex (may already exist): %v", err)
	}
	if _, err := c.index.UpdateSettings(&meilisearch.Settings{
		FilterableAttributes: []string{"userId"},
		SearchableAttributes: []string{"title", "url", "description"},
	}); err != nil {
		log.Printf("[search] updateSettings: %v", err)
	}
}

// UpsertBookmark adds or updates a document in the index. Fire-and-forget goroutine.
func (c *Client) UpsertBookmark(doc BookmarkDoc) {
	if c == nil {
		return
	}
	go func() {
		pk := "id"
		if _, err := c.index.AddDocuments([]BookmarkDoc{doc}, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
			log.Printf("[search] upsertBookmark %s: %v", doc.ID, err)
		}
	}()
}

// DeleteBookmark removes a document from the index. Fire-and-forget goroutine.
func (c *Client) DeleteBookmark(id string) {
	if c == nil {
		return
	}
	go func() {
		if _, err := c.index.DeleteDocument(id, nil); err != nil {
			log.Printf("[search] deleteBookmark %s: %v", id, err)
		}
	}()
}

// SearchBookmarks queries the index filtered to a single user.
func (c *Client) SearchBookmarks(userID, query string) ([]BookmarkDoc, error) {
	if c == nil {
		return []BookmarkDoc{}, nil
	}
	res, err := c.index.Search(query, &meilisearch.SearchRequest{
		Filter:               fmt.Sprintf(`userId = "%s"`, userID),
		AttributesToRetrieve: []string{"id", "title", "url", "description", "collectionId", "isArchived"},
		Limit:                20,
	})
	if err != nil {
		return nil, fmt.Errorf("meilisearch search: %w", err)
	}
	var docs []BookmarkDoc
	if err := res.Hits.DecodeInto(&docs); err != nil {
		return nil, fmt.Errorf("decode hits: %w", err)
	}
	return docs, nil
}
