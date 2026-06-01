package local

import (
	"context"
	"testing"

	"github.com/tabslate/server/billing"
)

var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)

func TestNew_missingAccountID(t *testing.T) {
	original := KeygenAccountID
	KeygenAccountID = ""
	t.Cleanup(func() {
		KeygenAccountID = original
	})

	_, err := New("some-license-key", nil)
	if err == nil {
		t.Fatal("expected error when licenseKey set but KeygenAccountID empty")
	}
}

func TestNew_freeTier(t *testing.T) {
	p, err := New("", nil)
	if err != nil {
		t.Fatalf("free tier New() should not error: %v", err)
	}
	if p.cache.client != nil {
		t.Error("free tier should have nil keygen client")
	}
}

func TestCheckRegistrationAllowed_freeTierAtLimit(t *testing.T) {
	p := &Provider{
		cache: newLicenseCache(nil, ""),
	}
	if p.cache.maxUsers() != defaultFreeUsers {
		t.Errorf("expected free limit %d", defaultFreeUsers)
	}
}

func TestGetSubscription_freeTier(t *testing.T) {
	p := &Provider{cache: newLicenseCache(nil, "")}
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanFree {
		t.Errorf("plan = %q, want free", sub.Plan)
	}
}

func TestGetSubscription_activeLicense(t *testing.T) {
	c := newLicenseCache(&keygenClient{}, "fp")
	c.data = keygenLicense{Status: "ACTIVE", MaxUsers: 10}
	p := &Provider{cache: c}
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanPro {
		t.Errorf("plan = %q, want pro", sub.Plan)
	}
}
