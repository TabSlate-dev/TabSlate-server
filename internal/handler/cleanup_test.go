package handler

import (
	"context"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing"
	"github.com/TabSlate-dev/TabSlate-server/billing/local"
	"github.com/TabSlate-dev/TabSlate-server/db"
	"github.com/TabSlate-dev/TabSlate-server/internal/mailer"
)

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
