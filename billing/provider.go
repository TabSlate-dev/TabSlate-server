package billing

import (
	"context"
	"net/http"
)

// Provider is the billing abstraction shared by all server editions.
//
// OSS edition uses local.Provider (License-based, no external calls).
// Cloud edition uses lago.Provider (Lago API + Stripe/Adyen checkout).
type Provider interface {
	// OnUserCreated is called immediately after a new user account is persisted.
	// OSS: no-op.
	// Cloud: creates a Lago customer and enrolls them in the free plan.
	OnUserCreated(ctx context.Context, user UserInfo) error

	// GetLimits returns the resource caps for the given user.
	// OSS: derived from License JWT; falls back to free-tier defaults.
	// Cloud: queries Lago Entitlements (with local cache).
	GetLimits(ctx context.Context, userID string) (*Limits, error)

	// GetSubscription returns the user's current subscription.
	// OSS: derived from License JWT.
	// Cloud: queries Lago.
	GetSubscription(ctx context.Context, userID string) (*Subscription, error)

	// GetCheckoutURL returns a URL to upgrade the user's plan.
	// OSS: returns an error directing the user to purchase a License.
	// Cloud: returns a Lago/Stripe checkout URL.
	GetCheckoutURL(ctx context.Context, userID string, planCode string) (string, error)

	// CancelSubscription cancels the user's active subscription.
	// OSS: returns an error (not supported).
	// Cloud: calls Lago API.
	CancelSubscription(ctx context.Context, userID string) error

	// ListInvoices returns paginated invoices for the user.
	// OSS: returns an empty slice.
	// Cloud: queries Lago.
	ListInvoices(ctx context.Context, userID string, page, perPage int) ([]Invoice, error)
}

// WebhookHandler is implemented by providers that process inbound webhook
// events from the billing platform (Cloud only).
type WebhookHandler interface {
	HandleWebhook(ctx context.Context, payload []byte, headers http.Header) error
}

// UserSyncer is an optional interface implemented by providers that support
// proactive reconciliation of user data with the billing platform.
//
// Cloud implements this so that /auth/me can silently re-sync users who
// slipped through during registration (e.g. all OnUserCreated retries failed).
// OSS does not implement this interface.
type UserSyncer interface {
	// EnsureUserSynced checks whether the user exists in the billing platform
	// and creates them if not. It is idempotent and safe to call on every
	// login/profile fetch.
	EnsureUserSynced(ctx context.Context, user UserInfo) error
}

// InstanceLimiter is implemented by providers that enforce instance-level user
// count limits. OSS local.Provider implements this; Cloud meteroid.Provider
// does not. auth.Register uses a type assertion — this is NOT part of
// billing.Provider.
type InstanceLimiter interface {
	// CheckRegistrationAllowed returns an error if registering a new user would
	// exceed the instance's licensed user count.
	CheckRegistrationAllowed(ctx context.Context) error
}
