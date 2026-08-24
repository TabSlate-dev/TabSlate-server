package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/billing/local"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/mailer"
	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
)

func TestCleanup_WorkspaceRetentionExpiresByPlan(t *testing.T) {
	testDB := openCleanupTestDB(t)
	now := time.Now()

	expiredUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-expired@example.com", password: "Password123",
	})
	retainedUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-retained@example.com", password: "Password123",
	})
	unlimitedUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-unlimited@example.com", password: "Password123",
	})

	insertWorkspaceLifecycleFixture(t, testDB, expiredUserID, "expired-workspace", 1)
	insertWorkspaceLifecycleRoot(t, testDB, retainedUserID, "retained-workspace", 1)
	insertWorkspaceLifecycleRoot(t, testDB, unlimitedUserID, "unlimited-workspace", 1)
	setCleanupWorkspaceDeletedAt(t, testDB, "expired-workspace", now.Add(-48*time.Hour))
	setCleanupWorkspaceDeletedAt(t, testDB, "retained-workspace", now.Add(-48*time.Hour))
	setCleanupWorkspaceDeletedAt(t, testDB, "unlimited-workspace", now.Add(-365*24*time.Hour))

	h := newCleanupWorkspaceTestHandler(testDB, cleanupLimitsProvider{limitsByUserID: map[string]*billing.Limits{
		expiredUserID:   {TrashGraceDays: 1},
		retainedUserID:  {TrashGraceDays: 3},
		unlimitedUserID: {TrashGraceDays: -1},
	}})
	h.runOnce(t.Context())

	assertWorkspaceLifecycleRoot(t, testDB, "expired-workspace", workspaceLifecycleRootExpectation{
		name: "", position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
	})
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "expired-workspace", workspaceLifecycleDescendantCounts{})
	assertWorkspaceLifecycleRoot(t, testDB, "retained-workspace", workspaceLifecycleRootExpectation{
		name: "Workspace retained-workspace", icon: stringPtr("icon-retained-workspace"), color: stringPtr("color-retained-workspace"),
		position: 17, seq: 40, isDeleted: 1, deletionModel: 1, deletedAtSet: true,
	})
	assertWorkspaceLifecycleRoot(t, testDB, "unlimited-workspace", workspaceLifecycleRootExpectation{
		name: "Workspace unlimited-workspace", icon: stringPtr("icon-unlimited-workspace"), color: stringPtr("color-unlimited-workspace"),
		position: 17, seq: 40, isDeleted: 1, deletionModel: 1, deletedAtSet: true,
	})
}

func TestCleanup_WorkspaceRetentionSkipsRestoredRoot(t *testing.T) {
	testDB := openCleanupTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-restored@example.com", password: "Password123",
	})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "restored-workspace", 1)
	setCleanupWorkspaceDeletedAt(t, testDB, "restored-workspace", time.Now().Add(-48*time.Hour))

	h := newCleanupWorkspaceTestHandler(testDB, cleanupLimitsProvider{
		limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}},
		onGetLimits: func(lookupUserID string) {
			if lookupUserID != userID {
				return
			}
			if _, err := testDB.Exec(t.Context(), `
				UPDATE workspaces
				SET is_deleted = 0, deleted_at = NULL
				WHERE id = 'restored-workspace'`); err != nil {
				t.Fatalf("restore workspace during cleanup: %v", err)
			}
		},
	})
	h.runOnce(t.Context())

	assertWorkspaceLifecycleRoot(t, testDB, "restored-workspace", workspaceLifecycleRootExpectation{
		name: "Workspace restored-workspace", icon: stringPtr("icon-restored-workspace"), color: stringPtr("color-restored-workspace"),
		position: 17, seq: 40, isDeleted: 0, deletionModel: 1,
	})
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "restored-workspace", workspaceLifecycleDescendantCounts{
		collections: 1, bookmarks: 3, groups: 1, groupTabs: 1,
	})
}

