package tables

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	defaultCommentLimit = 50
	maxCommentLimit     = 100
	maxCommentBodyChars = 4000
)

// CommentDto represents a comment on a generic table record.
type CommentDto struct {
	ID        string   `json:"id"`
	RecordID  string   `json:"record_id"`
	ParentID  *string  `json:"parent_id"`
	Seq       int64    `json:"seq"`
	Body      string   `json:"body"`
	Actor     ActorDto `json:"actor"`
	CreatedAt string   `json:"created_at"`
}

// CommentListDto is the paginated comment list response.
type CommentListDto struct {
	Comments   []CommentDto `json:"comments"`
	HasMore    bool         `json:"has_more"`
	NextCursor *int64       `json:"next_cursor"`
}

func CleanCommentBody(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(trimmed)
	if n == 0 {
		return "", errInvalid("评论内容不能为空")
	}
	if n > maxCommentBodyChars {
		return "", errInvalid("评论内容不能超过 4000 个字符")
	}
	return trimmed, nil
}

func NormalizeCommentListParams(afterOpt, limitOpt *int64) (int64, int64, error) {
	after := int64(0)
	if afterOpt != nil {
		after = *afterOpt
	}
	if after < 0 {
		return 0, 0, errInvalid("评论游标无效")
	}
	limit := int64(defaultCommentLimit)
	if limitOpt != nil {
		limit = *limitOpt
	}
	if limit < 1 || limit > maxCommentLimit {
		return 0, 0, errInvalid("评论分页数量需要在 1 到 100 之间")
	}
	return after, limit, nil
}

func ListComments(
	ctx context.Context,
	pool Pool,
	workspace, tableID, recordID string,
	afterOpt, limitOpt *int64,
) (CommentListDto, error) {
	after, limit, err := NormalizeCommentListParams(afterOpt, limitOpt)
	if err != nil {
		return CommentListDto{}, err
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return CommentListDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return CommentListDto{}, err
	}
	if err := ensureRecordExists(ctx, tx, workspace, tableID, recordID); err != nil {
		return CommentListDto{}, err
	}
	created := tsExpr("created_at")
	sql := fmt.Sprintf(
		`SELECT id::text, record_id::text, parent_id::text, seq, body,
		        actor_source, actor_id::text, task_id::text, actor_label, %s
		 FROM generic_record_comment
		 WHERE workspace_id = $1::uuid
		   AND table_id = $2::uuid
		   AND record_id = $3::uuid
		   AND seq > $4
		 ORDER BY seq ASC
		 LIMIT $5`, created)
	rows, err := tx.Query(ctx, sql, workspace, tableID, recordID, after, limit+1)
	if err != nil {
		return CommentListDto{}, logFailed(err)
	}
	defer rows.Close()
	all := make([]CommentDto, 0)
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return CommentListDto{}, logFailed(err)
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return CommentListDto{}, logFailed(err)
	}
	hasMore := int64(len(all)) > limit
	if hasMore {
		all = all[:limit]
	}
	var nextCursor *int64
	if hasMore && len(all) > 0 {
		seq := all[len(all)-1].Seq
		nextCursor = &seq
	}
	if err := commitTx(ctx, tx); err != nil {
		return CommentListDto{}, err
	}
	return CommentListDto{
		Comments:   all,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

func CreateComment(
	ctx context.Context,
	pool Pool,
	workspace, tableID, recordID string,
	actor Actor,
	rawBody string,
	parentID *string,
) (CommentDto, error) {
	body, err := CleanCommentBody(rawBody)
	if err != nil {
		return CommentDto{}, err
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return CommentDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := ensureTableExists(ctx, tx, workspace, tableID); err != nil {
		return CommentDto{}, err
	}
	if _, err := lockRecord(ctx, tx, workspace, tableID, recordID); err != nil {
		return CommentDto{}, err
	}
	if parentID != nil {
		if err := ensureParentCommentExists(ctx, tx, workspace, tableID, recordID, *parentID); err != nil {
			return CommentDto{}, err
		}
	}
	seq, err := nextCommentSeq(ctx, tx, workspace, tableID, recordID)
	if err != nil {
		return CommentDto{}, err
	}
	created := tsExpr("created_at")
	sql := fmt.Sprintf(
		`INSERT INTO generic_record_comment (
		    workspace_id, table_id, record_id, parent_id, seq, body,
		    actor_source, actor_id, task_id, actor_label
		 ) VALUES (
		    $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9::uuid, $10
		 )
		 RETURNING id::text, record_id::text, parent_id::text, seq, body,
		           actor_source, actor_id::text, task_id::text, actor_label, %s`, created)
	comment, err := scanComment(tx.QueryRow(
		ctx, sql,
		workspace, tableID, recordID, parentID, seq, body,
		actor.Source, actor.ID, actor.TaskID, actor.Label,
	))
	if err != nil {
		return CommentDto{}, logFailed(err)
	}
	if err := commitTx(ctx, tx); err != nil {
		return CommentDto{}, err
	}
	return comment, nil
}

func ensureRecordExists(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID string) error {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM generic_record WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`,
		recordID, tableID, workspace,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound("找不到这条记录")
	}
	if err != nil {
		return logFailed(err)
	}
	return nil
}

func ensureParentCommentExists(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID, parentID string) error {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1
		 FROM generic_record_comment
		 WHERE id = $1::uuid
		   AND workspace_id = $2::uuid
		   AND table_id = $3::uuid
		   AND record_id = $4::uuid`,
		parentID, workspace, tableID, recordID,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalid("回复的父评论不存在或不属于当前记录")
	}
	if err != nil {
		return logFailed(err)
	}
	return nil
}

func nextCommentSeq(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID string) (int64, error) {
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1
		 FROM generic_record_comment
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid`,
		workspace, tableID, recordID,
	).Scan(&seq); err != nil {
		return 0, logFailed(err)
	}
	return seq, nil
}

func scanComment(row rowScanner) (CommentDto, error) {
	var c CommentDto
	var source, actorID, label string
	var taskID *string
	if err := row.Scan(
		&c.ID, &c.RecordID, &c.ParentID, &c.Seq, &c.Body,
		&source, &actorID, &taskID, &label, &c.CreatedAt,
	); err != nil {
		return CommentDto{}, err
	}
	c.Actor = actorDTO(source, actorID, taskID, label)
	return c, nil
}
