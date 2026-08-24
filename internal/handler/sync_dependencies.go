package handler

import "github.com/TabSlate-dev/TabSlate-server/internal/model"

type entityIDSet map[string]struct{}
type parentAvailability map[string]string

type retainedQuota struct {
	limit             int
	count             int
	retainedUpdatedAt map[string]int64
	seen              entityIDSet
}

func newRetainedQuota(limit int) *retainedQuota {
	return &retainedQuota{
		limit:             limit,
		retainedUpdatedAt: map[string]int64{},
		seen:              entityIDSet{},
	}
}

func (quota *retainedQuota) AddRetained(id string, updatedAt int64) {
	quota.retainedUpdatedAt[id] = updatedAt
	quota.count++
}

func (quota *retainedQuota) Admit(id string, terminal bool, now int64) bool {
	if quota.limit == -1 {
		return true
	}
	if quota.seen.Has(id) {
		return true
	}

	if terminal {
		if updatedAt, exists := quota.retainedUpdatedAt[id]; exists && updatedAt < now {
			delete(quota.retainedUpdatedAt, id)
			quota.count--
		}
		quota.seen.Add(id)
		return true
	}

	if _, exists := quota.retainedUpdatedAt[id]; exists {
		quota.seen.Add(id)
		return true
	}
	if quota.count >= quota.limit {
		return false
	}
	quota.retainedUpdatedAt[id] = now
	quota.count++
	quota.seen.Add(id)
	return true
}

func (quota *retainedQuota) ReleaseApplied(id string) {
	if quota.limit == -1 {
		return
	}
	if _, exists := quota.retainedUpdatedAt[id]; !exists {
		return
	}
	delete(quota.retainedUpdatedAt, id)
	quota.count--
}

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
