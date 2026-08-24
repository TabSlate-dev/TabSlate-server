package handler

import "testing"

func TestClassifyParent(t *testing.T) {
	tests := []struct {
		name         string
		parentID     string
		owned        map[string]struct{}
		accepted     map[string]struct{}
		unavailable  parentAvailability
		wantAccepted bool
		wantReason   string
	}{
		{name: "owned active", parentID: "owned", owned: map[string]struct{}{"owned": {}}, wantAccepted: true},
		{name: "accepted in request", parentID: "accepted", accepted: map[string]struct{}{"accepted": {}}, wantAccepted: true},
		{name: "rejected in request", parentID: "rejected", unavailable: parentAvailability{"rejected": "parent_rejected"}, wantReason: "parent_rejected"},
		{name: "soft-deleted parent", parentID: "deleted", unavailable: parentAvailability{"deleted": "parent_deleted"}, wantReason: "parent_deleted"},
		{name: "terminal parent", parentID: "terminal", unavailable: parentAvailability{"terminal": "permanently_deleted"}, wantReason: "permanently_deleted"},
		{name: "missing", parentID: "missing", wantReason: "invalid_parent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accepted, reason := classifyParent(test.parentID, test.owned, test.accepted, test.unavailable)
			if accepted != test.wantAccepted || reason != test.wantReason {
				t.Fatalf("classifyParent() = (%t, %q), want (%t, %q)", accepted, reason, test.wantAccepted, test.wantReason)
			}
		})
	}
}

func TestStaleRejection(t *testing.T) {
	rejection := staleRejection("entity-1", "bookmark")
	if rejection.ID != "entity-1" || rejection.Reason != "stale" || rejection.Type != "bookmark" {
		t.Fatalf("unexpected stale rejection: %#v", rejection)
	}
}
