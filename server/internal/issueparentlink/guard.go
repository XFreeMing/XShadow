// Package issueparentlink serializes and validates task parent_issue_id writes.
package issueparentlink

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrSelfParent         = errors.New("an issue cannot be its own parent")
	ErrCircular           = errors.New("circular parent relationship detected")
	ErrParentNotFound     = errors.New("parent issue not found in this workspace")
	ErrCorruptParentGraph = errors.New("circular parent relationship detected")
)

// LockKey returns the transaction advisory key suffix for a workspace hierarchy lock.
func LockKey(workspaceID pgtype.UUID) string {
	return uuidToString(workspaceID) + HierarchyLockSuffix
}

// GuardCreate runs immediately before inserting a child row that references parentID.
func GuardCreate(ctx context.Context, q *db.Queries, workspaceID, newIssueID, parentID pgtype.UUID) error {
	if !parentID.Valid {
		return nil
	}
	if parentID == newIssueID {
		return ErrSelfParent
	}
	if err := q.LockIssueParentHierarchy(ctx, workspaceID); err != nil {
		return fmt.Errorf("lock issue parent hierarchy: %w", err)
	}
	if err := q.LockIssuesForParentWrite(ctx, db.LockIssuesForParentWriteParams{
		WorkspaceID: workspaceID,
		Column2:     []pgtype.UUID{parentID},
	}); err != nil {
		return fmt.Errorf("lock parent for create: %w", err)
	}
	parent, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: parentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrParentNotFound
		}
		return fmt.Errorf("load parent for create: %w", err)
	}
	if !parent.ID.Valid {
		return ErrParentNotFound
	}
	cycle, err := parentLinkWouldCycle(ctx, q, workspaceID, newIssueID, parentID)
	if err != nil {
		return fmt.Errorf("check parent link for create: %w", err)
	}
	if cycle {
		return ErrCircular
	}
	return nil
}

// GuardUpdate revalidates a parent link change under the workspace hierarchy lock.
func GuardUpdate(
	ctx context.Context,
	q *db.Queries,
	workspaceID, childID, nextParent pgtype.UUID,
	parentTouched bool,
) error {
	if !parentTouched {
		return nil
	}
	if err := q.LockIssueParentHierarchy(ctx, workspaceID); err != nil {
		return fmt.Errorf("lock issue parent hierarchy: %w", err)
	}
	current, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: childID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrParentNotFound
		}
		return fmt.Errorf("load issue for parent guard: %w", err)
	}
	lockIDs := lockIDsForParentWrite(childID, current.ParentIssueID, nextParent)
	if err := q.LockIssuesForParentWrite(ctx, db.LockIssuesForParentWriteParams{
		WorkspaceID: workspaceID,
		Column2:     lockIDs,
	}); err != nil {
		return fmt.Errorf("lock issues for parent write: %w", err)
	}
	if !nextParent.Valid {
		return nil
	}
	if nextParent == childID {
		return ErrSelfParent
	}
	parent, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: nextParent, WorkspaceID: workspaceID,
	})
	if err != nil || !parent.ID.Valid {
		return ErrParentNotFound
	}
	cycle, err := parentLinkWouldCycle(ctx, q, workspaceID, childID, nextParent)
	if err != nil {
		return fmt.Errorf("check parent link cycle: %w", err)
	}
	if cycle {
		return ErrCircular
	}
	return nil
}

func lockIDsForParentWrite(child, previous, next pgtype.UUID) []pgtype.UUID {
	seen := make(map[string]struct{})
	var ids []pgtype.UUID
	add := func(id pgtype.UUID) {
		if !id.Valid {
			return
		}
		key := uuidToString(id)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	add(child)
	add(previous)
	add(next)
	sort.Slice(ids, func(i, j int) bool {
		return uuidToString(ids[i]) < uuidToString(ids[j])
	})
	return ids
}

func uuidToString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	b := id.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
