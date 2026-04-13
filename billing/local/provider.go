// Package local implements billing.Provider for the OSS self-hosted edition.
// All quota decisions are made locally from a License JWT (or free-tier
// defaults when no license is configured). No external network calls are made.
package local

import (
	"context"
	"fmt"

	"github.com/tabslate/server/billing"
)

// Provider is the OSS billing implementation.
type Provider struct {
	// license holds the parsed license when one is configured.
	// nil means the instance is running on the free tier.
	license *License
}

// New creates a local Provider. If licenseKey is empty the provider operates
// in free-tier mode. publicKey is the RSA public key used to verify License
// JWTs; pass nil to skip verification (useful for tests).
func New(licenseKey string, publicKey []byte) (*Provider, error) {
	if licenseKey == "" {
		return &Provider{}, nil
	}
	lic, err := ParseAndVerify(licenseKey, publicKey)
	if err != nil {
		return nil, fmt.Errorf("invalid license: %w", err)
	}
	return &Provider{license: lic}, nil
}

// OnUserCreated is a no-op for the OSS edition. Users are fully local and
// do not need to be synchronised to any external billing system.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error {
	return nil
}

// GetLimits returns the plan limits derived from the installed License.
// When no valid License is present the free-tier defaults apply.
func (p *Provider) GetLimits(_ context.Context, _ string) (*billing.Limits, error) {
	if p.license != nil && p.license.Valid() {
		return &p.license.Limits, nil
	}
	return freeLimits(), nil
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

func freeLimits() *billing.Limits {
	return &billing.Limits{
		MaxWorkspaces:  1,
		MaxBookmarks:   1000,
		MaxCollections: 10,
		MaxTags:        20,
	}
}
