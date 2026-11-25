"use client";

import { useMemo, useState, type FormEvent } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { GridRecordOption } from "@multica/apitable-grid";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  genericRecordCommentsInfiniteOptions,
  genericTableKeys,
  type GenericField,
  type GenericRecord,
  type GenericRecordComment,
  type GenericTable,
  type GenericViewKind,
} from "@multica/core/tables";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { TableChatLauncher } from "./table-chat-launcher";
import { formatFieldValue, resolveRecordTitle } from "./view-helpers";

const COMMENTS_PAGE_SIZE = 50;

export function mergeComments(
  existing: readonly GenericRecordComment[],
  incoming: readonly GenericRecordComment[],
): GenericRecordComment[] {
  const byId = new Map<string, GenericRecordComment>();
  for (const item of existing) byId.set(item.id, item);
  for (const item of incoming) byId.set(item.id, item);
  return [...byId.values()].sort((left, right) => {
    if (left.seq !== right.seq) return left.seq - right.seq;
    return left.id.localeCompare(right.id);
  });
}

function formatCommentTime(iso: string): string {
  const parsed = Date.parse(iso);
  if (Number.isNaN(parsed)) return iso;
  return new Date(parsed).toLocaleString(undefined, {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function parentSnippet(body: string): string {
  const singleLine = body.replace(/\s+/g, " ").trim();
  return singleLine.length > 60 ? `${singleLine.slice(0, 59)}…` : singleLine;
}

interface RecordDetailBodyProps {
  table?: Pick<GenericTable, "id" | "name"> | null;
  activeView?: { id: string; name: string; kind: GenericViewKind } | null;
  record: GenericRecord;
  fields: readonly GenericField[];
  visibleFields?: readonly GenericField[];
  records?: readonly GenericRecord[];
  titleFieldId?: string;
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>;
  onClose: () => void;
}

function RecordDetailBody({
  table,
  activeView,
  record,
  fields,
  visibleFields,
  records,
  titleFieldId,
  recordOptionsByTable,
  onClose,
}: RecordDetailBodyProps) {
  const { t } = useT("tables");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const tableId = table?.id ?? "";
  const recordId = record.id;

  const labels = useMemo(
    () => ({
      checked: t(($) => $.sheet.checked),
      unchecked: t(($) => $.sheet.unchecked),
      emptyValue: t(($) => $.sheet.empty_value),
    }),
    [t],
  );

  const [draftBody, setDraftBody] = useState("");
  const [replyTo, setReplyTo] = useState<GenericRecordComment | null>(null);
  const [postError, setPostError] = useState<string | null>(null);

  const commentsQuery = useInfiniteQuery({
    ...genericRecordCommentsInfiniteOptions(wsId, tableId, recordId, COMMENTS_PAGE_SIZE),
    refetchOnMount: "always",
    refetchInterval: 5000,
  });

  const allComments = useMemo(() => {
    const pages = commentsQuery.data?.pages ?? [];
    let merged: GenericRecordComment[] = [];
    for (const page of pages) {
      merged = mergeComments(merged, page.comments);
    }
    return merged;
  }, [commentsQuery.data?.pages]);

  const commentById = useMemo(
    () => new Map(allComments.map((item) => [item.id, item])),
    [allComments],
  );

  const createComment = useMutation({
    mutationFn: (payload: { targetRecordId: string; body: string; parent_id?: string | null }) =>
      api.createGenericRecordComment(tableId, payload.targetRecordId, {
        body: payload.body,
        parent_id: payload.parent_id,
      }),
    onSuccess: async (_created, variables) => {
      if (variables.targetRecordId === recordId) {
        setPostError(null);
        setDraftBody("");
        setReplyTo(null);
      }
      await queryClient.invalidateQueries({
        queryKey: genericTableKeys.recordComments(wsId, tableId, variables.targetRecordId),
      });
    },
    onError: (err, variables) => {
      if (variables.targetRecordId !== recordId) return;
      setPostError(err instanceof ApiError ? err.message : t(($) => $.sheet.comment_post_failed));
    },
  });

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmed = draftBody.trim();
    if (!trimmed || createComment.isPending) return;
    setPostError(null);
    createComment.mutate({
      targetRecordId: recordId,
      body: trimmed,
      parent_id: replyTo?.id ?? null,
    });
  };

  const title = resolveRecordTitle(record, fields, titleFieldId, recordOptionsByTable, labels);

  return (
    <>
      <DialogHeader className="flex flex-row items-center justify-between gap-2 pr-8">
        <DialogTitle className="min-w-0 truncate">{title}</DialogTitle>
        {table ? (
          <TableChatLauncher
            mode="record"
            context={{
              table,
              view: activeView ?? null,
              fields,
              visibleFields: visibleFields ?? [],
              records: records ?? [record],
              selectedRecord: record,
              titleFieldId,
              recordOptionsByTable,
            }}
            onLaunched={onClose}
          />
        ) : null}
      </DialogHeader>
      <div className="flex max-h-[72vh] flex-col gap-4 overflow-y-auto pr-1">
        <dl className="divide-y divide-border/60 rounded-md border border-border">
          {fields.map((field) => (
            <div key={field.id} className="grid grid-cols-[140px_1fr] gap-3 px-3 py-2 text-body">
              <dt className="truncate text-muted-foreground">{field.name}</dt>
              <dd className="min-w-0 break-words">
                {formatFieldValue(
                  field,
                  record.values[field.id],
                  recordOptionsByTable,
                  labels,
                  record.errors?.[field.id],
                )}
              </dd>
            </div>
          ))}
        </dl>

        <section className="flex flex-col gap-2.5 border-t border-border pt-3" data-record-discussion="">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-body font-semibold">
              {t(($) => $.sheet.discussion)}
              {allComments.length > 0 ? ` (${allComments.length})` : ""}
            </h3>
          </div>

          {commentsQuery.isLoading ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.list.loading)}</p>
          ) : commentsQuery.isError && allComments.length === 0 ? (
            <div role="alert" className="flex items-center gap-2 text-caption text-destructive">
              <span>{t(($) => $.sheet.comments_load_failed)}</span>
              <Button
                type="button"
                variant="ghost"
                size="xs"
                onClick={() => void commentsQuery.refetch()}
              >
                {t(($) => $.sheet.retry)}
              </Button>
            </div>
          ) : allComments.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.sheet.no_comments)}</p>
          ) : (
            <ol className="space-y-2" data-record-comments-list="">
              {allComments.map((comment) => {
                const parent = comment.parent_id ? commentById.get(comment.parent_id) : undefined;
                const roleLabel = comment.actor.source === "agent"
                  ? t(($) => $.sheet.actor_agent)
                  : t(($) => $.sheet.actor_member);
                return (
                  <li
                    key={comment.id}
                    data-comment-id={comment.id}
                    data-comment-seq={comment.seq}
                    className="rounded-md border border-border/80 bg-muted/15 px-3 py-2"
                  >
                    <div className="flex items-center justify-between gap-2 text-caption">
                      <div className="flex min-w-0 items-center gap-1.5">
                        <span
                          className="truncate font-medium"
                          style={{ color: comment.actor.color }}
                        >
                          {comment.actor.label}
                        </span>
                        <span className="shrink-0 rounded bg-muted px-1 py-0.5 text-[10px] text-muted-foreground">
                          {roleLabel}
                        </span>
                        <time className="shrink-0 text-muted-foreground" dateTime={comment.created_at}>
                          {formatCommentTime(comment.created_at)}
                        </time>
                      </div>
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        onClick={() => setReplyTo(comment)}
                      >
                        {t(($) => $.sheet.reply)}
                      </Button>
                    </div>
                    {comment.parent_id ? (
                      <p className="mt-1 truncate border-l-2 border-border pl-2 text-caption text-muted-foreground">
                        {parent
                          ? `${t(($) => $.sheet.replying_to)} ${parent.actor.label}: ${parentSnippet(parent.body)}`
                          : t(($) => $.sheet.reply_parent_unloaded)}
                      </p>
                    ) : null}
                    <p className="mt-1 whitespace-pre-wrap break-words text-body text-foreground">
                      {comment.body}
                    </p>
                  </li>
                );
              })}
            </ol>
          )}

          {commentsQuery.hasNextPage ? (
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="xs"
                disabled={commentsQuery.isFetchingNextPage}
                onClick={() => void commentsQuery.fetchNextPage()}
              >
                {commentsQuery.isFetchingNextPage ? t(($) => $.list.loading) : t(($) => $.sheet.load_more)}
              </Button>
              {commentsQuery.isFetchNextPageError ? (
                <span role="alert" className="text-caption text-destructive">
                  {t(($) => $.sheet.comments_load_failed)}
                </span>
              ) : null}
            </div>
          ) : null}

          <form className="flex flex-col gap-2 pt-1" onSubmit={handleSubmit}>
            {replyTo ? (
              <div className="flex items-center justify-between gap-2 rounded bg-muted/40 px-2.5 py-1 text-caption">
                <span className="min-w-0 truncate text-muted-foreground">
                  {t(($) => $.sheet.replying_to)} {replyTo.actor.label}: {parentSnippet(replyTo.body)}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  onClick={() => setReplyTo(null)}
                >
                  {t(($) => $.sheet.cancel_reply)}
                </Button>
              </div>
            ) : null}
            <Textarea
              aria-label={t(($) => $.sheet.comment_placeholder)}
              value={draftBody}
              maxLength={4000}
              placeholder={t(($) => $.sheet.comment_placeholder)}
              className="min-h-16 resize-y"
              disabled={createComment.isPending}
              onChange={(event) => setDraftBody(event.target.value)}
            />
            {postError ? (
              <p role="alert" className="text-caption text-destructive">
                {postError}
              </p>
            ) : null}
            <div className="flex justify-end">
              <Button
                type="submit"
                size="sm"
                disabled={createComment.isPending || draftBody.trim() === ""}
              >
                {t(($) => $.sheet.send_comment)}
              </Button>
            </div>
          </form>
        </section>
      </div>
    </>
  );
}

export function RecordDetailDialog({
  table,
  activeView,
  record,
  fields,
  visibleFields,
  records,
  titleFieldId,
  recordOptionsByTable,
  onClose,
}: {
  table?: Pick<GenericTable, "id" | "name"> | null;
  activeView?: { id: string; name: string; kind: GenericViewKind } | null;
  record: GenericRecord | null;
  fields: readonly GenericField[];
  visibleFields?: readonly GenericField[];
  records?: readonly GenericRecord[];
  titleFieldId?: string;
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>;
  onClose: () => void;
}) {
  return (
    <Dialog open={record !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-xl" data-record-detail-dialog="">
        {record ? (
          <RecordDetailBody
            key={record.id}
            table={table}
            activeView={activeView}
            record={record}
            fields={fields}
            visibleFields={visibleFields}
            records={records}
            titleFieldId={titleFieldId}
            recordOptionsByTable={recordOptionsByTable}
            onClose={onClose}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
