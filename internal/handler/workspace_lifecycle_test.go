package handler

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
	"github.com/jackc/pgx/v5"
)

func TestWorkspaceLifecycle_DeleteParentOnly(t *testing.T) {
	testDB := openWorkspaceLifecycleTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "workspace-lifecycle-delete@example.com",
		password: "password123",
	})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "delete-target", 0)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "delete-sibling", 0)

	before := workspaceLifecycleDescendantSnapshot(t, testDB, "delete-target")
	hub := pubsub.NewInMemoryHub()
	_, broadcasts := hub.Subscribe(userID)
	service := NewWorkspaceLifecycleService(testDB, hub, nil)

	effect, rejection, err := service.Apply(
		t.Context(), userID, "delete-target", model.WorkspaceLifecycleDelete, 1,
	)
	if err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if rejection != nil {
		t.Fatalf("delete rejection = %#v", rejection)
	}
	if !effect.Changed || effect.Seq != 1 {
		t.Fatalf("delete effect = %#v, want changed seq 1", effect)
	}
	wantSearchDeletes := []string{"delete-target-bookmark-active", "delete-target-bookmark-archived", "delete-target-bookmark-trashed"}
	sort.Strings(effect.SearchDeletes)
	if !reflect.DeepEqual(effect.SearchDeletes, wantSearchDeletes) {
		t.Fatalf("search deletes = %#v, want %#v", effect.SearchDeletes, wantSearchDeletes)
	}
	if len(effect.SearchUpserts) != 0 {
		t.Fatalf("search upserts = %#v, want none", effect.SearchUpserts)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "delete-target", workspaceLifecycleRootExpectation{
		name:          "Workspace delete-target",
		icon:          stringPtr("icon-delete-target"),
		color:         stringPtr("color-delete-target"),
		position:      17,
		seq:           1,
		isDeleted:     1,
		deletionModel: 1,
		deletedAtSet:  true,
	})
	after := workspaceLifecycleDescendantSnapshot(t, testDB, "delete-target")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("descendants changed on parent-only delete\nbefore=%#v\nafter=%#v", before, after)
	}
	assertWorkspaceLifecycleBroadcast(t, broadcasts, 1)

	repeatEffect, repeatRejection, err := service.Apply(
		t.Context(), userID, "delete-target", model.WorkspaceLifecycleDelete, 1,
	)
	if err != nil {
		t.Fatalf("repeat delete workspace: %v", err)
	}
	if repeatRejection != nil {
		t.Fatalf("repeat delete rejection = %#v", repeatRejection)
	}
	if repeatEffect.Changed || repeatEffect.Seq != 1 || len(repeatEffect.SearchDeletes) != 0 || len(repeatEffect.SearchUpserts) != 0 {
		t.Fatalf("repeat delete effect = %#v, want unchanged seq 1 with no search work", repeatEffect)
	}
	assertNoWorkspaceLifecycleBroadcast(t, broadcasts)
	assertWorkspaceLifecycleUserSeq(t, testDB, userID, 1)
}

