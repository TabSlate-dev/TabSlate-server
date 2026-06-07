// Package local implements billing.Provider for the OSS self-hosted edition.
// All resource limits are unlimited by default. Subscription always reports
// PlanPro — self-hosters have full feature access.
package local

import (
	"context"
	"fmt"

	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
)

var _ billing.Provider = (*Provider)(nil)

// Provider is the OSS billing implementation.
type Provider struct {
	db *db.DB
}

// New creates a local Provider. Pass nil for db only in tests.
func New(d *db.DB) *Provider {
	return &Provider{db: d}
}

// OnUserCreated is a no-op for the OSS edition.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error {
	return nil
}

// GetLimits returns the OSS resource caps from subscription_capacity, falling
// back to unlimited defaults when the row is absent or db is nil.
func (p *Provider) GetLimits(ctx context.Context, _ string) (*billing.Limits, error) {
	if p.db != nil {
		var l billing.Limits
		err := p.db.QueryRow(ctx, `
			SELECT max_workspaces, max_bookmarks, max_collections, max_tags, max_saved_groups, trash_grace_days
			FROM subscription_capacity WHERE plan_code = 'unlimited'
		`).Scan(&l.MaxWorkspaces, &l.MaxBookmarks, &l.MaxCollections, &l.MaxTags, &l.MaxSavedGroups, &l.TrashGraceDays)
		if err == nil {
			return &l, nil
		}
	}
	return unlimitedLimits(), nil
}

// GetSubscription always returns PlanPro for the OSS edition — self-hosters
// have full feature access without a license key.
func (p *Provider) GetSubscription(_ context.Context, _ string) (*billing.Subscription, error) {
	return &billing.Subscription{Plan: billing.PlanPro, Status: "active"}, nil
}

// ChangePlan is not supported in the OSS edition.
func (p *Provider) ChangePlan(_ context.Context, _, _ string) error {
	return fmt.Errorf(
		"online plan change is not available in the OSS edition; " +
			"visit https://tabslate.com/pricing to purchase a license",
	)
}

// CancelSubscription is not supported in the OSS edition.
func (p *Provider) CancelSubscription(_ context.Context, _ string) error {
	return fmt.Errorf("subscription management is not available in the OSS edition")
}

// ListInvoices returns an empty slice for the OSS edition.
func (p *Provider) ListInvoices(_ context.Context, _ string, _, _ int) ([]billing.Invoice, error) {
	return nil, nil
}

func unlimitedLimits() *billing.Limits {
	return &billing.Limits{
		MaxWorkspaces:  -1,
		MaxBookmarks:   -1,
		MaxCollections: -1,
		MaxTags:        -1,
		MaxSavedGroups: -1,
		TrashGraceDays: -1,
	}
}
