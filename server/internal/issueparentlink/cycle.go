package issueparentlink

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func parentLinkWouldCycle(
	ctx context.Context,
	q *db.Queries,
	workspaceID, childID, parentID pgtype.UUID,
) (bool, error) {
	rows, err := q.WalkIssueParentAncestors(ctx, db.WalkIssueParentAncestorsParams{
		WorkspaceID: workspaceID,
		ID:          parentID,
	})
	if err != nil {
		return false, fmt.Errorf("walk issue parent ancestors: %w", err)
	}
	for _, row := range rows {
		if row.Cycle {
			return true, nil
		}
		if row.ID == childID {
			return true, nil
		}
	}
	return false, nil
}