func TestWorkspaceLifecycle_RestoreParentOnly(t *testing.T) {
	testDB := openWorkspaceLifecycleTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "workspace-lifecycle-restore@example.com",
		password: "password123",
	})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "restore-target", 1)
	insertWorkspaceLifecycleRoot(t, testDB, userID, "restore-sibling", 0)

	before := workspaceLifecycleDescendantSnapshot(t, testDB, "restore-target")
	service := NewWorkspaceLifecycleService(testDB, pubsub.NewInMemoryHub(), nil)
	effect, rejection, err := service.Apply(
		t.Context(), userID, "restore-target", model.WorkspaceLifecycleRestore, 1,
	)
	if err != nil {
		t.Fatalf("restore workspace: %v", err)
	}
	if rejection != nil {
		t.Fatalf("restore rejection = %#v", rejection)
	}
	if !effect.Changed || effect.Seq != 1 {
		t.Fatalf("restore effect = %#v, want changed seq 1", effect)
	}
	if len(effect.SearchDeletes) != 0 {
		t.Fatalf("search deletes = %#v, want none", effect.SearchDeletes)
	}
	wantSearchUpserts := []search.BookmarkDoc{
		{
			ID:           "restore-target-bookmark-active",
			UserID:       userID,
			Title:        "Active bookmark",
			URL:          "https://active.example.com",
			Description:  "Active description",
			CollectionID: "restore-target-collection",
			IsArchived:   false,
		},
		{
			ID:           "restore-target-bookmark-archived",
			UserID:       userID,
			Title:        "Archived bookmark",
			URL:          "https://archived.example.com",
			Description:  "Archived description",
			CollectionID: "restore-target-collection",
			IsArchived:   true,
		},
	}
	sort.Slice(effect.SearchUpserts, func(i, j int) bool {
		return effect.SearchUpserts[i].ID < effect.SearchUpserts[j].ID
	})
	if !reflect.DeepEqual(effect.SearchUpserts, wantSearchUpserts) {
		t.Fatalf("search upserts = %#v, want %#v", effect.SearchUpserts, wantSearchUpserts)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "restore-target", workspaceLifecycleRootExpectation{
		name:          "Workspace restore-target",
		icon:          stringPtr("icon-restore-target"),
		color:         stringPtr("color-restore-target"),
		position:      17,
		seq:           1,
		isDeleted:     0,
		deletionModel: 1,
		deletedAtSet:  false,
	})
	after := workspaceLifecycleDescendantSnapshot(t, testDB, "restore-target")
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("descendants changed on parent-only restore\nbefore=%#v\nafter=%#v", before, after)
	}

	repeatEffect, repeatRejection, err := service.Apply(
		t.Context(), userID, "restore-target", model.WorkspaceLifecycleRestore, 1,
	)
	if err != nil {
		t.Fatalf("repeat restore workspace: %v", err)
	}
	if repeatRejection != nil {
		t.Fatalf("repeat restore rejection = %#v", repeatRejection)
	}
	if repeatEffect.Changed || repeatEffect.Seq != 1 || len(repeatEffect.SearchDeletes) != 0 || len(repeatEffect.SearchUpserts) != 0 {
		t.Fatalf("repeat restore effect = %#v, want unchanged seq 1 with no search work", repeatEffect)
	}

	insertWorkspaceLifecycleRoot(t, testDB, userID, "restore-terminal", 2)
	terminalEffect, terminalRejection, err := service.Apply(
		t.Context(), userID, "restore-terminal", model.WorkspaceLifecycleRestore, 1,
	)
	if err != nil {
		t.Fatalf("restore terminal workspace: %v", err)
	}
	if terminalRejection == nil || terminalRejection.Reason != model.RejectionReasonPermanentlyDeleted || terminalRejection.Type != "workspace" || terminalRejection.ID != "restore-terminal" {
		t.Fatalf("terminal restore rejection = %#v", terminalRejection)
	}
	if terminalEffect.Changed {
		t.Fatalf("terminal restore effect = %#v, want unchanged", terminalEffect)
	}
}

func TestWorkspaceLifecycle_RejectLastActive(t *testing.T) {
	testDB := openWorkspaceLifecycleTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "workspace-lifecycle-last-active@example.com",
		password: "password123",
	})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "last-active", 0)
	service := NewWorkspaceLifecycleService(testDB, pubsub.NewInMemoryHub(), nil)

	effect, rejection, err := service.Apply(
		t.Context(), userID, "last-active", model.WorkspaceLifecycleDelete, 1,
	)
	if err != nil {
		t.Fatalf("delete final active workspace: %v", err)
	}
	if rejection == nil || rejection.Reason != model.RejectionReasonLastActiveWorkspace || rejection.Type != "workspace" || rejection.ID != "last-active" {
		t.Fatalf("last active rejection = %#v", rejection)
	}
	if effect.Changed {
		t.Fatalf("last active effect = %#v, want unchanged", effect)
	}
	assertWorkspaceLifecycleRoot(t, testDB, "last-active", workspaceLifecycleRootExpectation{
		name:          "Workspace last-active",
		icon:          stringPtr("icon-last-active"),
		color:         stringPtr("color-last-active"),
		position:      17,
		seq:           40,
		isDeleted:     0,
		deletionModel: 1,
		deletedAtSet:  false,
	})
	assertWorkspaceLifecycleUserSeqMissing(t, testDB, userID)

	missingEffect, missingRejection, err := service.Apply(
		t.Context(), userID, "not-owned", model.WorkspaceLifecycleDelete, 1,
	)
	if err != nil {
		t.Fatalf("delete missing workspace: %v", err)
	}
	if missingRejection == nil || missingRejection.Reason != "stale" || missingRejection.Type != "workspace" || missingRejection.ID != "not-owned" {
		t.Fatalf("missing workspace rejection = %#v", missingRejection)
	}
	if missingEffect.Changed {
		t.Fatalf("missing workspace effect = %#v, want unchanged", missingEffect)
	}
}

