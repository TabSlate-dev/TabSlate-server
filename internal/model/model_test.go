package model

import (
	"encoding/json"
	"testing"
)

func TestSyncWorkspaceMutationJSON(t *testing.T) {
	request := SyncPushRequest{
		ProtocolVersion: 2,
		Entities: SyncPushEntities{
			Workspaces: []SyncWorkspaceMutation{
				{ID: "workspace-omitted", Name: "Omitted", Position: 1, Seq: 4, CreatedAt: 10, UpdatedAt: 11},
				{ID: "workspace-deleted", Name: "Deleted", Position: 2, Seq: 5, CreatedAt: 12, UpdatedAt: 13, LifecycleAction: WorkspaceLifecycleDelete},
			},
		},
	}

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal push request: %v", err)
	}

	var encoded struct {
		ProtocolVersion int `json:"protocol_version"`
		Entities        struct {
			Workspaces []map[string]json.RawMessage `json:"workspaces"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &encoded); err != nil {
		t.Fatalf("decode marshalled request: %v", err)
	}
	if encoded.ProtocolVersion != 2 {
		t.Fatalf("protocol_version = %d, want 2", encoded.ProtocolVersion)
	}
	if _, ok := encoded.Entities.Workspaces[0]["lifecycle_action"]; ok {
		t.Fatal("omitted mutation unexpectedly contains lifecycle_action")
	}
	if got := string(encoded.Entities.Workspaces[1]["lifecycle_action"]); got != `"delete"` {
		t.Fatalf("delete lifecycle_action = %s, want \"delete\"", got)
	}

	var versionless SyncPushRequest
	if err := json.Unmarshal([]byte(`{"entities":{"workspaces":[]}}`), &versionless); err != nil {
		t.Fatalf("decode versionless request: %v", err)
	}
	if versionless.ProtocolVersion != 0 {
		t.Fatalf("versionless protocol_version = %d, want 0", versionless.ProtocolVersion)
	}
}
