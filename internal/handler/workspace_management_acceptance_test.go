package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/mailer"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
	"github.com/TabSlate-dev/TabSlate-server/internal/search"
)

// TestWorkspaceManagementAcceptance drives the real production paths — the
// REST Workspace routes, SyncHandler.Push, CleanupHandler's retention-expiry
// job, and SearchHandler's PostgreSQL visibility join — through the
// multi-device, restart, legacy-migration, and retention scenarios called
// out by the workspace management redesign plan. It does not reimplement any
// lifecycle algorithm; every assertion observes state produced by
// WorkspaceLifecycleService, SyncHandler, WorkspaceHandler, CleanupHandler,
// or SearchHandler.
func TestWorkspaceManagementAcceptance(t *testing.T) {
	t.Run("ManualPurgeAndRetentionPurgeReachIdenticalEndState", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email: "acceptance-purge-parity@example.com", password: "password123",
		})
		hub := pubsub.NewInMemoryHub()
		lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)

		manual := seedWorkspaceAcceptanceFixture(t, testDB, userID, "acceptance-manual-purge", 1)
		retention := seedWorkspaceAcceptanceFixture(t, testDB, userID, "acceptance-retention-purge", 1)
		setCleanupWorkspaceDeletedAt(t, testDB, retention.WorkspaceID, time.Now().Add(-48*time.Hour))

		billingHandler := NewBillingHandler(fixedLimitsProvider{limits: syncTestLimits()}, nil, testDB)
		assertPlanWorkspaceUsage(t, userID, billingHandler, planUsage{Workspaces: 2}, planUsage{Workspaces: 2})

		// Manual purge: a user-initiated REST call against the retained root.
		workspaceHandler := NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: syncTestLimits()}, lifecycle)
		manualRecorder := performWorkspaceRoute(
			t, userID, http.MethodDelete, "/api/workspaces/:id/permanent",
			"/api/workspaces/"+manual.WorkspaceID+"/permanent", workspaceHandler.PermanentlyDelete,
		)
		if manualRecorder.Code != http.StatusNoContent {
			t.Fatalf("manual purge status = %d body=%s, want 204", manualRecorder.Code, manualRecorder.Body.String())
		}
		assertPlanWorkspaceUsage(t, userID, billingHandler, planUsage{Workspaces: 1}, planUsage{Workspaces: 1})

		// Retention expiry: the same *WorkspaceLifecycleService instance, reached
		// through CleanupHandler's daily job instead of the REST route.
		cleanupHandler := NewCleanupHandler(
			testDB, 36500, mailer.New(mailer.Config{}),
			cleanupLimitsProvider{limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}}},
			nil, lifecycle,
		)
		cleanupHandler.runOnce(t.Context())
		assertPlanWorkspaceUsage(t, userID, billingHandler, planUsage{Workspaces: 0}, planUsage{Workspaces: 0})

		// Both paths converge on the identical scrubbed terminal shape.
		assertWorkspaceLifecycleRoot(t, testDB, manual.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})
		assertWorkspaceLifecycleRoot(t, testDB, retention.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 2, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})
		assertWorkspaceLifecycleDescendantCounts(t, testDB, manual.WorkspaceID, workspaceLifecycleDescendantCounts{})
		assertWorkspaceLifecycleDescendantCounts(t, testDB, retention.WorkspaceID, workspaceLifecycleDescendantCounts{})
	})

	t.Run("StaleRestoreAfterPurgeIsRejected", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email: "acceptance-stale-restore@example.com", password: "password123",
		})
		hub := pubsub.NewInMemoryHub()
		lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)
		fixture := seedWorkspaceAcceptanceFixture(t, testDB, userID, "acceptance-stale-restore-target", 1)
		setCleanupWorkspaceDeletedAt(t, testDB, fixture.WorkspaceID, time.Now().Add(-48*time.Hour))

		cleanupHandler := NewCleanupHandler(
			testDB, 36500, mailer.New(mailer.Config{}),
			cleanupLimitsProvider{limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}}},
			nil, lifecycle,
		)
		cleanupHandler.runOnce(t.Context())
		assertWorkspaceLifecycleRoot(t, testDB, fixture.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})

		workspaceHandler := NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: syncTestLimits()}, lifecycle)
		recorder := performWorkspaceRoute(
			t, userID, http.MethodPost, "/api/workspaces/:id/restore",
			"/api/workspaces/"+fixture.WorkspaceID+"/restore", workspaceHandler.Restore,
		)
		assertWorkspaceRouteRejection(t, recorder, http.StatusGone, fixture.WorkspaceID, model.RejectionReasonPermanentlyDeleted)

		// The rejected restore's own seq increment rolled back with its transaction.
		assertWorkspaceLifecycleRoot(t, testDB, fixture.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})
		assertWorkspaceLifecycleDescendantCounts(t, testDB, fixture.WorkspaceID, workspaceLifecycleDescendantCounts{})
		assertWorkspaceLifecycleUserSeq(t, testDB, userID, 1)
	})

	t.Run("LatePushAfterRetentionPurgeIsPermanentlyDeleted", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email: "acceptance-late-push@example.com", password: "password123",
		})
		hub := pubsub.NewInMemoryHub()
		lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)
		fixture := seedWorkspaceAcceptanceFixture(t, testDB, userID, "acceptance-late-push-target", 1)
		insertWorkspaceLifecycleRoot(t, testDB, userID, "acceptance-late-push-sibling", 0)
		setCleanupWorkspaceDeletedAt(t, testDB, fixture.WorkspaceID, time.Now().Add(-48*time.Hour))

		// The Cleanup job purges the root while the device that queued a local
		// delete is still offline.
		cleanupHandler := NewCleanupHandler(
			testDB, 36500, mailer.New(mailer.Config{}),
			cleanupLimitsProvider{limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}}},
			nil, lifecycle,
		)
		cleanupHandler.runOnce(t.Context())
		assertWorkspaceLifecycleRoot(t, testDB, fixture.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})

		// The offline device now reconnects and pushes the delete it queued
		// locally before the purge happened.
		recorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
			ProtocolVersion: 2,
			Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{
				{ID: fixture.WorkspaceID, Name: "Queued offline delete", Position: 3, LifecycleAction: model.WorkspaceLifecycleDelete},
			}},
		})
		assertSyncStatusOK(t, recorder)
		response := decodeSyncPushResponse(t, recorder)
		if len(response.Rejected) != 1 {
			t.Fatalf("rejected = %#v, want exactly one permanently_deleted rejection", response.Rejected)
		}
		assertRejected(t, response, model.Rejected{
			ID: fixture.WorkspaceID, Reason: model.RejectionReasonPermanentlyDeleted, Type: "workspace",
		})
		if response.ServerSeq != 2 {
			t.Fatalf("server seq = %d, want 2", response.ServerSeq)
		}

		// Purge wins: the scrubbed terminal root is untouched by the late push.
		assertWorkspaceLifecycleRoot(t, testDB, fixture.WorkspaceID, workspaceLifecycleRootExpectation{
			name: "", icon: nil, color: nil, position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
		})
		assertWorkspaceLifecycleDescendantCounts(t, testDB, fixture.WorkspaceID, workspaceLifecycleDescendantCounts{})
	})

	t.Run("SearchFiltersStaleMeiliHitsForRetainedAndPurgedWorkspaces", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email: "acceptance-search-visibility@example.com", password: "password123",
		})
		hub := pubsub.NewInMemoryHub()
		lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)

		// active-workspace/active-collection/active-bookmark-* are visible;
		// retained-workspace/retained-collection/retained-bookmark is hidden
		// purely because its parent Workspace is retained (state 1).
		insertWorkspaceVisibilityFixture(t, testDB, userID)

		// A third Workspace whose bookmark row will be physically removed by a
		// real retention purge before the stale MeiliSearch hit arrives.
		insertSearchAcceptancePurgedFixture(t, testDB, userID)
		setCleanupWorkspaceDeletedAt(t, testDB, "search-purged-workspace", time.Now().Add(-48*time.Hour))
		cleanupHandler := NewCleanupHandler(
			testDB, 36500, mailer.New(mailer.Config{}),
			cleanupLimitsProvider{limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}}},
			nil, lifecycle,
		)
		cleanupHandler.runOnce(t.Context())
		assertWorkspaceLifecycleDescendantCounts(t, testDB, "search-purged-workspace", workspaceLifecycleDescendantCounts{})

		searcher := fixedBookmarkSearcher{documents: []search.BookmarkDoc{
			{ID: "search-purged-bookmark", Title: "Stale purged hit"},
			{ID: "retained-bookmark", Title: "Stale retained hit"},
			{ID: "active-bookmark-second", Title: "Visible hit second"},
			{ID: "active-bookmark-first", Title: "Visible hit first"},
		}}
		searchHandler := NewSearchHandler(testDB, searcher)
		recorder := performHandlerRequest(t, userID, http.MethodGet, "/search?q=hit", nil, searchHandler.Search)

		var response struct {
			Bookmarks []search.BookmarkDoc `json:"bookmarks"`
		}
		decodeHandlerResponse(t, recorder, http.StatusOK, &response)
		if len(response.Bookmarks) != 2 ||
			response.Bookmarks[0].ID != "active-bookmark-second" ||
			response.Bookmarks[1].ID != "active-bookmark-first" {
			t.Fatalf("bookmarks = %#v, want only the two visible active-workspace hits in MeiliSearch order", response.Bookmarks)
		}
	})

	t.Run("VersionlessPushAndRestoreUseLegacyCascadeNotParentTombstone", func(t *testing.T) {
		testDB := openSyncTestDB(t)
		userID := insertAuthTestUser(t, testDB, authTestUserSeed{
			email: "acceptance-versionless-migration@example.com", password: "password123",
		})
		hub := pubsub.NewInMemoryHub()
		lifecycle := NewWorkspaceLifecycleService(testDB, hub, nil)
		workspaceID := "acceptance-versionless-workspace"
		insertLegacyWorkspaceLifecycleFixture(t, testDB, userID, workspaceID, 0, 1, 15)
		insertWorkspaceLifecycleRoot(t, testDB, userID, workspaceID+"-sibling", 0)
		// Pre-advance the sequence counter well past the fixture's independent
		// (pre-existing) trashed evidence (seq 12/13/14) so the cascade delete's
		// own seq cleanly separates "part of this delete" from "already trashed
		// before this delete ever happened".
		if _, err := testDB.Exec(t.Context(), `INSERT INTO user_sync_seq (user_id, seq) VALUES ($1, 20)`, userID); err != nil {
			t.Fatalf("seed user sync seq: %v", err)
		}

		deletedAtHint := int64(1)
		pushRecorder := pushSyncRequest(t, testDB, userID, syncTestLimits(), model.SyncPushRequest{
			// ProtocolVersion intentionally omitted (zero value): a pre-migration
			// client that has never heard of protocol_version 2.
			Entities: model.SyncPushEntities{Workspaces: []model.SyncWorkspaceMutation{
				{ID: workspaceID, Name: "Ignored by legacy cascade", Position: 99, DeletedAt: &deletedAtHint},
			}},
		})
		assertSyncStatusOK(t, pushRecorder)
		pushResponse := decodeSyncPushResponse(t, pushRecorder)
		if len(pushResponse.Rejected) != 0 {
			t.Fatalf("versionless delete rejected = %#v, want none", pushResponse.Rejected)
		}
		if pushResponse.ServerSeq != 21 {
			t.Fatalf("versionless delete server seq = %d, want 21", pushResponse.ServerSeq)
		}

		var observedDeletedAt int64
		if err := testDB.QueryRow(t.Context(), `SELECT deleted_at FROM workspaces WHERE id = $1`, workspaceID).Scan(&observedDeletedAt); err != nil {
			t.Fatalf("query cascade deleted_at: %v", err)
		}

		// The root keeps its original name/icon/color/position: the legacy
		// cascade UPDATE never touches them, unlike a metadata upsert.
		assertWorkspaceLifecycleRoot(t, testDB, workspaceID, workspaceLifecycleRootExpectation{
			name: "Workspace " + workspaceID, icon: stringPtr("icon-" + workspaceID), color: stringPtr("color-" + workspaceID),
			position: 17, seq: 21, isDeleted: 1, deletionModel: 0, deletedAtSet: true,
		})
		assertLegacyWorkspaceLifecycleState(t, testDB, workspaceID, legacyWorkspaceLifecycleExpectation{
			activeCollection:           lifecycleRowState{state: 1, seq: 21, deletedAt: int64Ptr(observedDeletedAt)},
			archivedCollection:         lifecycleRowState{state: 1, seq: 21, deletedAt: int64Ptr(observedDeletedAt)},
			trashedCollection:          lifecycleRowState{state: 1, seq: 12, deletedAt: int64Ptr(7012)},
			activeBookmark:             lifecycleRowState{state: 1, seq: 21, deletedAt: int64Ptr(observedDeletedAt)},
			archivedBookmarkState:      lifecycleRowState{state: 1, seq: 21, deletedAt: int64Ptr(observedDeletedAt)},
			trashedBookmark:            lifecycleRowState{state: 1, seq: 13, deletedAt: int64Ptr(7013)},
			contradictoryBookmark:      lifecycleRowState{state: 1, seq: 15, deletedAt: int64Ptr(7090)},
			activeGroup:                lifecycleRowState{state: 1, seq: 21, deletedAt: int64Ptr(observedDeletedAt)},
			trashedGroup:               lifecycleRowState{state: 1, seq: 14, deletedAt: int64Ptr(7014)},
			archivedAt:                 int64Ptr(6032),
			archivedBookmarkIsArchived: true,
		})

		// Versionless restore: the REST route carries no protocol_version at
		// all, yet must still resolve to the legacy atomic cascade restore
		// (because the row's stored deletion_model is 0), not the
		// parent-tombstone-only restore.
		workspaceHandler := NewWorkspaceHandler(testDB, hub, fixedLimitsProvider{limits: syncTestLimits()}, lifecycle)
		restoreRecorder := performWorkspaceRoute(
			t, userID, http.MethodPost, "/api/workspaces/:id/restore",
			"/api/workspaces/"+workspaceID+"/restore", workspaceHandler.Restore,
		)
		if restoreRecorder.Code != http.StatusOK {
			t.Fatalf("restore status = %d body=%s, want 200", restoreRecorder.Code, restoreRecorder.Body.String())
		}

		assertWorkspaceLifecycleRoot(t, testDB, workspaceID, workspaceLifecycleRootExpectation{
			name: "Workspace " + workspaceID, icon: stringPtr("icon-" + workspaceID), color: stringPtr("color-" + workspaceID),
			position: 17, seq: 22, isDeleted: 0, deletionModel: 1, deletedAtSet: false,
		})
		assertLegacyWorkspaceLifecycleState(t, testDB, workspaceID, legacyWorkspaceLifecycleExpectation{
			activeCollection:           lifecycleRowState{state: 0, seq: 22},
			archivedCollection:         lifecycleRowState{state: 0, seq: 22},
			trashedCollection:          lifecycleRowState{state: 1, seq: 12, deletedAt: int64Ptr(7012)},
			activeBookmark:             lifecycleRowState{state: 0, seq: 22},
			archivedBookmarkState:      lifecycleRowState{state: 0, seq: 22},
			trashedBookmark:            lifecycleRowState{state: 1, seq: 13, deletedAt: int64Ptr(7013)},
			contradictoryBookmark:      lifecycleRowState{state: 1, seq: 15, deletedAt: int64Ptr(7090)},
			activeGroup:                lifecycleRowState{state: 0, seq: 22},
			trashedGroup:               lifecycleRowState{state: 1, seq: 14, deletedAt: int64Ptr(7014)},
			archivedAt:                 int64Ptr(6032),
			archivedBookmarkIsArchived: false,
		})
	})
}