func TestWorkspaceLifecycle_PurgeScrubsRoot(t *testing.T) {
	t.Run("purge retains one scrubbed terminal root and is idempotent", func(t *testing.T) {
		testDB := openWorkspaceLifecycleTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email:    "workspace-lifecycle-purge@example.com",
			password: "password123",
		})
		insertWorkspaceLifecycleFixture(t, testDB, userID, "purge-target", 1)
		service := NewWorkspaceLifecycleService(testDB, pubsub.NewInMemoryHub(), nil)

		effect, rejection, err := service.Apply(
			t.Context(), userID, "purge-target", model.WorkspaceLifecyclePurge, 1,
		)
		if err != nil {
			t.Fatalf("purge workspace: %v", err)
		}
		if rejection != nil {
			t.Fatalf("purge rejection = %#v", rejection)
		}
		if !effect.Changed || effect.Seq != 1 {
			t.Fatalf("purge effect = %#v, want changed seq 1", effect)
		}
		wantSearchDeletes := []string{"purge-target-bookmark-active", "purge-target-bookmark-archived", "purge-target-bookmark-trashed"}
		sort.Strings(effect.SearchDeletes)
		if !reflect.DeepEqual(effect.SearchDeletes, wantSearchDeletes) {
			t.Fatalf("purge search deletes = %#v, want %#v", effect.SearchDeletes, wantSearchDeletes)
		}
		assertWorkspaceLifecycleRoot(t, testDB, "purge-target", workspaceLifecycleRootExpectation{
			name:          "",
			icon:          nil,
			color:         nil,
			position:      0,
			seq:           1,
			isDeleted:     2,
			deletionModel: 1,
			deletedAtSet:  true,
		})
		assertWorkspaceLifecycleDescendantCounts(t, testDB, "purge-target", workspaceLifecycleDescendantCounts{})
		terminalBefore := workspaceLifecycleRootSnapshot(t, testDB, "purge-target")

		repeatEffect, repeatRejection, err := service.Apply(
			t.Context(), userID, "purge-target", model.WorkspaceLifecyclePurge, 1,
		)
		if err != nil {
			t.Fatalf("repeat purge workspace: %v", err)
		}
		if repeatRejection != nil {
			t.Fatalf("repeat purge rejection = %#v", repeatRejection)
		}
		if repeatEffect.Changed || repeatEffect.Seq != 1 || len(repeatEffect.SearchDeletes) != 0 || len(repeatEffect.SearchUpserts) != 0 {
			t.Fatalf("repeat purge effect = %#v, want unchanged seq 1 with no search work", repeatEffect)
		}
		terminalAfter := workspaceLifecycleRootSnapshot(t, testDB, "purge-target")
		if terminalAfter != terminalBefore {
			t.Fatalf("terminal root changed on repeated purge\nbefore=%s\nafter=%s", terminalBefore, terminalAfter)
		}
		assertWorkspaceLifecycleDescendantCounts(t, testDB, "purge-target", workspaceLifecycleDescendantCounts{})
		assertWorkspaceLifecycleUserSeq(t, testDB, userID, 1)
	})

	for _, table := range []string{"workspaces", "group_tabs", "groups", "bookmarks", "collections"} {
		t.Run("rollback_on_"+table+"_failure", func(t *testing.T) {
			testDB := openWorkspaceLifecycleTestDB(t)
			userID := insertAuthTestUser(t, testDB, authTestUserSeed{
				email:    fmt.Sprintf("workspace-lifecycle-rollback-%s@example.com", table),
				password: "password123",
			})
			workspaceID := "rollback-" + table
			insertWorkspaceLifecycleFixture(t, testDB, userID, workspaceID, 1)

			tx, err := testDB.Begin(t.Context())
			if err != nil {
				t.Fatalf("begin rollback test transaction: %v", err)
			}
			defer tx.Rollback(context.Background())
			installWorkspaceLifecycleFailingTrigger(t, tx, table)

			service := NewWorkspaceLifecycleService(testDB, pubsub.NewInMemoryHub(), nil)
			_, rejection, err := service.ApplyInTx(
				t.Context(), tx, userID, workspaceID, model.WorkspaceLifecyclePurge, 1, 99, 123456,
			)
			if err == nil {
				t.Fatalf("purge with failing %s trigger returned nil error", table)
			}
			if rejection != nil {
				t.Fatalf("purge with failing %s trigger rejection = %#v", table, rejection)
			}
			if rollbackErr := tx.Rollback(t.Context()); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
				t.Fatalf("rollback failed transaction: %v", rollbackErr)
			}

			assertWorkspaceLifecycleRoot(t, testDB, workspaceID, workspaceLifecycleRootExpectation{
				name:          "Workspace " + workspaceID,
				icon:          stringPtr("icon-" + workspaceID),
				color:         stringPtr("color-" + workspaceID),
				position:      17,
				seq:           40,
				isDeleted:     1,
				deletionModel: 1,
				deletedAtSet:  true,
			})
			assertWorkspaceLifecycleDescendantCounts(t, testDB, workspaceID, workspaceLifecycleDescendantCounts{
				collections: 1,
				bookmarks:   3,
				groups:      1,
				groupTabs:   1,
			})
		})
	}
}

