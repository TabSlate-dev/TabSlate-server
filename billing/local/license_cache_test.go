package local

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestLicenseCache_maxUsers_freeTier(t *testing.T) {
	c := newLicenseCache(nil, "")
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("maxUsers() = %d, want %d", got, defaultFreeUsers)
	}
}

func TestLicenseCache_maxUsers_activeWithLimit(t *testing.T) {
	c := newLicenseCache(nil, "")
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 25}
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != 25 {
		t.Errorf("maxUsers() = %d, want 25", got)
	}
}

func TestLicenseCache_maxUsers_expiredFallsBackToFree(t *testing.T) {
	c := newLicenseCache(nil, "")
	past := time.Now().Add(-time.Hour)
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 25, Expiry: &past}
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("expired license should return %d, got %d", defaultFreeUsers, got)
	}
}

func TestLicenseCache_maxUsers_suspendedFallsBackToFree(t *testing.T) {
	c := newLicenseCache(nil, "")
	c.data = keygenLicense{Status: "SUSPENDED", MaxUsers: 25}
	c.client = &keygenClient{}
	if got := c.maxUsers(); got != defaultFreeUsers {
		t.Errorf("suspended license should return %d, got %d", defaultFreeUsers, got)
	}
}

func TestLicenseCache_refresh_updatesCache(t *testing.T) {
	client := newKeygenClient("https://keygen.test", "acct", "lic")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := &responseRecorder{header: make(http.Header)}
		if r.Method == http.MethodGet && r.URL.Path != "" {
			if len(r.URL.Query().Get("fingerprint")) > 0 {
				rec.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(rec).Encode(map[string]any{
					"data": []map[string]any{{"id": "m1"}},
				})
				return rec.Response(), nil
			}
			rec.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(rec).Encode(map[string]any{
				"data": map[string]any{
					"attributes": map[string]any{
						"status":   "ACTIVE",
						"expiry":   nil,
						"metadata": map[string]any{"max_users": float64(15)},
					},
				},
			})
		}
		return rec.Response(), nil
	})
	c := newLicenseCache(client, "fp-test")

	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh error: %v", err)
	}
	if got := c.maxUsers(); got != 15 {
		t.Errorf("after refresh maxUsers() = %d, want 15", got)
	}
}
