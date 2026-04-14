package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/auth"
	"github.com/tabslate/server/internal/captcha"
	"github.com/tabslate/server/internal/mailer"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
)

const (
	// loginFailureThreshold is the number of recent failures (within the
	// window) after which the login endpoint requires a captcha token.
	loginFailureThreshold = 5

	// loginFailureWindow is how far back we look for login failures.
	loginFailureWindow = 15 * time.Minute

	// verificationTokenTTL is how long an email verification token is valid.
	verificationTokenTTL = 24 * time.Hour
)

type AuthHandler struct {
	db      *db.DB
	secret  string
	billing billing.Provider
	captcha *captcha.Verifier
	mailer  *mailer.Mailer

	// verifyBaseURL is the base URL for building the verification link.
	// e.g. "https://api.tabslate.app" → link becomes "<base>/auth/verify-email?token=xxx"
	verifyBaseURL string
}

func NewAuthHandler(d *db.DB, secret string, bp billing.Provider, cv *captcha.Verifier, m *mailer.Mailer, verifyBaseURL string) *AuthHandler {
	return &AuthHandler{
		db:            d,
		secret:        secret,
		billing:       bp,
		captcha:       cv,
		mailer:        m,
		verifyBaseURL: verifyBaseURL,
	}
}

// POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// ── Step 1: Captcha verification (must be first to prevent DB abuse) ─────
	if err := h.captcha.Verify(ctx, req.CaptchaToken); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "captcha verification failed"})
		return
	}

	// ── Step 2: Password strength ────────────────────────────────────────────
	if err := validatePasswordStrength(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ── Step 3: Email uniqueness ─────────────────────────────────────────────
	var count int
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE email = $1`, req.Email).Scan(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	userID := uuid.NewString()
	now := time.Now().Unix()

	// ── Step 4: Generate verification token ──────────────────────────────────
	verifyToken := generateVerificationToken()
	verifyExpires := time.Now().Add(verificationTokenTTL).Unix()

	// If email is disabled, auto-verify the user.
	isVerified := !h.mailer.Enabled()

	if _, err := h.db.Exec(ctx,
		`INSERT INTO users (id, name, email, password_hash, is_verified, verification_token, verification_expires_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		userID, req.Name, req.Email, hash, isVerified, verifyToken, verifyExpires, now, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	h.db.Exec(ctx,
		`INSERT INTO subscriptions (id, user_id, plan, status, created_at, updated_at) VALUES ($1,$2,'free','active',$3,$4)`,
		uuid.NewString(), userID, now, now,
	)
	h.db.Exec(ctx,
		`INSERT INTO workspaces (id, user_id, name, position, created_at, updated_at) VALUES ($1,$2,'My Workspace',0,$3,$4)`,
		uuid.NewString(), userID, now, now,
	)

	// ── Step 5: Send verification email or sync to Lago immediately ─────────
	if h.mailer.Enabled() {
		go h.sendVerificationEmail(req.Email, req.Name, verifyToken)
		// billing.OnUserCreated is deferred until email is verified.
	} else {
		// No email provider → user is auto-verified → sync to Lago now.
		go h.billing.OnUserCreated(context.Background(), billing.UserInfo{ID: userID, Name: req.Name, Email: req.Email})
	}

	user := &model.User{ID: userID, Name: req.Name, Email: req.Email, IsVerified: isVerified, CreatedAt: now, UpdatedAt: now}
	resp, err := h.issueTokens(user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue tokens"})
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// POST /auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// ── Conditional captcha: require if too many recent failures for this email ──
	if h.captcha.Enabled() {
		if h.loginFailureCount(ctx, req.Email) >= loginFailureThreshold {
			if err := h.captcha.Verify(ctx, req.CaptchaToken); err != nil {
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":            "too many failed attempts, captcha required",
					"captcha_required": true,
				})
				return
			}
		}
	}

	var user model.User
	err := h.db.QueryRow(ctx,
		`SELECT id, name, email, password_hash, is_verified, created_at, updated_at FROM users WHERE email = $1`,
		req.Email,
	).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		h.recordLoginFailure(ctx, req.Email)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		h.recordLoginFailure(ctx, req.Email)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	// Clear failures on success.
	h.db.Exec(ctx, `DELETE FROM login_failures WHERE email = $1`, req.Email)

	resp, err := h.issueTokens(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GET /auth/verify-email?token=xxx
func (h *AuthHandler) VerifyEmail(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	ctx := c.Request.Context()
	now := time.Now().Unix()

	var userID, name, email string
	err := h.db.QueryRow(ctx,
		`SELECT id, name, email FROM users
		 WHERE verification_token = $1
		   AND verification_expires_at > $2
		   AND is_verified = FALSE`,
		token, now,
	).Scan(&userID, &name, &email)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired verification token"})
		return
	}

	if _, err := h.db.Exec(ctx,
		`UPDATE users SET is_verified = TRUE, verification_token = NULL, verification_expires_at = NULL, updated_at = $1 WHERE id = $2`,
		now, userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify email"})
		return
	}

	// Now that the user is verified, sync to billing (Lago).
	go h.billing.OnUserCreated(context.Background(), billing.UserInfo{ID: userID, Name: name, Email: email})

	c.JSON(http.StatusOK, gin.H{"message": "email verified successfully"})
}

// POST /auth/resend-verification
func (h *AuthHandler) ResendVerification(c *gin.Context) {
	var req model.ResendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	var userID, name string
	var isVerified bool
	err := h.db.QueryRow(ctx,
		`SELECT id, name, is_verified FROM users WHERE email = $1`, req.Email,
	).Scan(&userID, &name, &isVerified)
	if err != nil {
		// Don't reveal whether email exists — always return success.
		c.JSON(http.StatusOK, gin.H{"message": "if the email is registered, a verification link has been sent"})
		return
	}

	if isVerified {
		c.JSON(http.StatusOK, gin.H{"message": "if the email is registered, a verification link has been sent"})
		return
	}

	// Generate new token.
	verifyToken := generateVerificationToken()
	verifyExpires := time.Now().Add(verificationTokenTTL).Unix()
	now := time.Now().Unix()

	h.db.Exec(ctx,
		`UPDATE users SET verification_token = $1, verification_expires_at = $2, updated_at = $3 WHERE id = $4`,
		verifyToken, verifyExpires, now, userID,
	)

	go h.sendVerificationEmail(req.Email, name, verifyToken)

	c.JSON(http.StatusOK, gin.H{"message": "if the email is registered, a verification link has been sent"})
}

// POST /auth/refresh
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	tokenHash := auth.HashRefreshToken(req.RefreshToken)

	var userID string
	var expiresAt int64
	err := h.db.QueryRow(ctx,
		`SELECT user_id, expires_at FROM refresh_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&userID, &expiresAt)
	if err != nil || time.Now().Unix() > expiresAt {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	h.db.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash)

	var user model.User
	h.db.QueryRow(ctx,
		`SELECT id, name, email, is_verified, created_at, updated_at FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt)

	resp, err := h.issueTokens(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err == nil {
		tokenHash := auth.HashRefreshToken(req.RefreshToken)
		h.db.Exec(c.Request.Context(), `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)

	var user model.User
	err := h.db.QueryRow(ctx,
		`SELECT id, name, email, is_verified, created_at, updated_at FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	sub, err := h.billing.GetSubscription(ctx, userID)
	if err != nil {
		var plan, status string
		h.db.QueryRow(ctx,
			`SELECT plan, status FROM subscriptions WHERE user_id = $1`, userID,
		).Scan(&plan, &status)
		if plan == "" {
			plan = string(model.PlanFree)
		}
		c.JSON(http.StatusOK, gin.H{"user": user, "subscription": gin.H{"plan": plan, "status": status}})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": user, "subscription": sub})
}

// GET /auth/login-captcha-status?email=xxx
// Returns whether captcha is required for a given email (used by frontend).
func (h *AuthHandler) LoginCaptchaStatus(c *gin.Context) {
	email := c.Query("email")
	required := false
	if email != "" && h.captcha.Enabled() {
		required = h.loginFailureCount(c.Request.Context(), email) >= loginFailureThreshold
	}
	c.JSON(http.StatusOK, gin.H{"captcha_required": required})
}

// ─── Private helpers ─────────────────────────────────────────────────────────

func (h *AuthHandler) issueTokens(user *model.User) (*model.AuthResponse, error) {
	accessToken, err := auth.SignAccessToken(user.ID, h.secret)
	if err != nil {
		return nil, err
	}

	rawRefresh, hashRefresh, err := auth.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(auth.RefreshTokenTTL).Unix()
	if _, err := h.db.Exec(context.Background(),
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		uuid.NewString(), user.ID, hashRefresh, expiresAt,
	); err != nil {
		return nil, err
	}

	return &model.AuthResponse{
		User:         user,
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	}, nil
}

// loginFailureCount returns the number of recent login failures for the email.
func (h *AuthHandler) loginFailureCount(ctx context.Context, email string) int {
	cutoff := time.Now().Add(-loginFailureWindow).Unix()
	var count int
	h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM login_failures WHERE email = $1 AND failed_at > $2`,
		email, cutoff,
	).Scan(&count)
	return count
}

// recordLoginFailure inserts a login failure row for rate-limiting/captcha purposes.
func (h *AuthHandler) recordLoginFailure(ctx context.Context, email string) {
	now := time.Now().Unix()
	h.db.Exec(ctx, `INSERT INTO login_failures (email, failed_at) VALUES ($1, $2)`, email, now)
}

// sendVerificationEmail sends the verification email asynchronously.
func (h *AuthHandler) sendVerificationEmail(to, name, token string) {
	link := fmt.Sprintf("%s/auth/verify-email?token=%s", h.verifyBaseURL, token)
	subject := "Verify your TabSlate email"
	body := fmt.Sprintf(`<html><body>
<p>Hi %s,</p>
<p>Thanks for signing up for TabSlate! Please verify your email by clicking the link below:</p>
<p><a href="%s">Verify Email</a></p>
<p>This link expires in 24 hours.</p>
<p>If you didn't create an account, you can safely ignore this email.</p>
</body></html>`, name, link)

	if err := h.mailer.Send(context.Background(), to, subject, body); err != nil {
		log.Printf("failed to send verification email to %s: %v", to, err)
	}
}

// generateVerificationToken returns a 32-byte hex-encoded random token.
func generateVerificationToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}

// validatePasswordStrength enforces minimum password requirements:
// at least 10 characters, at least one letter, at least one digit.
func validatePasswordStrength(password string) error {
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}
	var hasLetter, hasDigit bool
	for _, ch := range password {
		if unicode.IsLetter(ch) {
			hasLetter = true
		}
		if unicode.IsDigit(ch) {
			hasDigit = true
		}
	}
	if !hasLetter {
		return fmt.Errorf("password must contain at least one letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}
	return nil
}