type workspaceLifecycleRootExpectation struct {
	name          string
	icon          *string
	color         *string
	position      int
	seq           int64
	isDeleted     int
	deletionModel int
	deletedAtSet  bool
}

type workspaceLifecycleDescendantCounts struct {
	collections int
	bookmarks   int
	groups      int
	groupTabs   int
}

func openWorkspaceLifecycleTestDB(t *testing.T) *db.DB {
	t.Helper()
	return openSyncTestDB(t)
}

func insertWorkspaceLifecycleFixture(t *testing.T, testDB *db.DB, userID, workspaceID string, isDeleted int) {
	t.Helper()
	insertWorkspaceLifecycleRoot(t, testDB, userID, workspaceID, isDeleted)
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections
			(id, user_id, workspace_id, name, icon, position, seq, deleted_at, archived_at, is_deleted, created_at, updated_at)
		VALUES
			($1, $2, $3, 'Fixture collection', 'fixture-collection-icon', 23, 31, 222, 333, 1, 444, 555)`,
		workspaceID+"-collection", userID, workspaceID,
	); err != nil {
		t.Fatalf("insert collection fixture: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO bookmarks
			(id, user_id, collection_id, title, url, favicon_url, description, is_favorite, is_archived,
			 is_trashed, tag_ids, position, seq, deleted_at, created_at, updated_at)
		VALUES
			($1, $2, $3, 'Active bookmark', 'https://active.example.com', 'active-icon', 'Active description', TRUE, FALSE,
			 0, ARRAY['tag-a'], 1, 31, NULL, 444, 555),
			($4, $2, $3, 'Archived bookmark', 'https://archived.example.com', 'archived-icon', 'Archived description', FALSE, TRUE,
			 0, ARRAY['tag-b'], 2, 32, NULL, 445, 556),
			($5, $2, $3, 'Trashed bookmark', 'https://trashed.example.com', 'trashed-icon', 'Trashed description', FALSE, FALSE,
			 1, ARRAY['tag-c'], 3, 33, 777, 446, 557)`,
		workspaceID+"-bookmark-active", userID, workspaceID+"-collection",
		workspaceID+"-bookmark-archived", workspaceID+"-bookmark-trashed",
	); err != nil {
		t.Fatalf("insert bookmark fixtures: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO groups
			(id, user_id, workspace_id, name, color, is_compact, seq, deleted_at, is_deleted, created_at, updated_at)
		VALUES
			($1, $2, $3, 'Fixture group', 'blue', TRUE, 34, 888, 1, 444, 555)`,
		workspaceID+"-group", userID, workspaceID,
	); err != nil {
		t.Fatalf("insert group fixture: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO group_tabs (id, group_id, title, url, favicon, position)
		VALUES ($1, $2, 'Fixture tab', 'https://tab.example.com', 'tab-icon', 9)`,
		workspaceID+"-group-tab", workspaceID+"-group",
	); err != nil {
		t.Fatalf("insert group tab fixture: %v", err)
	}
}