func TestCleanup_WorkspaceRetentionRetriesAggregateAfterPurgeFailure(t *testing.T) {
	testDB := openCleanupTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-retry@example.com", password: "Password123",
	})
	insertWorkspaceLifecycleFixture(t, testDB, userID, "retry-workspace", 1)
	setCleanupWorkspaceDeletedAt(t, testDB, "retry-workspace", time.Now().Add(-48*time.Hour))
	if _, err := testDB.Exec(t.Context(), `
		CREATE FUNCTION cleanup_fail_workspace_groups() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'forced cleanup workspace purge failure';
		END
		$$`); err != nil {
		t.Fatalf("create cleanup failure function: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `
		CREATE TRIGGER cleanup_fail_workspace_groups
		BEFORE DELETE ON groups
		FOR EACH ROW EXECUTE FUNCTION cleanup_fail_workspace_groups()`); err != nil {
		t.Fatalf("create cleanup failure trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testDB.Exec(context.Background(), `DROP TRIGGER IF EXISTS cleanup_fail_workspace_groups ON groups`); err != nil {
			t.Errorf("drop cleanup failure trigger: %v", err)
		}
		if _, err := testDB.Exec(context.Background(), `DROP FUNCTION IF EXISTS cleanup_fail_workspace_groups()`); err != nil {
			t.Errorf("drop cleanup failure function: %v", err)
		}
	})

	h := newCleanupWorkspaceTestHandler(testDB, cleanupLimitsProvider{
		limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}},
	})
	h.runOnce(t.Context())

	assertWorkspaceLifecycleRoot(t, testDB, "retry-workspace", workspaceLifecycleRootExpectation{
		name: "Workspace retry-workspace", icon: stringPtr("icon-retry-workspace"), color: stringPtr("color-retry-workspace"),
		position: 17, seq: 40, isDeleted: 1, deletionModel: 1, deletedAtSet: true,
	})
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "retry-workspace", workspaceLifecycleDescendantCounts{
		collections: 1, bookmarks: 3, groups: 1, groupTabs: 1,
	})
	if _, err := testDB.Exec(t.Context(), `DROP TRIGGER cleanup_fail_workspace_groups ON groups`); err != nil {
		t.Fatalf("remove cleanup failure trigger: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `DROP FUNCTION cleanup_fail_workspace_groups()`); err != nil {
		t.Fatalf("remove cleanup failure function: %v", err)
	}

	h.runOnce(t.Context())
	assertWorkspaceLifecycleRoot(t, testDB, "retry-workspace", workspaceLifecycleRootExpectation{
		name: "", position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
	})
	assertWorkspaceLifecycleDescendantCounts(t, testDB, "retry-workspace", workspaceLifecycleDescendantCounts{})
}

func TestCleanup_WorkspaceRetentionRetriesBillingLookupFailure(t *testing.T) {
	testDB := openCleanupTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-billing-retry@example.com", password: "Password123",
	})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "billing-retry-workspace", 1)
	setCleanupWorkspaceDeletedAt(t, testDB, "billing-retry-workspace", time.Now().Add(-48*time.Hour))

	provider := cleanupLimitsProvider{
		limitsByUserID: map[string]*billing.Limits{userID: {TrashGraceDays: 1}},
		errorsByUserID: map[string]error{userID: errors.New("billing temporarily unavailable")},
	}
	h := newCleanupWorkspaceTestHandler(testDB, provider)
	h.runOnce(t.Context())

	assertWorkspaceLifecycleRoot(t, testDB, "billing-retry-workspace", workspaceLifecycleRootExpectation{
		name: "Workspace billing-retry-workspace", icon: stringPtr("icon-billing-retry-workspace"), color: stringPtr("color-billing-retry-workspace"),
		position: 17, seq: 40, isDeleted: 1, deletionModel: 1, deletedAtSet: true,
	})
	delete(provider.errorsByUserID, userID)

	h.runOnce(t.Context())
	assertWorkspaceLifecycleRoot(t, testDB, "billing-retry-workspace", workspaceLifecycleRootExpectation{
		name: "", position: 0, seq: 1, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
	})
}

func TestCleanup_NeverDeletesWorkspaceTerminal(t *testing.T) {
	testDB := openCleanupTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email: "workspace-terminal@example.com", password: "Password123",
	})
	insertWorkspaceLifecycleRoot(t, testDB, userID, "terminal-workspace", 2)
	setCleanupWorkspaceDeletedAt(t, testDB, "terminal-workspace", time.Now().Add(-365*24*time.Hour))

	newCleanupWorkspaceTestHandler(testDB, cleanupLimitsProvider{}).runOnce(t.Context())

	assertWorkspaceLifecycleRoot(t, testDB, "terminal-workspace", workspaceLifecycleRootExpectation{
		name: "", position: 0, seq: 40, isDeleted: 2, deletionModel: 1, deletedAtSet: true,
	})
}

func TestCleanupRunOnceMarksDueDeletionReminders(t *testing.T) {
	testDB := openCleanupTestDB(t)
	now := time.Now().Unix()

	dueUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "due-reminder@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 28*24*60*60),
	})
	futureUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "future-reminder@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 20*24*60*60),
	})
	alreadySentUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "already-reminded@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 28*24*60*60),
	})

	if _, err := testDB.Exec(
		t.Context(),
		`UPDATE users SET deletion_reminder_sent_at = $1 WHERE id = $2`,
		now-60,
		alreadySentUserID,
	); err != nil {
		t.Fatalf("seed reminder timestamp: %v", err)
	}

	h := newCleanupTestHandler(testDB)
	h.runOnce(t.Context())

	assertDeletionReminderState(t, testDB, dueUserID, true)
	assertDeletionReminderState(t, testDB, futureUserID, false)
	assertDeletionReminderState(t, testDB, alreadySentUserID, true)
}

func TestCleanupRunOnceDeletesExpiredAccounts(t *testing.T) {
	testDB := openCleanupTestDB(t)
	now := time.Now().Unix()

	expiredUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "expired-account@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 31*24*60*60),
	})
	activeUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "active-account@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 20*24*60*60),
	})

	h := newCleanupTestHandler(testDB)
	h.runOnce(t.Context())

	assertUserDeleted(t, testDB, expiredUserID, true)
	assertUserDeleted(t, testDB, activeUserID, false)
}

func TestCleanupRunOnceNotifiesBillingDeleterForExpiredAccounts(t *testing.T) {
	testDB := openCleanupTestDB(t)
	now := time.Now().Unix()

	expiredUserID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "billing-delete@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(now - 31*24*60*60),
	})

	billingSpy := &cleanupBillingSpy{Provider: local.New(testDB)}
	h := NewCleanupHandler(testDB, 7, mailer.New(mailer.Config{}), billingSpy, nil)
	h.runOnce(t.Context())

	if len(billingSpy.deletedUserIDs) != 1 {
		t.Fatalf("expected 1 billing deletion, got %d", len(billingSpy.deletedUserIDs))
	}
	if billingSpy.deletedUserIDs[0] != expiredUserID {
		t.Fatalf("expected billing deletion for %s, got %s", expiredUserID, billingSpy.deletedUserIDs[0])
	}
}

func openCleanupTestDB(t *testing.T) *db.DB {
	t.Helper()

	testDB := openAuthTestDB(t)
	if _, err := testDB.Exec(t.Context(), `TRUNCATE TABLE bookmarks, collections, group_tabs, groups, refresh_tokens, subscriptions, user_sync_seq, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate cleanup tables: %v", err)
	}
	return testDB
}

