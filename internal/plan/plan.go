package plan

import (
	"database/sql"
	"fmt"

	"github.com/lieutenant/tabmaster/internal/model"
)

// Limits defines the resource caps for a plan.
type Limits struct {
	MaxWorkspaces  int // -1 = unlimited
	MaxBookmarks   int
	MaxCollections int
	MaxTags        int
}

var limits = map[model.Plan]Limits{
	model.PlanFree: {
		MaxWorkspaces:  1,
		MaxBookmarks:   1000,
		MaxCollections: 10,
		MaxTags:        20,
	},
	model.PlanPro: {
		MaxWorkspaces:  -1,
		MaxBookmarks:   -1,
		MaxCollections: -1,
		MaxTags:        -1,
	},
}

// Get returns the limits for the given plan (defaults to Free if unknown).
func Get(p model.Plan) Limits {
	if l, ok := limits[p]; ok {
		return l
	}
	return limits[model.PlanFree]
}

// GetUserPlan fetches the user's current plan from the DB.
// Returns PlanFree if no subscription row exists.
func GetUserPlan(db *sql.DB, userID string) model.Plan {
	var p string
	err := db.QueryRow(
		`SELECT plan FROM subscriptions WHERE user_id = ? AND status = 'active'`,
		userID,
	).Scan(&p)
	if err != nil {
		return model.PlanFree
	}
	return model.Plan(p)
}

// CheckWorkspace returns an error if the user is at their workspace limit.
func CheckWorkspace(db *sql.DB, userID string) error {
	p := GetUserPlan(db, userID)
	l := Get(p)
	if l.MaxWorkspaces == -1 {
		return nil
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE user_id = ?`, userID).Scan(&count)
	if count >= l.MaxWorkspaces {
		return fmt.Errorf("free plan allows only %d workspace — upgrade to Pro for unlimited", l.MaxWorkspaces)
	}
	return nil
}

// CheckBookmark returns an error if the user is at their bookmark limit.
func CheckBookmark(db *sql.DB, userID string) error {
	p := GetUserPlan(db, userID)
	l := Get(p)
	if l.MaxBookmarks == -1 {
		return nil
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM bookmarks WHERE user_id = ? AND is_trashed = 0`, userID).Scan(&count)
	if count >= l.MaxBookmarks {
		return fmt.Errorf("free plan allows only %d bookmarks — upgrade to Pro for unlimited", l.MaxBookmarks)
	}
	return nil
}

// CheckCollection returns an error if the user is at their collection limit.
func CheckCollection(db *sql.DB, userID string) error {
	p := GetUserPlan(db, userID)
	l := Get(p)
	if l.MaxCollections == -1 {
		return nil
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM collections WHERE user_id = ?`, userID).Scan(&count)
	if count >= l.MaxCollections {
		return fmt.Errorf("free plan allows only %d collections — upgrade to Pro for unlimited", l.MaxCollections)
	}
	return nil
}

// CheckTag returns an error if the user is at their tag limit.
func CheckTag(db *sql.DB, userID string) error {
	p := GetUserPlan(db, userID)
	l := Get(p)
	if l.MaxTags == -1 {
		return nil
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM tags WHERE user_id = ?`, userID).Scan(&count)
	if count >= l.MaxTags {
		return fmt.Errorf("free plan allows only %d tags — upgrade to Pro for unlimited", l.MaxTags)
	}
	return nil
}