func insertWorkspaceLifecycleRoot(t *testing.T, testDB *db.DB, userID, workspaceID string, isDeleted int) {
	t.Helper()
	var deletedAt *int64
	if isDeleted > 0 {
		deletedAt = int64Ptr(111)
	}
	name := "Workspace " + workspaceID
	icon := "icon-" + workspaceID
	color := "color-" + workspaceID
	if isDeleted == 2 {
		name = ""
		icon = ""
		color = ""
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO workspaces
			(id, user_id, name, icon, color, position, seq, deleted_at, is_deleted, deletion_model, created_at, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, 40, $7, $8, 1, 444, 555)`,
		workspaceID, userID, name, icon, color, func() int {
			if isDeleted == 2 {
				return 0
			}
			return 17
		}(), deletedAt, isDeleted,
	); err != nil {
		t.Fatalf("insert workspace fixture: %v", err)
	}
}

func workspaceLifecycleDescendantSnapshot(t *testing.T, testDB *db.DB, workspaceID string) []string {
	t.Helper()
	rows, err := testDB.Query(t.Context(), `
		SELECT kind || ':' || id || ':' || row_data
		FROM (
			SELECT 'collection' AS kind, c.id, row_to_json(c)::text AS row_data
			FROM collections c WHERE c.workspace_id = $1
			UNION ALL
			SELECT 'bookmark', b.id, row_to_json(b)::text
			FROM bookmarks b
			JOIN collections c ON c.id = b.collection_id
			WHERE c.workspace_id = $1
			UNION ALL
			SELECT 'group', g.id, row_to_json(g)::text
			FROM groups g WHERE g.workspace_id = $1
			UNION ALL
			SELECT 'group_tab', gt.id, row_to_json(gt)::text
			FROM group_tabs gt
			JOIN groups g ON g.id = gt.group_id
			WHERE g.workspace_id = $1
		) snapshot
		ORDER BY kind, id`, workspaceID)
	if err != nil {
		t.Fatalf("query descendant snapshot: %v", err)
	}
	defer rows.Close()
	var snapshot []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("scan descendant snapshot: %v", err)
		}
		snapshot = append(snapshot, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate descendant snapshot: %v", err)
	}
	return snapshot
}

func workspaceLifecycleRootSnapshot(t *testing.T, testDB *db.DB, workspaceID string) string {
	t.Helper()
	var snapshot string
	if err := testDB.QueryRow(t.Context(), `SELECT row_to_json(w)::text FROM workspaces w WHERE id = $1`, workspaceID).Scan(&snapshot); err != nil {
		t.Fatalf("query root snapshot: %v", err)
	}
	return snapshot
}

func assertWorkspaceLifecycleRoot(t *testing.T, testDB *db.DB, workspaceID string, want workspaceLifecycleRootExpectation) {
	t.Helper()
	var (
		name          string
		icon          *string
		color         *string
		position      int
		seq           int64
		deletedAt     *int64
		isDeleted     int
		deletionModel int
	)
	if err := testDB.QueryRow(t.Context(), `
		SELECT name, icon, color, position, seq, deleted_at, is_deleted, deletion_model
		FROM workspaces WHERE id = $1`, workspaceID,
	).Scan(&name, &icon, &color, &position, &seq, &deletedAt, &isDeleted, &deletionModel); err != nil {
		t.Fatalf("query workspace root %q: %v", workspaceID, err)
	}
	if name != want.name || !reflect.DeepEqual(icon, want.icon) || !reflect.DeepEqual(color, want.color) ||
		position != want.position || seq != want.seq || isDeleted != want.isDeleted || deletionModel != want.deletionModel ||
		(deletedAt != nil) != want.deletedAtSet {
		t.Fatalf("workspace root = {name:%q icon:%v color:%v position:%d seq:%d deletedAt:%v isDeleted:%d deletionModel:%d}, want %#v",
			name, icon, color, position, seq, deletedAt, isDeleted, deletionModel, want)
	}
}

func assertWorkspaceLifecycleDescendantCounts(
	t *testing.T,
	testDB *db.DB,
	workspaceID string,
	want workspaceLifecycleDescendantCounts,
) {
	t.Helper()
	var got workspaceLifecycleDescendantCounts
	if err := testDB.QueryRow(t.Context(), `
		SELECT
			(SELECT COUNT(*) FROM collections WHERE workspace_id = $1),
			(SELECT COUNT(*) FROM bookmarks b JOIN collections c ON c.id = b.collection_id WHERE c.workspace_id = $1),
			(SELECT COUNT(*) FROM groups WHERE workspace_id = $1),
			(SELECT COUNT(*) FROM group_tabs gt JOIN groups g ON g.id = gt.group_id WHERE g.workspace_id = $1)`,
		workspaceID,
	).Scan(&got.collections, &got.bookmarks, &got.groups, &got.groupTabs); err != nil {
		t.Fatalf("query descendant counts: %v", err)
	}
	if got != want {
		t.Fatalf("descendant counts = %#v, want %#v", got, want)
	}
}

func installWorkspaceLifecycleFailingTrigger(t *testing.T, tx pgx.Tx, table string) {
	t.Helper()
	functionName := "test_workspace_lifecycle_fail_" + table
	triggerName := "test_workspace_lifecycle_fail_" + table
	if _, err := tx.Exec(t.Context(), fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced workspace lifecycle %s failure';
		END
		$$`, functionName, table)); err != nil {
		t.Fatalf("create failing %s function: %v", table, err)
	}
	event := "DELETE"
	if table == "workspaces" {
		event = "UPDATE"
	}
	if _, err := tx.Exec(t.Context(), fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()",
		triggerName, event, table, functionName,
	)); err != nil {
		t.Fatalf("create failing %s trigger: %v", table, err)
	}
}

