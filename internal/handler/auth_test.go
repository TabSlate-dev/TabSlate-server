package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/billing/local"
	"github.com/TabSlate-dev/TabSlate-server/db"
	iauth "github.com/TabSlate-dev/TabSlate-server/internal/auth"
	"github.com/TabSlate-dev/TabSlate-server/internal/captcha"
	"github.com/TabSlate-dev/TabSlate-server/internal/mailer"
	"github.com/TabSlate-dev/TabSlate-server/internal/middleware"
	"github.com/TabSlate-dev/TabSlate-server/internal/model"
	"github.com/TabSlate-dev/TabSlate-server/internal/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestParseLang(t *testing.T) {
	tests := []struct {
		name       string
		acceptLang string
		want       string
	}{
		{name: "zh-CN", acceptLang: "zh-CN,zh;q=0.9,en;q=0.8", want: "zh"},
		{name: "zh", acceptLang: "zh", want: "zh"},
		{name: "zh-TW", acceptLang: "zh-TW,zh;q=0.9", want: "zh"},
		{name: "en-US", acceptLang: "en-US,en;q=0.9", want: "en"},
		{name: "en", acceptLang: "en", want: "en"},
		{name: "fr-FR", acceptLang: "fr-FR,fr;q=0.9", want: "en"},
		{name: "empty", acceptLang: "", want: "en"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLang(tc.acceptLang); got != tc.want {
				t.Fatalf("parseLang(%q) = %q, want %q", tc.acceptLang, got, tc.want)
			}
		})
	}
}

func TestRegister_registrationClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := `{"email":"test@example.com","password":"test123456","name":"Test"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{registrationOpen: false}
	h.Register(c)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["error"] != "registration is disabled on this instance" {
		t.Errorf("unexpected error: %q", resp["error"])
	}
}

func TestDeleteAccount_MissingPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/delete-account", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &AuthHandler{}
	h.DeleteAccount(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestLogin_ReturnsErrorWhenLastLoginUpdateFails(t *testing.T) {
	testDB := openAuthTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "update-fails@example.com",
		password: "Password123",
	})

	if _, err := testDB.Exec(t.Context(), `ALTER TABLE users DROP COLUMN last_login_at`); err != nil {
		t.Fatalf("drop last_login_at: %v", err)
	}

	h := newAuthTestHandler(testDB)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"update-fails@example.com","password":"Password123"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}

	var refreshTokenCount int
	if err := testDB.QueryRow(t.Context(), `SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`, userID).Scan(&refreshTokenCount); err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if refreshTokenCount != 0 {
		t.Fatalf("expected no refresh tokens, got %d", refreshTokenCount)
	}
}

func TestLogin_DoesNotUpdateLastLoginAtWhenTokenIssuanceFails(t *testing.T) {
	testDB := openAuthTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "token-fails@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(1_700_000_000),
	})

	if _, err := testDB.Exec(t.Context(), `DROP TABLE refresh_tokens`); err != nil {
		t.Fatalf("drop refresh_tokens: %v", err)
	}

	h := newAuthTestHandler(testDB)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"token-fails@example.com","password":"Password123"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", w.Code, w.Body.String())
	}

	var lastLoginAt *int64
	if err := testDB.QueryRow(t.Context(), `SELECT last_login_at FROM users WHERE id = $1`, userID).Scan(&lastLoginAt); err != nil {
		t.Fatalf("read last_login_at: %v", err)
	}
	if lastLoginAt != nil {
		t.Fatalf("expected last_login_at to remain NULL, got %d", *lastLoginAt)
	}
}

func TestDeleteAccount_SchedulesDeletionForValidPassword(t *testing.T) {
	testDB := openAuthTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:    "delete-me@example.com",
		password: "Password123",
		name:     "Delete Me",
	})

	h := newAuthTestHandler(testDB)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.UserIDKey, userID)
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/delete-account", strings.NewReader(`{"password":"Password123"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.DeleteAccount(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		ScheduledAt int64 `json:"scheduled_at"`
		ExecutesAt  int64 `json:"executes_at"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := resp.ExecutesAt-resp.ScheduledAt, int64((30 * 24 * time.Hour).Seconds()); got != want {
		t.Fatalf("expected 30 day delay, got %d seconds", got)
	}

	var scheduledAt *int64
	var reminderSentAt *int64
	if err := testDB.QueryRow(t.Context(), `SELECT deletion_requested_at, deletion_reminder_sent_at FROM users WHERE id = $1`, userID).Scan(&scheduledAt, &reminderSentAt); err != nil {
		t.Fatalf("read deletion state: %v", err)
	}
	if scheduledAt == nil {
		t.Fatal("expected deletion_requested_at to be set")
	}
	if *scheduledAt != resp.ScheduledAt {
		t.Fatalf("expected deletion_requested_at %d, got %d", resp.ScheduledAt, *scheduledAt)
	}
	if reminderSentAt != nil {
		t.Fatalf("expected deletion_reminder_sent_at to be NULL, got %d", *reminderSentAt)
	}
}

func TestMe_DerivesDeletionScheduledAtFromLatestDeletionOrLogin(t *testing.T) {
	testDB := openAuthTestDB(t)
	userID := insertAuthTestUser(t, testDB, authTestUserSeed{
		email:               "me@example.com",
		password:            "Password123",
		deletionRequestedAt: int64Ptr(1_700_000_000),
		lastLoginAt:         int64Ptr(1_700_000_100),
	})

	h := newAuthTestHandler(testDB)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.UserIDKey, userID)
	c.Request = httptest.NewRequest(http.MethodGet, "/auth/me", nil)

	h.Me(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		User model.User `json:"user"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := int64(1_700_000_100) + int64((30 * 24 * time.Hour).Seconds())
	if resp.User.DeletionScheduledAt == nil {
		t.Fatal("expected deletion_scheduled_at to be present")
	}
	if *resp.User.DeletionScheduledAt != want {
		t.Fatalf("expected deletion_scheduled_at %d, got %d", want, *resp.User.DeletionScheduledAt)
	}
}

type authTestUserSeed struct {
	email               string
	password            string
	name                string
	deletionRequestedAt *int64
	lastLoginAt         *int64
}

func openAuthTestDB(t *testing.T) *db.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	testDB, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })

	if err := db.Migrate(testDB); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	if _, err := testDB.Exec(t.Context(), `TRUNCATE TABLE refresh_tokens, subscriptions, user_sync_seq, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate test db: %v", err)
	}

	return testDB
}

func insertAuthTestUser(t *testing.T, testDB *db.DB, seed authTestUserSeed) string {
	t.Helper()

	name := seed.name
	if name == "" {
		name = "Test User"
	}

	hash, err := iauth.HashPassword(seed.password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	userID := uuid.NewString()
	now := time.Now().Unix()
	if _, err := testDB.Exec(
		t.Context(),
		`INSERT INTO users (id, name, email, password_hash, is_verified, created_at, updated_at, deletion_requested_at, last_login_at)
		 VALUES ($1, $2, $3, $4, TRUE, $5, $5, $6, $7)`,
		userID, name, seed.email, hash, now, seed.deletionRequestedAt, seed.lastLoginAt,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	return userID
}

func newAuthTestHandler(testDB *db.DB) *AuthHandler {
	return NewAuthHandler(
		testDB,
		"test-secret",
		local.New(testDB),
		captcha.New("", ""),
		mailer.New(mailer.Config{}),
		ratelimit.NewInMemoryLimiter(),
		nil,
		3,
		24*time.Hour,
		5,
		15*time.Minute,
		true,
	)
}

func int64Ptr(v int64) *int64 {
	return &v
}
