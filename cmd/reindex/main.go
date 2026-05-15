// Command reindex drops the MeiliSearch bookmarks index and rebuilds it
// from the PostgreSQL bookmarks table. Useful when the index drifts out of
// sync (e.g. after a bulk import, schema change, or MeiliSearch data loss).
//
// Usage:
//
//	go run ./cmd/reindex          # reads .env in project root
//	go run ./cmd/reindex -batch 500
//
// Required env vars: DATABASE_URL, MEILISEARCH_HOST, MEILISEARCH_API_KEY.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/joho/godotenv"
	meilisearch "github.com/meilisearch/meilisearch-go"
	"github.com/tabslate/server/app"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/search"
)

func main() {
	batchSize := flag.Int("batch", 1000, "number of bookmarks per MeiliSearch batch")
	flag.Parse()

	_ = godotenv.Load()
	cfg := app.LoadConfig()

	if cfg.MeiliSearchHost == "" {
		log.Fatal("MEILISEARCH_HOST is not set")
	}

	// ── Connect to PostgreSQL ────────────────────────────────────────────────
	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer database.Close()
	log.Println("database connected")

	// ── Delete old MeiliSearch index ─────────────────────────────────────────
	svc := meilisearch.New(cfg.MeiliSearchHost, meilisearch.WithAPIKey(cfg.MeiliSearchAPIKey))
	log.Println("deleting index \"bookmarks\"...")

	task, err := svc.DeleteIndex("bookmarks")
	if err != nil {
		// 404 = index doesn't exist yet, that's fine.
		log.Printf("delete index (may not exist): %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := svc.WaitForTaskWithContext(ctx, task.TaskUID, 100*time.Millisecond); err != nil {
			log.Printf("wait for delete task: %v", err)
		}
		log.Println("old index deleted")
	}

	// ── Recreate index with settings ─────────────────────────────────────────
	// search.New handles CreateIndex + UpdateSettings + waits for readiness.
	sc := search.New(cfg.MeiliSearchHost, cfg.MeiliSearchAPIKey)
	if sc == nil {
		log.Fatal("failed to create search client")
	}
	log.Println("new index created with settings")

	// ── Stream bookmarks from DB and index in batches ────────────────────────
	ctx := context.Background()
	rows, err := database.Query(ctx,
		`SELECT id, user_id, title, url, COALESCE(description, ''),
		        COALESCE(collection_id, ''), is_archived
		   FROM bookmarks
		  WHERE is_trashed = 0 AND deleted_at IS NULL`)
	if err != nil {
		log.Fatalf("query bookmarks: %v", err)
	}
	defer rows.Close()

	batch := make([]search.BookmarkDoc, 0, *batchSize)
	total := 0
	indexed := 0

	for rows.Next() {
		var doc search.BookmarkDoc
		if err := rows.Scan(&doc.ID, &doc.UserID, &doc.Title, &doc.URL,
			&doc.Description, &doc.CollectionID, &doc.IsArchived); err != nil {
			log.Fatalf("scan row: %v", err)
		}
		batch = append(batch, doc)
		total++

		if len(batch) >= *batchSize {
			n, err := flushBatch(sc, batch)
			if err != nil {
				log.Fatalf("flush batch: %v", err)
			}
			indexed += n
			log.Printf("indexed %d / %d ...", indexed, total)
			batch = batch[:0]
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate rows: %v", err)
	}

	// Flush remaining
	if len(batch) > 0 {
		n, err := flushBatch(sc, batch)
		if err != nil {
			log.Fatalf("flush final batch: %v", err)
		}
		indexed += n
	}

	log.Printf("done — %d bookmarks indexed from %d total rows", indexed, total)
}

// flushBatch sends a batch of documents to MeiliSearch using the search.Client.
// It calls UpsertBookmark for each doc (fire-and-forget goroutines inside the
// client), but for bulk we need synchronous batching, so we use the underlying
// index directly through the exported BulkUpsert method.
func flushBatch(sc *search.Client, docs []search.BookmarkDoc) (int, error) {
	if err := sc.BulkUpsert(docs); err != nil {
		return 0, fmt.Errorf("bulk upsert: %w", err)
	}
	return len(docs), nil
}
