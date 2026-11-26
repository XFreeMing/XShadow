package issueproperty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrInactiveStatus = errors.New("value must refer to an active status in this workspace's status library")

// ValidateStatusReference checks the catalog rather than copying its options
// into the property definition. A writer holds the shared lock through commit.
func ValidateStatusReference(ctx context.Context, q *db.Queries, workspace pgtype.UUID, value []byte, lock bool) error {
	var key string
	if err := json.Unmarshal(value, &key); err != nil || key == "" {
		return ErrInactiveStatus
	}
	if lock {
		if err := q.LockIssueStatusCatalogShared(ctx, workspace); err != nil {
			return fmt.Errorf("lock status library: %w", err)
		}
	}
	entry, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{WorkspaceID: workspace, Key: key})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && entry.ArchivedAt.Valid) {
		return ErrInactiveStatus
	}
	return err
}
