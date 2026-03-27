package model

// Plan represents a user subscription tier.
type Plan string

const (
	PlanFree Plan = "free"
	PlanPro  Plan = "pro"
)

// User represents an authenticated user.
type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Subscription holds a user's plan info.
type Subscription struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	Plan      Plan    `json:"plan"`
	Status    string  `json:"status"`
	ExpiresAt *int64  `json:"expires_at"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// Workspace represents a logical workspace grouping collections.
type Workspace struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Icon      string `json:"icon,omitempty"`
	Color     string `json:"color,omitempty"`
	Position  int    `json:"position"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
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
	IsFavorite   bool    `json:"is_favorite"`
	IsArchived   bool    `json:"is_archived"`
	IsTrashed    bool    `json:"is_trashed"`
	Position     int     `json:"position"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

// Tag is a label that can be applied to bookmarks.
type Tag struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
}

// ─── Request / Response DTOs ──────────────────────────────────────────────────

type RegisterRequest struct {
	Name     string `json:"name"     binding:"required,min=1,max=100"`
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

type LoginRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required"`
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

// SyncPush is the payload the client sends with local changes.
type SyncPush struct {
	Workspaces  []Workspace  `json:"workspaces"`
	Collections []Collection `json:"collections"`
	Bookmarks   []Bookmark   `json:"bookmarks"`
	Tags        []Tag        `json:"tags"`
}

// SyncResponse is what the server sends back: server-side changes since `since`.
type SyncResponse struct {
	Workspaces  []Workspace  `json:"workspaces"`
	Collections []Collection `json:"collections"`
	Bookmarks   []Bookmark   `json:"bookmarks"`
	Tags        []Tag        `json:"tags"`
	ServerTime  int64        `json:"server_time"`
}
