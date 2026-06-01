package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log"
	"math/big"
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
	"github.com/tabslate/server/internal/ratelimit"
	"github.com/tabslate/server/internal/store"
)

const (
	// loginFailureThreshold is the number of recent failures (within the
	// window) after which the login endpoint requires a captcha token.
	loginFailureThreshold = 5

	// loginFailureWindow is how far back we look for login failures.
	loginFailureWindow = 15 * time.Minute

	// otpTTL is how long a 6-digit OTP is valid (email verification or reset).
	otpTTL = 10 * time.Minute

	// otpEmailCooldown is the minimum interval between OTP emails to the same address.
	otpEmailCooldown = 60 * time.Second

)

type AuthHandler struct {
	db      *db.DB
	secret  string
	billing billing.Provider
	captcha *captcha.Verifier
	mailer  *mailer.Mailer
	limiter ratelimit.Limiter
	cache   store.Cache

	registerCaptchaThreshold int
	registerCaptchaWindow    time.Duration

	otpCaptchaThreshold int
	otpCaptchaWindow    time.Duration
}

func NewAuthHandler(
	d *db.DB,
	secret string,
	bp billing.Provider,
	cv *captcha.Verifier,
	m *mailer.Mailer,
	l ratelimit.Limiter,
	cache store.Cache,
	registerThreshold int,
	registerWindow time.Duration,
	otpThreshold int,
	otpWindow time.Duration,
) *AuthHandler {
	return &AuthHandler{
		db:                       d,
		secret:                   secret,
		billing:                  bp,
		captcha:                  cv,
		mailer:                   m,
		limiter:                  l,
		cache:                    cache,
		registerCaptchaThreshold: registerThreshold,
		registerCaptchaWindow:    registerWindow,
		otpCaptchaThreshold:      otpThreshold,
		otpCaptchaWindow:         otpWindow,
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

	// ── Step 1: Captcha verification (guards all DB writes below) ───────────
	// Captcha is required once the IP has already registered at least
	// registerCaptchaThreshold accounts within registerCaptchaWindow.
	// A threshold of 0 means captcha is always required.
	ip := c.ClientIP()
	if h.captcha.Enabled() && h.registerIPRequestCount(ctx, ip) >= h.registerCaptchaThreshold {
		if err := h.captcha.Verify(ctx, req.CaptchaToken); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "captcha verification failed"})
			return
		}
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

	if il, ok := h.billing.(billing.InstanceLimiter); ok {
		if err := il.CheckRegistrationAllowed(ctx); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	userID := uuid.NewString()
	now := time.Now().Unix()

	// ── Step 4: Generate email verification OTP ───────────────────────────────
	otp := generateOTP()
	otpHash := hashOTP(otp)
	otpExpires := time.Now().Add(otpTTL).Unix()

	// If mailer is disabled, auto-verify the user immediately.
	isVerified := !h.mailer.Enabled()

	// Set otp_last_sent_at only when we actually send an email, so that the
	// per-email 60-second cooldown applies immediately after registration and
	// the resend endpoint cannot be called without the cooldown being enforced.
	var otpLastSentAt interface{}
	if h.mailer.Enabled() {
		otpLastSentAt = now
	}

	if _, err := h.db.Exec(ctx,
		`INSERT INTO users (id, name, email, password_hash, is_verified, verification_token, verification_expires_at, otp_last_sent_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		userID, req.Name, req.Email, hash, isVerified, otpHash, otpExpires, otpLastSentAt, now, now,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	h.db.Exec(ctx,
		`INSERT INTO subscriptions (id, user_id, plan, status, created_at, updated_at) VALUES ($1,$2,'free','active',$3,$4)`,
		uuid.NewString(), userID, now, now,
	)
	h.db.Exec(ctx,
		`INSERT INTO user_sync_seq (user_id, seq) VALUES ($1, 0) ON CONFLICT DO NOTHING`,
		userID,
	)

	// Record this registration for per-IP captcha threshold tracking.
	h.recordRegisterIPRequest(ctx, ip)

	// ── Step 5: Send OTP email or sync to billing immediately ────────────────
	if h.mailer.Enabled() {
		go h.sendOTPEmail(req.Email, req.Name, otp, "verify")
		// billing.OnUserCreated is deferred until email is verified.
	} else {
		// No email provider → user is auto-verified → sync to billing now.
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
		`SELECT id, name, email, password_hash, is_verified, created_at, updated_at, suspended_at FROM users WHERE email = $1`,
		req.Email,
	).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt, &user.SuspendedAt)
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
	h.limiter.ResetCounter(ctx, "tabslate:auth:login_fail:"+req.Email)

	if user.SuspendedAt != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "account suspended: instance user limit exceeded"})
		return
	}

	// If the user hasn't verified their email yet, refresh the OTP so they
	// always have a fresh code when they arrive at the verification screen.
	// The per-email cooldown still applies to avoid duplicate sends when the
	// user logs in repeatedly in quick succession.
	if !user.IsVerified && h.mailer.Enabled() {
		var lastSentAt int64
		h.db.QueryRow(ctx,
			`SELECT COALESCE(otp_last_sent_at, 0) FROM users WHERE id = $1`, user.ID,
		).Scan(&lastSentAt)

		if wait := int64(otpEmailCooldown.Seconds()) - (time.Now().Unix() - lastSentAt); wait <= 0 {
			otp := generateOTP()
			otpHash := hashOTP(otp)
			otpExpires := time.Now().Add(otpTTL).Unix()
			loginNow := time.Now().Unix()
			h.db.Exec(ctx,
				`UPDATE users SET verification_token = $1, verification_expires_at = $2, otp_last_sent_at = $3, verification_attempts = 0, updated_at = $3 WHERE id = $4`,
				otpHash, otpExpires, loginNow, user.ID,
			)
			go h.sendOTPEmail(user.Email, user.Name, otp, "verify")
		}
	}

	resp, err := h.issueTokens(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

const otpMaxAttempts = 5

// POST /auth/verify-email  { email, code }
func (h *AuthHandler) VerifyEmail(c *gin.Context) {
	var req model.VerifyEmailOTPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	now := time.Now().Unix()

	// Fetch the unverified user's OTP state (regardless of code correctness).
	var userID, name, storedHash string
	var otpExpires, attempts int64
	err := h.db.QueryRow(ctx,
		`SELECT id, name, COALESCE(verification_token,''), COALESCE(verification_expires_at,0), verification_attempts
		 FROM users
		 WHERE email = $1 AND is_verified = FALSE`,
		req.Email,
	).Scan(&userID, &name, &storedHash, &otpExpires, &attempts)
	if err != nil || storedHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired verification code"})
		return
	}

	// Already invalidated due to prior exhausted attempts.
	if attempts >= otpMaxAttempts {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many failed attempts, please request a new code"})
		return
	}

	// Expired.
	if now > otpExpires {
		c.JSON(http.StatusBadRequest, gin.H{"error": "verification code has expired, please request a new one"})
		return
	}

	// Wrong code — increment counter, invalidate after reaching the limit.
	if hashOTP(req.Code) != storedHash {
		newAttempts := attempts + 1
		if newAttempts >= otpMaxAttempts {
			h.db.Exec(ctx,
				`UPDATE users SET verification_token = NULL, verification_expires_at = NULL, verification_attempts = 0, updated_at = $1 WHERE id = $2`,
				now, userID,
			)
			c.JSON(http.StatusBadRequest, gin.H{"error": "too many failed attempts, please request a new code"})
		} else {
			h.db.Exec(ctx,
				`UPDATE users SET verification_attempts = $1, updated_at = $2 WHERE id = $3`,
				newAttempts, now, userID,
			)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid verification code"})
		}
		return
	}

	// Correct — mark verified and clear OTP state.
	if _, err := h.db.Exec(ctx,
		`UPDATE users SET is_verified = TRUE, verification_token = NULL, verification_expires_at = NULL, verification_attempts = 0, updated_at = $1 WHERE id = $2`,
		now, userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify email"})
		return
	}

	go h.billing.OnUserCreated(context.Background(), billing.UserInfo{ID: userID, Name: name, Email: req.Email})

	c.JSON(http.StatusOK, gin.H{"message": "email verified successfully"})
}

// POST /auth/resend-verification  { email, captcha_token? }
func (h *AuthHandler) ResendVerification(c *gin.Context) {
	var req model.ResendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ip := c.ClientIP()

	// ── Per-IP captcha check ──────────────────────────────────────────────────
	if h.otpIPRequestCount(ctx, ip) >= h.otpCaptchaThreshold {
		if err := h.captcha.Verify(ctx, req.CaptchaToken); err != nil {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":            "captcha required",
				"captcha_required": true,
			})
			return
		}
	}

	var userID, name string
	var isVerified bool
	var lastSentAt int64
	err := h.db.QueryRow(ctx,
		`SELECT id, name, is_verified, COALESCE(otp_last_sent_at, 0) FROM users WHERE email = $1`, req.Email,
	).Scan(&userID, &name, &isVerified, &lastSentAt)
	if err != nil || isVerified {
		h.recordOTPIPRequest(ctx, ip)
		c.JSON(http.StatusOK, gin.H{"message": "if the email is registered and unverified, a new code has been sent"})
		return
	}

	// ── Per-email cooldown check ──────────────────────────────────────────────
	if wait := int64(otpEmailCooldown.Seconds()) - (time.Now().Unix() - lastSentAt); wait > 0 {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "please wait before requesting another code",
			"retry_after": wait,
		})
		return
	}

	otp := generateOTP()
	otpHash := hashOTP(otp)
	otpExpires := time.Now().Add(otpTTL).Unix()
	now := time.Now().Unix()

	h.db.Exec(ctx,
		`UPDATE users SET verification_token = $1, verification_expires_at = $2, otp_last_sent_at = $3, verification_attempts = 0, updated_at = $3 WHERE id = $4`,
		otpHash, otpExpires, now, userID,
	)
	h.recordOTPIPRequest(ctx, ip)

	go h.sendOTPEmail(req.Email, name, otp, "verify")

	c.JSON(http.StatusOK, gin.H{"message": "if the email is registered and unverified, a new code has been sent"})
}

// POST /auth/forgot-password  { email, captcha_token? }
func (h *AuthHandler) ForgotPassword(c *gin.Context) {
	var req model.ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	ip := c.ClientIP()

	// ── Per-IP captcha check ──────────────────────────────────────────────────
	if h.otpIPRequestCount(ctx, ip) >= h.otpCaptchaThreshold {
		if err := h.captcha.Verify(ctx, req.CaptchaToken); err != nil {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":            "captcha required",
				"captcha_required": true,
			})
			return
		}
	}

	var userID, name string
	var lastSentAt int64
	err := h.db.QueryRow(ctx,
		`SELECT id, name, COALESCE(otp_last_sent_at, 0) FROM users WHERE email = $1`, req.Email,
	).Scan(&userID, &name, &lastSentAt)
	if err != nil {
		// Don't reveal whether the email exists — still record IP request.
		h.recordOTPIPRequest(ctx, ip)
		c.JSON(http.StatusOK, gin.H{"message": "if the email is registered, a reset code has been sent"})
		return
	}

	// ── Per-email cooldown check ──────────────────────────────────────────────
	if wait := int64(otpEmailCooldown.Seconds()) - (time.Now().Unix() - lastSentAt); wait > 0 {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "please wait before requesting another code",
			"retry_after": wait,
		})
		return
	}

	otp := generateOTP()
	otpHash := hashOTP(otp)
	otpExpires := time.Now().Add(otpTTL).Unix()
	now := time.Now().Unix()

	h.db.Exec(ctx,
		`UPDATE users SET reset_otp_hash = $1, reset_otp_expires_at = $2, otp_last_sent_at = $3, reset_attempts = 0, updated_at = $3 WHERE id = $4`,
		otpHash, otpExpires, now, userID,
	)
	h.recordOTPIPRequest(ctx, ip)

	go h.sendOTPEmail(req.Email, name, otp, "reset")

	c.JSON(http.StatusOK, gin.H{"message": "if the email is registered, a reset code has been sent"})
}

// GET /auth/otp-captcha-status
// Returns whether the current IP has exceeded the OTP request threshold.
func (h *AuthHandler) OTPCaptchaStatus(c *gin.Context) {
	required := h.otpIPRequestCount(c.Request.Context(), c.ClientIP()) >= h.otpCaptchaThreshold
	c.JSON(http.StatusOK, gin.H{"captcha_required": required})
}

// POST /auth/reset-password  { email, code, new_password }
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req model.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := validatePasswordStrength(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	now := time.Now().Unix()

	// Fetch reset OTP state regardless of code correctness.
	var userID, storedHash string
	var otpExpires, attempts int64
	err := h.db.QueryRow(ctx,
		`SELECT id, COALESCE(reset_otp_hash,''), COALESCE(reset_otp_expires_at,0), reset_attempts
		 FROM users WHERE email = $1`,
		req.Email,
	).Scan(&userID, &storedHash, &otpExpires, &attempts)
	if err != nil || storedHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or expired reset code"})
		return
	}

	if attempts >= otpMaxAttempts {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many failed attempts, please request a new code"})
		return
	}

	if now > otpExpires {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reset code has expired, please request a new one"})
		return
	}

	if hashOTP(req.Code) != storedHash {
		newAttempts := attempts + 1
		if newAttempts >= otpMaxAttempts {
			h.db.Exec(ctx,
				`UPDATE users SET reset_otp_hash = NULL, reset_otp_expires_at = NULL, reset_attempts = 0, updated_at = $1 WHERE id = $2`,
				now, userID,
			)
			c.JSON(http.StatusBadRequest, gin.H{"error": "too many failed attempts, please request a new code"})
		} else {
			h.db.Exec(ctx,
				`UPDATE users SET reset_attempts = $1, updated_at = $2 WHERE id = $3`,
				newAttempts, now, userID,
			)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid verification code"})
		}
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}

	if _, err := h.db.Exec(ctx,
		`UPDATE users SET password_hash = $1, reset_otp_hash = NULL, reset_otp_expires_at = NULL, reset_attempts = 0, updated_at = $2 WHERE id = $3`,
		newHash, now, userID,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset password"})
		return
	}

	h.db.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)

	c.JSON(http.StatusOK, gin.H{"message": "password reset successfully"})
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

	if _, err := h.db.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to invalidate refresh token"})
		return
	}

	var user model.User
	if err := h.db.QueryRow(ctx,
		`SELECT id, name, email, is_verified, created_at, updated_at FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.IsVerified, &user.CreatedAt, &user.UpdatedAt); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

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

	// If the provider supports proactive reconciliation (Cloud only), ensure
	// the user is synced to the billing platform. Runs in a goroutine so it
	// never adds latency to this endpoint. Handles the case where all
	// OnUserCreated retries failed during registration.
	if syncer, ok := h.billing.(billing.UserSyncer); ok && user.IsVerified {
		go func() {
			if err := syncer.EnsureUserSynced(context.Background(), billing.UserInfo{
				ID: user.ID, Name: user.Name, Email: user.Email,
			}); err != nil {
				log.Printf("EnsureUserSynced user %s: %v", user.ID, err)
			}
		}()
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

// GET /auth/register-captcha-status
// Returns whether the current IP has reached the registration threshold.
func (h *AuthHandler) RegisterCaptchaStatus(c *gin.Context) {
	required := h.captcha.Enabled() &&
		h.registerIPRequestCount(c.Request.Context(), c.ClientIP()) >= h.registerCaptchaThreshold
	c.JSON(http.StatusOK, gin.H{"captcha_required": required})
}

// GET /auth/login-captcha-status?email=xxx
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

func (h *AuthHandler) loginFailureCount(ctx context.Context, email string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:login_fail:"+email)
	return int(count)
}

func (h *AuthHandler) recordLoginFailure(ctx context.Context, email string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:login_fail:"+email, loginFailureWindow)
}

func (h *AuthHandler) otpIPRequestCount(ctx context.Context, ip string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:otp_ip:"+ip)
	return int(count)
}

func (h *AuthHandler) recordOTPIPRequest(ctx context.Context, ip string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:otp_ip:"+ip, h.otpCaptchaWindow)
}

func (h *AuthHandler) registerIPRequestCount(ctx context.Context, ip string) int {
	count, _ := h.limiter.GetCounter(ctx, "tabslate:auth:reg_ip:"+ip)
	return int(count)
}

func (h *AuthHandler) recordRegisterIPRequest(ctx context.Context, ip string) {
	h.limiter.IncrCounter(ctx, "tabslate:auth:reg_ip:"+ip, h.registerCaptchaWindow)
}

// sendOTPEmail sends a 6-digit OTP email. purpose: "verify" or "reset".
func (h *AuthHandler) sendOTPEmail(to, name, code, purpose string) {
	var subject, intro, note string
	switch purpose {
	case "reset":
		subject = "Reset your TabSlate password"
		intro = "Use the code below to reset your password. It expires in 10 minutes."
		note = "If you didn't request a password reset, you can safely ignore this email."
	default:
		subject = "Verify your TabSlate email"
		intro = "Use the code below to verify your email address. It expires in 10 minutes."
		note = "If you didn't create an account, you can safely ignore this email."
	}

	body := fmt.Sprintf(`<html><body>
<p>Hi %s,</p>
<p>%s</p>
<p style="font-size:2em;letter-spacing:0.15em;font-weight:bold;">%s</p>
<p>%s</p>
</body></html>`, name, intro, code, note)

	if err := h.mailer.Send(context.Background(), to, subject, body); err != nil {
		log.Printf("failed to send OTP email to %s: %v", to, err)
	}
}

// generateOTP returns a random 6-digit string ("100000"–"999999").
func generateOTP() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return fmt.Sprintf("%06d", n.Int64()+100000)
}

// hashOTP returns the hex-encoded SHA-256 of the OTP code.
func hashOTP(code string) string {
	h := sha256.Sum256([]byte(code))
	return fmt.Sprintf("%x", h)
}

// validatePasswordStrength enforces minimum password requirements.
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

// POST /auth/sse-token
// Issues a single-use, 30-second SSE authentication token stored in Cache.
func (h *AuthHandler) IssueSSEToken(c *gin.Context) {
	ctx := c.Request.Context()
	userID := middleware.UserID(c)
	token := uuid.NewString()

	if err := h.cache.Set(ctx, "tabslate:sse_token:"+token, []byte(userID), 30*time.Second); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue SSE token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}