// workspaceAcceptanceFixture identifies the entities a seeded acceptance
// Workspace produced, so scenarios can reference them without re-deriving
// the fixture's internal ID scheme.
type workspaceAcceptanceFixture struct {
	UserID       string
	WorkspaceID  string
	CollectionID string
	BookmarkIDs  []string
	GroupID      string
}

// seedWorkspaceAcceptanceFixture wraps the existing raw-SQL lifecycle test
// fixture (insertWorkspaceLifecycleFixture) — it seeds data only, it does not
// reimplement any lifecycle transition.
func seedWorkspaceAcceptanceFixture(
	t *testing.T,
	testDB *db.DB,
	userID string,
	workspaceID string,
	isDeleted int,
) workspaceAcceptanceFixture {
	t.Helper()
	insertWorkspaceLifecycleFixture(t, testDB, userID, workspaceID, isDeleted)
	return workspaceAcceptanceFixture{
		UserID:       userID,
		WorkspaceID:  workspaceID,
		CollectionID: workspaceID + "-collection",
		BookmarkIDs: []string{
			workspaceID + "-bookmark-active",
			workspaceID + "-bookmark-archived",
			workspaceID + "-bookmark-trashed",
		},
		GroupID: workspaceID + "-group",
	}
}

// insertSearchAcceptancePurgedFixture seeds a Workspace/collection/bookmark
// with a genuinely visible shape (all state 0) so that after a real
// retention purge removes the rows, a stale MeiliSearch hit for the bookmark
// ID has nothing left to join against in PostgreSQL.
func insertSearchAcceptancePurgedFixture(t *testing.T, testDB *db.DB, userID string) {
	t.Helper()
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO workspaces
			(id, user_id, name, icon, color, position, seq, deleted_at, is_deleted, deletion_model, created_at, updated_at)
		VALUES
			('search-purged-workspace', $1, 'Purged workspace', 'purged-icon', 'green', 3, 3, NULL, 1, 1, 1, 1)`, userID); err != nil {
		t.Fatalf("insert search acceptance purged workspace: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO collections
			(id, user_id, workspace_id, name, icon, position, seq, is_deleted, created_at, updated_at)
		VALUES
			('search-purged-collection', $1, 'search-purged-workspace', 'Purged collection', 'purged-icon', 1, 1, 0, 1, 1)`, userID); err != nil {
		t.Fatalf("insert search acceptance purged collection: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		INSERT INTO bookmarks
			(id, user_id, collection_id, title, url, favicon_url, description, is_trashed, position, seq, created_at, updated_at)
		VALUES
			('search-purged-bookmark', $1, 'search-purged-collection', 'Purged bookmark',
			 'https://purged.example.com', 'purged-icon', 'Purged description', 0, 1, 1, 1, 1)`, userID); err != nil {
		t.Fatalf("insert search acceptance purged bookmark: %v", err)
	}
}

func assertPlanWorkspaceUsage(
	t *testing.T,
	userID string,
	billingHandler *BillingHandler,
	wantUsage planUsage,
	wantTrashUsage planUsage,
) {
	t.Helper()
	recorder := performHandlerRequest(t, userID, http.MethodGet, "/api/plan", nil, billingHandler.GetPlan)
	var response struct {
		Usage      planUsage `json:"usage"`
		TrashUsage planUsage `json:"trash_usage"`
	}
	decodeHandlerResponse(t, recorder, http.StatusOK, &response)
	if response.Usage.Workspaces != wantUsage.Workspaces {
		t.Fatalf("usage.workspaces = %d, want %d", response.Usage.Workspaces, wantUsage.Workspaces)
	}
	if response.TrashUsage.Workspaces != wantTrashUsage.Workspaces {
		t.Fatalf("trash_usage.workspaces = %d, want %d", response.TrashUsage.Workspaces, wantTrashUsage.Workspaces)
	}
}
