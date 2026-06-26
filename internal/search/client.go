package search

import (
	"context"
	"fmt"
	"log"
	"time"

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

// initIndex creates the index and waits for it to be ready, then applies
// settings. Waiting is necessary because MeiliSearch index creation is async —
// without it, the first AddDocuments call races against index creation and gets
// a 404. If the index already exists the wait is skipped.
func (c *Client) initIndex() {
	task, err := c.svc.CreateIndex(&meilisearch.IndexConfig{
		Uid:        indexName,
		PrimaryKey: "id",
	})
	if err != nil {
		// Index likely already exists — that's fine.
		log.Printf("[search] createIndex (may already exist): %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := c.svc.WaitForTaskWithContext(ctx, task.TaskUID, 50*time.Millisecond); err != nil {
			log.Printf("[search] wait for createIndex task: %v", err)
		}
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

// BulkUpsertAsync adds or updates multiple documents in a single HTTP call.
// Fire-and-forget goroutine — avoids the N-goroutine / N-connection storm that
// arises when UpsertBookmark is called in a loop for large imports.
func (c *Client) BulkUpsertAsync(docs []BookmarkDoc) {
	if c == nil || len(docs) == 0 {
		return
	}
	go func() {
		pk := "id"
		if _, err := c.index.AddDocuments(docs, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
			log.Printf("[search] bulkUpsertBookmarks %d docs: %v", len(docs), err)
		}
	}()
}

// BulkDeleteAsync removes multiple documents from the index in a single HTTP call.
// Fire-and-forget goroutine.
func (c *Client) BulkDeleteAsync(ids []string) {
	if c == nil || len(ids) == 0 {
		return
	}
	go func() {
		if _, err := c.index.DeleteDocuments(ids, nil); err != nil {
			log.Printf("[search] bulkDeleteBookmarks %d docs: %v", len(ids), err)
		}
	}()
}

// BulkUpsert adds or updates multiple documents synchronously in a single call.
// Used by the reindex CLI tool for efficient batch indexing.
func (c *Client) BulkUpsert(docs []BookmarkDoc) error {
	if c == nil || len(docs) == 0 {
		return nil
	}
	pk := "id"
	_, err := c.index.AddDocuments(docs, &meilisearch.DocumentOptions{PrimaryKey: &pk})
	if err != nil {
		return fmt.Errorf("meilisearch addDocuments: %w", err)
	}
	return nil
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

// DeleteUserDocumentsAsync removes all indexed bookmarks belonging to a user.
// Fire-and-forget goroutine. Nil-safe (no-op when search is disabled).
func (c *Client) DeleteUserDocumentsAsync(userID string) {
	if c == nil {
		return
	}
	go func() {
		filter := fmt.Sprintf(`userId = "%s"`, userID)
		if _, err := c.index.DeleteDocumentsByFilterWithContext(context.Background(), filter, nil); err != nil {
			log.Printf("[search] deleteUserDocuments %s: %v", userID, err)
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

// Ping checks MeiliSearch connectivity. Returns nil when client is nil (not configured).
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if _, err := c.svc.HealthWithContext(ctx); err != nil {
		return fmt.Errorf("meilisearch ping: %w", err)
	}
	return nil
}
