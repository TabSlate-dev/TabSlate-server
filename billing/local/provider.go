// Package local implements billing.Provider for the OSS self-hosted edition.
// Quota decisions are based on the keygen.sh license (or free-tier defaults
// when no license key is configured). No external network calls are made in the
// hot path.
package local

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
)

var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)

// Provider is the OSS billing implementation.
type Provider struct {
	db    *db.DB
	cache *licenseCache
}

// New creates a local Provider. If licenseKey is empty the provider operates
// in free-tier mode (3 users max). KeygenAPIURL and KeygenAccountID must be set
// at build time via -ldflags when a licenseKey is supplied.
func New(licenseKey string, d *db.DB) (*Provider, error) {
	var cache *licenseCache
	if licenseKey != "" {
		if KeygenAccountID == "" {
			return nil, fmt.Errorf("billing/local: keygen account ID must be set at build time when a license key is provided")
		}
		cache = newLicenseCache(newKeygenClient(KeygenAPIURL, KeygenAccountID, licenseKey), "")
	} else {
		cache = newLicenseCache(nil, "")
	}

	return &Provider{db: d, cache: cache}, nil
}

// Start activates the current machine, performs an initial sync, and launches
// the periodic refresh loop. It must be called once before serving requests.
func (p *Provider) Start(ctx context.Context) {
	if p == nil || p.cache == nil || p.cache.client == nil {
		return
	}

	fingerprint, err := p.loadOrCreateFingerprint(ctx)
	if err != nil {
		log.Fatalf("billing/local: machine fingerprint: %v", err)
	}
	p.cache.fingerprint = fingerprint

	hostname, _ := os.Hostname()
	if err := p.cache.client.ActivateMachine(ctx, fingerprint, hostname); err != nil {
		if errors.Is(err, ErrMachineLimitExceeded) {
			log.Fatalf("billing/local: license already activated on another machine; deactivate it from the keygen.sh dashboard first")
		}
		log.Printf("billing/local: machine activation: %v (will retry on next refresh)", err)
	}

	if err := p.cache.refresh(ctx); err != nil {
		log.Printf("billing/local: initial license sync failed; using free-tier limits: %v", err)
	}
	p.enforceUserLimit(ctx)

	go func() {
		ticker := time.NewTicker(defaultSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.cache.refresh(ctx); err != nil {
					log.Printf("billing/local: license refresh: %v", err)
				}
				p.enforceUserLimit(ctx)
			}
		}
	}()
}

// loadOrCreateFingerprint reads the stable machine fingerprint from
// server_config, or persists a new UUID when none exists yet.
func (p *Provider) loadOrCreateFingerprint(ctx context.Context) (string, error) {
	if p.db == nil {
		return uuid.NewString(), nil
	}

	var fp string
	err := p.db.QueryRow(ctx, `
		SELECT value FROM server_config WHERE key = 'license_machine_fingerprint'
	`).Scan(&fp)
	if err == nil {
		return fp, nil
	}

	fp = uuid.NewString()
	_, err = p.db.Exec(ctx, `
		INSERT INTO server_config (key, value) VALUES ('license_machine_fingerprint', $1)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, fp)
	if err != nil {
		return "", err
	}
	return fp, nil
}

// CheckRegistrationAllowed returns an error when the verified-user count has
// reached the current license limit.
func (p *Provider) CheckRegistrationAllowed(ctx context.Context) error {
	if p.db == nil {
		return nil
	}

	max := p.cache.maxUsers()
	var count int
	if err := p.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM users WHERE is_verified = true
	`).Scan(&count); err != nil {
		return fmt.Errorf("billing/local: user count: %w", err)
	}
	if count >= max {
		return fmt.Errorf("user limit reached: this instance allows %d verified users", max)
	}
	return nil
}

// enforceUserLimit suspends verified users beyond the license limit and clears
// their refresh sessions. Oldest verified users are preserved first.
func (p *Provider) enforceUserLimit(ctx context.Context) {
	if p.db == nil {
		return
	}

	rows, err := p.db.Query(ctx, `
		SELECT id FROM users WHERE is_verified = true ORDER BY created_at ASC
	`)
	if err != nil {
		log.Printf("billing/local: enforceUserLimit query: %v", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("billing/local: enforceUserLimit scan: %v", err)
			return
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("billing/local: enforceUserLimit rows: %v", err)
		return
	}

	max := p.cache.maxUsers()
	now := time.Now().Unix()
	for i, id := range ids {
		if i < max {
			if _, err := p.db.Exec(ctx, `
				UPDATE users SET suspended_at = NULL WHERE id = $1 AND suspended_at IS NOT NULL
			`, id); err != nil {
				log.Printf("billing/local: unsuspend user %s: %v", id, err)
			}
			continue
		}

		if _, err := p.db.Exec(ctx, `
			UPDATE users SET suspended_at = $1 WHERE id = $2 AND suspended_at IS NULL
		`, now, id); err != nil {
			log.Printf("billing/local: suspend user %s: %v", id, err)
			continue
		}
		if _, err := p.db.Exec(ctx, `
			DELETE FROM refresh_tokens WHERE user_id = $1
		`, id); err != nil {
			log.Printf("billing/local: revoke refresh tokens for %s: %v", id, err)
		}
	}
}

// OnUserCreated is a no-op for the OSS edition.
func (p *Provider) OnUserCreated(_ context.Context, _ billing.UserInfo) error {
	return nil
}

// GetLimits returns the OSS resource caps. User-count enforcement is handled
// separately via InstanceLimiter.
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

// GetSubscription returns the plan inferred from the cached keygen license.
func (p *Provider) GetSubscription(_ context.Context, _ string) (*billing.Subscription, error) {
	if p.cache.client != nil {
		p.cache.mu.RLock()
		status := p.cache.data.Status
		expiry := p.cache.data.Expiry
		p.cache.mu.RUnlock()

		if status == "ACTIVE" && (expiry == nil || time.Now().Before(*expiry)) {
			return &billing.Subscription{Plan: billing.PlanPro, Status: "active"}, nil
		}
	}
	return &billing.Subscription{Plan: billing.PlanFree, Status: "active"}, nil
}

// GetCheckoutURL is not supported in the OSS edition.
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
