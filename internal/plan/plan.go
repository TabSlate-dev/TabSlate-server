package plan

import (
	"context"
	"fmt"

	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/model"
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
	model.PlanPro:        {MaxWorkspaces: -1, MaxBookmarks: -1, MaxCollections: -1, MaxTags: -1},
	model.PlanEnterprise: {MaxWorkspaces: -1, MaxBookmarks: -1, MaxCollections: -1, MaxTags: -1},
}

func Get(p model.Plan) Limits {
	if l, ok := limits[p]; ok {
		return l
	}
	return limits[model.PlanFree]
}

func GetUserPlan(ctx context.Context, d *db.DB, userID string) model.Plan {
	var p string
	d.QueryRow(ctx, `SELECT plan FROM subscriptions WHERE user_id = $1 AND status = 'active'`, userID).Scan(&p)
	if p == "" {
		return model.PlanFree
	}
	return model.Plan(p)
}

func CheckWorkspace(ctx context.Context, d *db.DB, userID string) error {
	l := Get(GetUserPlan(ctx, d, userID))
	if l.MaxWorkspaces == -1 {
		return nil
	}
	var count int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM workspaces WHERE user_id = $1`, userID).Scan(&count)
	if count >= l.MaxWorkspaces {
		return fmt.Errorf("free plan allows only %d workspace — upgrade to Pro for unlimited", l.MaxWorkspaces)
	}
	return nil
}

func CheckBookmark(ctx context.Context, d *db.DB, userID string) error {
	l := Get(GetUserPlan(ctx, d, userID))
	if l.MaxBookmarks == -1 {
		return nil
	}
	var count int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM bookmarks WHERE user_id = $1 AND is_trashed = false`, userID).Scan(&count)
	if count >= l.MaxBookmarks {
		return fmt.Errorf("free plan allows only %d bookmarks — upgrade to Pro for unlimited", l.MaxBookmarks)
	}
	return nil
}

func CheckCollection(ctx context.Context, d *db.DB, userID string) error {
	l := Get(GetUserPlan(ctx, d, userID))
	if l.MaxCollections == -1 {
		return nil
	}
	var count int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM collections WHERE user_id = $1`, userID).Scan(&count)
	if count >= l.MaxCollections {
		return fmt.Errorf("free plan allows only %d collections — upgrade to Pro for unlimited", l.MaxCollections)
	}
	return nil
}

func CheckTag(ctx context.Context, d *db.DB, userID string) error {
	l := Get(GetUserPlan(ctx, d, userID))
	if l.MaxTags == -1 {
		return nil
	}
	var count int
	d.QueryRow(ctx, `SELECT COUNT(*) FROM tags WHERE user_id = $1`, userID).Scan(&count)
	if count >= l.MaxTags {
		return fmt.Errorf("free plan allows only %d tags — upgrade to Pro for unlimited", l.MaxTags)
	}
	return nil
}