func newCleanupTestHandler(testDB *db.DB) *CleanupHandler {
	return NewCleanupHandler(testDB, 7, mailer.New(mailer.Config{}), local.New(testDB), nil)
}

func newCleanupWorkspaceTestHandler(testDB *db.DB, provider cleanupLimitsProvider) *CleanupHandler {
	return NewCleanupHandler(
		testDB,
		36500,
		mailer.New(mailer.Config{}),
		provider,
		nil,
		NewWorkspaceLifecycleService(testDB, pubsub.NewInMemoryHub(), nil),
	)
}

type cleanupLimitsProvider struct {
	limitsByUserID map[string]*billing.Limits
	errorsByUserID map[string]error
	onGetLimits    func(string)
}

func (p cleanupLimitsProvider) GetLimits(_ context.Context, userID string) (*billing.Limits, error) {
	if p.onGetLimits != nil {
		p.onGetLimits(userID)
	}
	if err := p.errorsByUserID[userID]; err != nil {
		return nil, err
	}
	if limits := p.limitsByUserID[userID]; limits != nil {
		return limits, nil
	}
	return &billing.Limits{TrashGraceDays: -1}, nil
}

func setCleanupWorkspaceDeletedAt(t *testing.T, testDB *db.DB, workspaceID string, deletedAt time.Time) {
	t.Helper()
	if _, err := testDB.Exec(t.Context(), `
		UPDATE workspaces
		SET deleted_at = $1
		WHERE id = $2`, deletedAt.UnixMilli(), workspaceID); err != nil {
		t.Fatalf("set workspace deleted_at for %s: %v", workspaceID, err)
	}
}

type cleanupBillingSpy struct {
	billing.Provider
	deletedUserIDs []string
}

func (s *cleanupBillingSpy) OnUserDeleted(_ context.Context, userID string) error {
	s.deletedUserIDs = append(s.deletedUserIDs, userID)
	return nil
}

func assertDeletionReminderState(t *testing.T, testDB *db.DB, userID string, wantSet bool) {
	t.Helper()

	var reminderSentAt *int64
	if err := testDB.QueryRow(t.Context(), `SELECT deletion_reminder_sent_at FROM users WHERE id = $1`, userID).Scan(&reminderSentAt); err != nil {
		t.Fatalf("read reminder state for %s: %v", userID, err)
	}

	if wantSet && reminderSentAt == nil {
		t.Fatalf("expected deletion_reminder_sent_at to be set for %s", userID)
	}
	if !wantSet && reminderSentAt != nil {
		t.Fatalf("expected deletion_reminder_sent_at to stay NULL for %s, got %d", userID, *reminderSentAt)
	}
}

func assertUserDeleted(t *testing.T, testDB *db.DB, userID string, wantDeleted bool) {
	t.Helper()

	var count int
	if err := testDB.QueryRow(t.Context(), `SELECT COUNT(*) FROM users WHERE id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count users for %s: %v", userID, err)
	}

	if wantDeleted && count != 0 {
		t.Fatalf("expected user %s to be deleted, count=%d", userID, count)
	}
	if !wantDeleted && count != 1 {
		t.Fatalf("expected user %s to remain, count=%d", userID, count)
	}
}
