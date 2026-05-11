// Package local implements billing.Provider for the OSS self-hosted edition.
// All quota decisions are made locally from the subscription_capacity table (or
// unlimited defaults when the DB is unavailable). No external network calls are made.
package local

import (
	"context"
	"fmt"
	"log"

	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
)

// Provider is the OSS billing implementation.
type Provider struct {
	// license holds the parsed license when one is configured.
	// nil means the instance is running on the free tier.
	license *License
	db      *db.DB
}

// New creates a local Provider. If licenseKey is empty the provider operates
// in free-tier mode. publicKey is the RSA public key used to verify License
// JWTs; pass nil to skip verification (useful for tests). d is used to read
// quota limits from the subscription_capacity table; pass nil to use defaults.
func New(licenseKey string, publicKey []byte, d *db.DB) (*Provider, error) {
	p := &Provider{db: d}
	if licenseKey != "" {
		lic, err := ParseAndVerify(licenseKey, publicKey)
		if err != nil {
			return nil, fmt.Errorf("invalid license: %w", err)
		}
		p.license = lic
	}
	if d != nil {
		if err := p.seedCapacity(context.Background()); err != nil {
			log.Printf("billing/local: seed capacity: %v", err)
		}
	}
	return p, nil
}

// seedCapacity inserts the "unlimited" plan row if it does not already exist.
// Existing rows are never overwritten so operator edits are preserved.
func (p *Provider) seedCapacity(ctx context.Context) error {
	_, err := p.db.Exec(ctx, `
		INSERT INTO subscription_capacity
			(plan_code, plan_id, max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (plan_code) DO NOTHING
	`, "unlimited", "", -1, -1, -1, -1, -1, -1)
	return err
}

// OnUserCreated is a no-op for the OSS edition. Users are fully local and
// do not need to be synchronised to any external billing system.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error {
	return nil
}

// GetLimits returns the plan limits from the subscription_capacity table.
// Falls back to unlimited defaults when the DB is unavailable.
func (p *Provider) GetLimits(ctx context.Context, _ string) (*billing.Limits, error) {
	if p.db != nil {
		var l billing.Limits
		err := p.db.QueryRow(ctx, `
			SELECT max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days
			FROM subscription_capacity WHERE plan_code = $1
		`, "unlimited").Scan(&l.MaxWorkspaces, &l.MaxBookmarks, &l.MaxCollections, &l.MaxTags, &l.MaxSavedGroups, &l.TrashGraceDays)
		if err == nil {
			return &l, nil
		}
	}
	return unlimitedLimits(), nil
}

// GetSubscription returns the subscription state inferred from the License.
func (p *Provider) GetSubscription(_ context.Context, _ string) (*billing.Subscription, error) {
	if p.license != nil && p.license.Valid() {
		return &billing.Subscription{
			Plan:      p.license.Plan,
			Status:    "active",
			ExpiresAt: &p.license.ExpiresAt,
		}, nil
	}
	return &billing.Subscription{Plan: billing.PlanFree, Status: "active"}, nil
}

// GetCheckoutURL is not supported in the OSS edition. Users must purchase a
// License from the TabSlate website.
func (p *Provider) GetCheckoutURL(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf(
		"online checkout is not available in the OSS edition; " +
			"visit https://tabslate.app/pricing to purchase a license",
	)
}

// CancelSubscription is not supported in the OSS edition.
func (p *Provider) CancelSubscription(_ context.Context, _ string) error {
	return fmt.Errorf("subscription management is not available in the OSS edition")
}

// ListInvoices returns an empty slice. The OSS edition does not issue invoices.
func (p *Provider) ListInvoices(_ context.Context, _ string, _, _ int) ([]billing.Invoice, error) {
	return nil, nil
}

func unlimitedLimits() *billing.Limits {
	return &billing.Limits{MaxWorkspaces: -1, MaxBookmarks: -1, MaxCollections: -1, MaxTags: -1, MaxSavedGroups: -1, TrashGraceDays: -1}
}
