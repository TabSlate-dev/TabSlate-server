package model

// Plan represents a user subscription tier.
type Plan string

const (
	PlanFree       Plan = "free"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

// User represents an authenticated user.
type User struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	IsVerified bool   `json:"is_verified"`

	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Subscription holds a user's plan info.
type Subscription struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Plan      Plan   `json:"plan"`
	Status    string `json:"status"`
	ExpiresAt *int64 `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Workspace represents a logical workspace grouping collections.
type Workspace struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	Name      string  `json:"name"`
	Icon      string  `json:"icon,omitempty"`
	Color     string  `json:"color,omitempty"`
	Position  int     `json:"position"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
	Seq       int64   `json:"seq"`
	DeletedAt *int64  `json:"deleted_at,omitempty"`
}

// Collection is a folder of bookmarks inside a workspace.
type Collection struct {
	ID          string  `json:"id"`
	UserID      string  `json:"user_id"`
	WorkspaceID *string `json:"workspace_id"`
	Name        string  `json:"name"`
	Icon        string  `json:"icon,omitempty"`
	Position    int     `json:"position"`
	CreatedAt   int64   `json:"created_at"`
	UpdatedAt   int64   `json:"updated_at"`
	Seq         int64   `json:"seq"`
	DeletedAt   *int64  `json:"deleted_at,omitempty"`
	ArchivedAt  *int64  `json:"archived_at,omitempty"`
	IsDeleted   int     `json:"is_deleted"`     // 0=active 1=trashed 2=permanently deleted
	IsDefault   bool    `json:"is_default"`
}

// Bookmark is a saved URL.
type Bookmark struct {
	ID           string  `json:"id"`
	UserID       string  `json:"user_id"`
	CollectionID *string `json:"collection_id"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	FaviconURL   string  `json:"favicon_url,omitempty"`
	Description  string  `json:"description,omitempty"`
	IsFavorite   bool     `json:"is_favorite"`
	IsArchived   bool     `json:"is_archived"`
	IsTrashed    int      `json:"is_trashed"`    // 0=active 1=trashed 2=permanently deleted
	TagIDs       []string `json:"tag_ids"`
	Position     int      `json:"position"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	Seq          int64    `json:"seq"`
	DeletedAt    *int64   `json:"deleted_at,omitempty"`
}

// Tag is a label that can be applied to bookmarks.
type Tag struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Color     string `json:"color,omitempty"`
	Seq       int64  `json:"seq"`
	DeletedAt *int64 `json:"deleted_at,omitempty"`
}

// Group is a saved tab group. Tabs sync as a snapshot — no individual tab seq.
type Group struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	Color       string     `json:"color"`
	IsCompact   bool       `json:"is_compact"`
	Seq         int64      `json:"seq"`
	DeletedAt   *int64     `json:"deleted_at,omitempty"`
	IsDeleted   int        `json:"is_deleted"`    // 0=active 1=trashed 2=permanently deleted
	CreatedAt   int64      `json:"created_at"`
	UpdatedAt   int64      `json:"updated_at"`
	WorkspaceID *string    `json:"workspace_id"`
	Tabs        []GroupTab `json:"tabs"`
}

// GroupTab is a tab inside a saved group.
type GroupTab struct {
	ID       string `json:"id"`
	GroupID  string `json:"group_id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Favicon  string `json:"favicon"`
	Position int    `json:"position"`
}

// ─── Request / Response DTOs ──────────────────────────────────────────────────

type RegisterRequest struct {
	Name         string `json:"name"          binding:"required,min=1,max=100"`
	Email        string `json:"email"         binding:"required,email"`
	Password     string `json:"password"      binding:"required,min=10"`
	CaptchaToken string `json:"captcha_token"`
}

type LoginRequest struct {
	Email        string `json:"email"         binding:"required,email"`
	Password     string `json:"password"      binding:"required"`
	CaptchaToken string `json:"captcha_token"`
}

type ResendVerificationRequest struct {
	Email        string `json:"email"         binding:"required,email"`
	CaptchaToken string `json:"captcha_token"`
}

type VerifyEmailOTPRequest struct {
	Email string `json:"email" binding:"required,email"`
	Code  string `json:"code"  binding:"required,min=6,max=6"`
}

type ForgotPasswordRequest struct {
	Email        string `json:"email"         binding:"required,email"`
	CaptchaToken string `json:"captcha_token"`
}

type ResetPasswordRequest struct {
	Email       string `json:"email"        binding:"required,email"`
	Code        string `json:"code"         binding:"required,min=6,max=6"`
	NewPassword string `json:"new_password" binding:"required,min=10"`
}

type AuthResponse struct {
	User         *User  `json:"user"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type WorkspaceRequest struct {
	Name     string `json:"name"     binding:"required,min=1,max=100"`
	Icon     string `json:"icon"`
	Color    string `json:"color"`
	Position int    `json:"position"`
}

type CollectionRequest struct {
	WorkspaceID *string `json:"workspace_id"`
	Name        string  `json:"name"     binding:"required,min=1,max=100"`
	Icon        string  `json:"icon"`
	Position    int     `json:"position"`
}

type BookmarkRequest struct {
	CollectionID *string `json:"collection_id"`
	Title        string  `json:"title"       binding:"required,min=1,max=500"`
	URL          string  `json:"url"         binding:"required,url"`
	FaviconURL   string  `json:"favicon_url"`
	Description  string  `json:"description"`
	IsFavorite   bool    `json:"is_favorite"`
	IsArchived   bool    `json:"is_archived"`
	IsTrashed    bool    `json:"is_trashed"`
	Position     int     `json:"position"`
}

type TagRequest struct {
	Name  string `json:"name"  binding:"required,min=1,max=50"`
	Color string `json:"color"`
}

// ─── Sync DTOs ────────────────────────────────────────────────────────────────

// SyncEntities is the common shape for push requests and pull responses.
type SyncEntities struct {
	Workspaces  []Workspace  `json:"workspaces"`
	Collections []Collection `json:"collections"`
	Bookmarks   []Bookmark   `json:"bookmarks"`
	Tags        []Tag        `json:"tags"`
	Groups      []Group      `json:"groups"`
}

type SyncPushRequest struct {
	Entities SyncEntities `json:"entities"`
}

type SyncPushResponse struct {
	ServerSeq int64      `json:"server_seq"`
	Rejected  []Rejected `json:"rejected"`
}

type Rejected struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`          // "stale" | "quota_exceeded"
	Type   string `json:"type,omitempty"` // "collection" | "saved_group" — set when reason is "quota_exceeded"
}

type SyncPullResponse struct {
	Entities  SyncEntities `json:"entities"`
	ServerSeq int64        `json:"server_seq"`
}

// SSEToken is a short-lived token for authenticating the SSE stream.
type SSEToken struct {
	Token     string `json:"token"`
	UserID    string `json:"-"`
	ExpiresAt int64  `json:"-"`
}
