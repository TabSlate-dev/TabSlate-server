package local

import (
	"context"
	"testing"

	"github.com/TabSlate-dev/TabSlate-server/billing"
)

var _ billing.Provider = (*Provider)(nil)
var _ billing.InstanceLimiter = (*Provider)(nil)

func TestNew_returnsProvider(t *testing.T) {
	p := New(nil)
	if p == nil {
		t.Fatal("New(nil) should return a non-nil Provider")
	}
}

func TestGetSubscription_alwaysPro(t *testing.T) {
	p := New(nil)
	sub, err := p.GetSubscription(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Plan != billing.PlanPro {
		t.Errorf("plan = %q, want %q", sub.Plan, billing.PlanPro)
	}
	if sub.Status != "active" {
		t.Errorf("status = %q, want active", sub.Status)
	}
}

func TestCheckRegistrationAllowed_nilDB_allowsRegistration(t *testing.T) {
	p := New(nil)
	if err := p.CheckRegistrationAllowed(context.Background()); err != nil {
		t.Fatalf("nil DB should allow registration, got: %v", err)
	}
}

func TestGetLimits_nilDB_returnsUnlimited(t *testing.T) {
	p := New(nil)
	limits, err := p.GetLimits(context.Background(), "any-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limits.MaxBookmarks != -1 {
		t.Errorf("MaxBookmarks = %d, want -1 (unlimited)", limits.MaxBookmarks)
	}
	if limits.MaxWorkspaces != -1 {
		t.Errorf("MaxWorkspaces = %d, want -1 (unlimited)", limits.MaxWorkspaces)
	}
}
