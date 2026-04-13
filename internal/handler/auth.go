package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/auth"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
)

type AuthHandler struct {
	db      *db.DB
	secret  string
	billing billing.Provider
}

func NewAuthHandler(d *db.DB, secret string, bp billing.Provider) *AuthHandler {
	return &AuthHandler{db: d, secret: secret, billing: bp}
}

// POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
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

	if _, err := h.db.Exec(ctx,
		`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, req.Name, req.Email, hash, now, now,
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

	go h.billing.OnUserCreated(ctx, billing.UserInfo{ID: userID, Name: req.Name, Email: req.Email})

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

	ctx := c.Request.Context()
	var user model.User
	err := h.db.QueryRow(ctx,
		`SELECT id, name, email, password_hash, created_at, updated_at FROM users WHERE email = $1`,
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
		`SELECT id, name, email, created_at, updated_at FROM users WHERE id = $1`, userID,
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
		`SELECT id, name, email, created_at, updated_at FROM users WHERE id = $1`, userID,
	).Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt, &user.UpdatedAt)
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
