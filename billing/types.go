package billing

// UserInfo carries the minimal user data needed by the billing provider
// when a new account is created.
type UserInfo struct {
	ID    string
	Name  string
	Email string
}

// Plan represents a subscription tier.
type Plan string

const (
	PlanFree       Plan = "free"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise" // OSS self-hosted enterprise license
)

// Limits defines the resource caps for a plan. -1 means unlimited.
type Limits struct {
	MaxWorkspaces  int `json:"max_workspaces"`
	MaxBookmarks   int `json:"max_bookmarks"`
	MaxCollections int `json:"max_collections"`
	MaxTags        int `json:"max_tags"`
}

// Subscription holds the user's current subscription state.
type Subscription struct {
	Plan      Plan   `json:"plan"`
	Status    string `json:"status"`     // active | canceled | past_due
	ExpiresAt *int64 `json:"expires_at"` // unix timestamp; nil = never
}

// Invoice represents a billing invoice.
type Invoice struct {
	ID       string `json:"id"`
	Amount   int    `json:"amount_cents"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
	IssuedAt int64  `json:"issued_at"`
	PdfURL   string `json:"pdf_url,omitempty"`
}
