package handler

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lieutenant/tabmaster/internal/auth"
	"github.com/lieutenant/tabmaster/internal/middleware"
	"github.com/lieutenant/tabmaster/internal/model"
)

type AuthHandler struct {
	db     *sql.DB
	secret string
}

func NewAuthHandler(db *sql.DB, secret string) *AuthHandler {
	return &AuthHandler{db: db, secret: secret}
}

// POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Check email uniqueness
	var exists int
	h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, req.Email).Scan(&exists)
	if exists > 0 {
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

	_, err = h.db.Exec(
		`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, req.Name, req.Email, hash, now, now,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}

	// Create default free subscription
	h.db.Exec(
		`INSERT INTO subscriptions (id, user_id, plan, status, created_at, updated_at) VALUES (?, ?, 'free', 'active', ?, ?)`,
		uuid.NewString(), userID, now, now,
	)

	// Create default workspace for free users
	h.db.Exec(
		`INSERT INTO workspaces (id, user_id, name, position, created_at, updated_at) VALUES (?, ?, 'My Workspace', 0, ?, ?)`,
		uuid.NewString(), userID, now, now,
	)

	user := &model.User{ID: userID, Name: req.Name, Email: req.Email, CreatedAt: now, UpdatedAt: now}
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

	var user model.User
	err := h.db.QueryRow(
		`SELECT id, name, email, password_hash, created_at, updated_at FROM users WHERE email = ?`,
		req.Email,
	).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	resp, err := h.issueTokens(&user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to issue tokens"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// POST /auth/refresh
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tokenHash := auth.HashRefreshToken(req.RefreshToken)
	var userID string
	var expiresAt int64
	err := h.db.QueryRow(
		`SELECT user_id, expires_at FROM refresh_tokens WHERE token_hash = ?`,
		tokenHash,
	).Scan(&userID, &expiresAt)
	if err != nil || time.Now().Unix() > expiresAt {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	// Rotate: delete old, issue new
	h.db.Exec(`DELETE FROM refresh_tokens WHERE token_hash = ?`, tokenHash)

	var user model.User
	h.db.QueryRow(
		`SELECT id, name, email, created_at, updated_at FROM users WHERE id = ?`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt, &user.UpdatedAt)

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
		h.db.Exec(`DELETE FROM refresh_tokens WHERE token_hash = ?`, tokenHash)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	userID := middleware.UserID(c)
	var user model.User
	err := h.db.QueryRow(
		`SELECT id, name, email, created_at, updated_at FROM users WHERE id = ?`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	var sub model.Subscription
	h.db.QueryRow(
		`SELECT plan, status, expires_at FROM subscriptions WHERE user_id = ?`, userID,
	).Scan(&sub.Plan, &sub.Status, &sub.ExpiresAt)
	if sub.Plan == "" {
		sub.Plan = model.PlanFree
	}

	c.JSON(http.StatusOK, gin.H{"user": user, "subscription": sub})
}

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
	_, err = h.db.Exec(
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES (?, ?, ?, ?)`,
		uuid.NewString(), user.ID, hashRefresh, expiresAt,
	)
	if err != nil {
		return nil, err
	}

	return &model.AuthResponse{
		User:         user,
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	}, nil
}
