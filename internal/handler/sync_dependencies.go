package handler

import "github.com/TabSlate-dev/TabSlate-server/internal/model"

type entityIDSet map[string]struct{}

func (set entityIDSet) Add(id string) {
	set[id] = struct{}{}
}

func (set entityIDSet) Has(id string) bool {
	_, exists := set[id]
	return exists
}

func classifyParent(
	entityID string,
	entityType string,
	parentID *string,
	parentType string,
	allowNil bool,
	owned entityIDSet,
	accepted entityIDSet,
	unavailable entityIDSet,
) *model.Rejected {
	if parentID == nil {
		if allowNil {
			return nil
		}
		return &model.Rejected{
			ID: entityID, Reason: "invalid_parent", Type: entityType, ParentType: parentType,
		}
	}
	if unavailable.Has(*parentID) {
		return &model.Rejected{
			ID: entityID, Reason: "parent_rejected", Type: entityType,
			ParentID: *parentID, ParentType: parentType,
		}
	}
	if owned.Has(*parentID) || accepted.Has(*parentID) {
		return nil
	}
	return &model.Rejected{
		ID: entityID, Reason: "invalid_parent", Type: entityType,
		ParentID: *parentID, ParentType: parentType,
	}
}
