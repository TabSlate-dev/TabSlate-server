package local

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	defaultFreeUsers    = 3
	defaultSyncInterval = time.Hour
)

type licenseCache struct {
	mu          sync.RWMutex
	data        keygenLicense
	client      *keygenClient
	fingerprint string
}

func newLicenseCache(client *keygenClient, fingerprint string) *licenseCache {
	return &licenseCache{
		client:      client,
		fingerprint: fingerprint,
	}
}

// maxUsers returns the current maximum verified-user count allowed by the
// cached license. Free-tier behavior is used when there is no active license.
func (c *licenseCache) maxUsers() int {
	if c.client == nil {
		return defaultFreeUsers
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.data.Status != "ACTIVE" {
		return defaultFreeUsers
	}
	if c.data.Expiry != nil && time.Now().After(*c.data.Expiry) {
		return defaultFreeUsers
	}
	if c.data.MaxUsers <= 0 {
		return defaultFreeUsers
	}
	return c.data.MaxUsers
}

// refresh fetches a fresh license and machine state. On any error, the
// previous cached state is preserved and the error is returned.
func (c *licenseCache) refresh(ctx context.Context) error {
	if c.client == nil {
		return nil
	}

	lic, err := c.client.FetchLicense(ctx)
	if err != nil {
		log.Printf("billing/local: license refresh: %v", err)
		return err
	}

	if c.fingerprint != "" {
		active, err := c.client.ValidateMachine(ctx, c.fingerprint)
		if err != nil {
			log.Printf("billing/local: machine validation: %v", err)
			return err
		}
		if !active {
			log.Printf("billing/local: machine deactivated via keygen.sh dashboard; treating license as revoked")
			lic.Status = "SUSPENDED"
		}
	}

	c.mu.Lock()
	c.data = *lic
	c.mu.Unlock()
	return nil
}