func assertWorkspaceLifecycleBroadcast(t *testing.T, broadcasts <-chan int64, want int64) {
	t.Helper()
	select {
	case got := <-broadcasts:
		if got != want {
			t.Fatalf("broadcast seq = %d, want %d", got, want)
		}
	default:
		t.Fatal("expected workspace lifecycle broadcast")
	}
}

func assertNoWorkspaceLifecycleBroadcast(t *testing.T, broadcasts <-chan int64) {
	t.Helper()
	select {
	case got := <-broadcasts:
		t.Fatalf("unexpected workspace lifecycle broadcast seq %d", got)
	default:
	}
}

func assertWorkspaceLifecycleUserSeq(t *testing.T, testDB *db.DB, userID string, want int64) {
	t.Helper()
	var got int64
	if err := testDB.QueryRow(t.Context(), `SELECT seq FROM user_sync_seq WHERE user_id = $1`, userID).Scan(&got); err != nil {
		t.Fatalf("query user sync seq: %v", err)
	}
	if got != want {
		t.Fatalf("user sync seq = %d, want %d", got, want)
	}
}

func assertWorkspaceLifecycleUserSeqMissing(t *testing.T, testDB *db.DB, userID string) {
	t.Helper()
	var count int
	if err := testDB.QueryRow(t.Context(), `SELECT COUNT(*) FROM user_sync_seq WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("query user sync seq count: %v", err)
	}
	if count != 0 {
		t.Fatalf("user sync seq rows = %d, want 0", count)
	}
}

func stringPtr(value string) *string {
	return &value
}
