package handler

import "testing"

func testStringPointer(value string) *string {
	return &value
}

func TestClassifyParent(t *testing.T) {
	tests := []struct {
		name        string
		parentID    *string
		allowNil    bool
		owned       entityIDSet
		accepted    entityIDSet
		unavailable entityIDSet
		wantReason  string
	}{
		{name: "owned", parentID: testStringPointer("owned"), owned: entityIDSet{"owned": {}}, wantReason: ""},
		{name: "accepted", parentID: testStringPointer("accepted"), accepted: entityIDSet{"accepted": {}}, wantReason: ""},
		{name: "rejected", parentID: testStringPointer("rejected"), unavailable: entityIDSet{"rejected": {}}, wantReason: "parent_rejected"},
		{name: "missing", parentID: testStringPointer("missing"), wantReason: "invalid_parent"},
		{name: "allowed nil", parentID: nil, allowNil: true, wantReason: ""},
		{name: "required nil", parentID: nil, allowNil: false, wantReason: "invalid_parent"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rejection := classifyParent(
				"child-1", "collection", test.parentID, "workspace", test.allowNil,
				test.owned, test.accepted, test.unavailable,
			)
			if test.wantReason == "" {
				if rejection != nil {
					t.Fatalf("expected accepted parent, got %#v", rejection)
				}
				return
			}
			if rejection == nil || rejection.Reason != test.wantReason {
				t.Fatalf("rejection = %#v, want %q", rejection, test.wantReason)
			}
			if rejection.ID != "child-1" || rejection.Type != "collection" || rejection.ParentType != "workspace" {
				t.Fatalf("unexpected context: %#v", rejection)
			}
		})
	}
}
