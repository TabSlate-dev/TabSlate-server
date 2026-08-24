package handler

import "github.com/TabSlate-dev/TabSlate-server/internal/model"

type entityIDSet map[string]struct{}
type parentAvailability map[string]string

func staleRejection(entityID string, entityType string) model.Rejected {
	return model.Rejected{ID: entityID, Reason: "stale", Type: entityType}
}

func (set entityIDSet) Add(id string) {
	set[id] = struct{}{}
}

func (set entityIDSet) Has(id string) bool {
	_, exists := set[id]
	return exists
}

func (set entityIDSet) Delete(id string) {
	delete(set, id)
}

func (availability parentAvailability) Set(id string, reason string) {
	availability[id] = reason
}

func (availability parentAvailability) Delete(id string) {
	delete(availability, id)
}

func classifyParent(
	parentID string,
	ownedActive map[string]struct{},
	acceptedInRequest map[string]struct{},
	unavailable parentAvailability,
) (accepted bool, reason string) {
	if reason, exists := unavailable[parentID]; exists {
		return false, reason
	}
	if _, exists := ownedActive[parentID]; exists {
		return true, ""
	}
	if _, exists := acceptedInRequest[parentID]; exists {
		return true, ""
	}
	return false, "invalid_parent"
}

func classifyParentRejection(
	entityID string,
	entityType string,
	parentID *string,
	parentType string,
	allowNil bool,
	owned entityIDSet,
	accepted entityIDSet,
	unavailable parentAvailability,
) *model.Rejected {
	if parentID == nil {
		if allowNil {
			return nil
		}
		return &model.Rejected{
			ID: entityID, Reason: "invalid_parent", Type: entityType, ParentType: parentType,
		}
	}
	acceptedParent, reason := classifyParent(*parentID, owned, accepted, unavailable)
	if !acceptedParent {
		return &model.Rejected{
			ID: entityID, Reason: reason, Type: entityType,
			ParentID: *parentID, ParentType: parentType,
		}
	}
	return nil
}
