"use client";

import { useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import type { GridRecordOption } from "@multica/apitable-grid";
import { useAuthStore } from "@multica/core/auth";
import { useChatStore } from "@multica/core/chat";
import { useCreateChatSession } from "@multica/core/chat/mutations";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  GenericField,
  GenericRecord,
  GenericTable,
  GenericViewKind,
} from "@multica/core/tables";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Sparkles } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";
import { canAssignAgent } from "../../issues/components/pickers/assignee-picker";
import {
  formatFieldValue,
  resolveRecordTitle,
  type ValueFormatLabels,
} from "./view-helpers";

const MAX_DRAFT_FIELDS = 10;
const MAX_DRAFT_RECORDS = 15;
const MAX_CELL_CHARS = 80;
const MAX_DRAFT_CHARS = 3200;

export interface TableChatContext {
  table: Pick<GenericTable, "id" | "name">;
  view?: { id: string; name: string; kind: GenericViewKind } | null;
  fields: readonly GenericField[];
  visibleFields: readonly GenericField[];
  records: readonly GenericRecord[];
  selectedRecord?: GenericRecord | null;
  titleFieldId?: string;
  recordOptionsByTable: Readonly<Record<string, readonly GridRecordOption[]>>;
}

function truncateText(text: string, maxChars: number): string {
  if (text.length <= maxChars) return text;
  return `${text.slice(0, Math.max(0, maxChars - 1))}…`;
}

export function buildTableChatDraft(
  ctx: TableChatContext,
  labels: ValueFormatLabels & { truncatedNotice: string },
): string {
  const lines: string[] = [];
  lines.push(`Table: ${ctx.table.name} (${ctx.table.id})`);
  if (ctx.view) {
    lines.push(`View: ${ctx.view.name} [${ctx.view.kind}]`);
  }

  let truncated = false;

  if (ctx.selectedRecord) {
    const title = resolveRecordTitle(
      ctx.selectedRecord,
      ctx.visibleFields,
      ctx.titleFieldId,
      ctx.recordOptionsByTable,
      labels,
    );
    lines.push(`Selected record: ${title} (${ctx.selectedRecord.id})`);
    const shownFields = ctx.visibleFields.slice(0, MAX_DRAFT_FIELDS);
    if (ctx.visibleFields.length > MAX_DRAFT_FIELDS) truncated = true;
    for (const field of shownFields) {
      const val = formatFieldValue(
        field,
        ctx.selectedRecord.values[field.id],
        ctx.recordOptionsByTable,
        labels,
        ctx.selectedRecord.errors?.[field.id],
      );
      lines.push(`- ${field.name}: ${truncateText(val.replace(/\s+/g, " "), MAX_CELL_CHARS)}`);
    }
  } else {
    const shownFields = ctx.visibleFields.slice(0, MAX_DRAFT_FIELDS);
    if (ctx.visibleFields.length > MAX_DRAFT_FIELDS) truncated = true;
    const shownRecords = ctx.records.slice(0, MAX_DRAFT_RECORDS);
    if (ctx.records.length > MAX_DRAFT_RECORDS) truncated = true;

    lines.push(`Fields (${shownFields.length}/${ctx.visibleFields.length}): ${shownFields.map((f) => f.name).join(", ")}`);
    lines.push(`Records (${shownRecords.length}/${ctx.records.length}):`);
    for (let index = 0; index < shownRecords.length; index++) {
      const rec = shownRecords[index]!;
      const parts = shownFields.map((field) => {
        const val = formatFieldValue(
          field,
          rec.values[field.id],
          ctx.recordOptionsByTable,
          labels,
          rec.errors?.[field.id],
        );
        return `${field.name}=${truncateText(val.replace(/\s+/g, " "), MAX_CELL_CHARS)}`;
      });
      lines.push(`${index + 1}. [${rec.id.slice(0, 8)}] ${parts.join(" | ")}`);
    }
  }

  let result = lines.join("\n");
  if (result.length > MAX_DRAFT_CHARS) {
    result = result.slice(0, MAX_DRAFT_CHARS);
    truncated = true;
  }
  if (truncated) {
    result = `${result}\n[${labels.truncatedNotice}]`;
  }
  return result;
}

function TableChatForm({
  context,
  onDone,
}: {
  context: TableChatContext;
  onDone: () => void;
}) {
  const { t } = useT("tables");
  const userId = useAuthStore((state) => state.user?.id);
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { push } = useNavigation();
  const agentList = useQuery(agentListOptions(wsId));
  const memberList = useQuery(memberListOptions(wsId));
  const role = memberList.data?.find((member) => member.user_id === userId)?.role;
  const agents = agentList.data?.filter((agent) => !agent.archived_at && canAssignAgent(agent, userId, role)) ?? [];
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(
    () => useChatStore.getState().selectedAgentId,
  );
  const agent = agents.find((candidate) => candidate.id === selectedAgentId) ?? agents[0];
  const session = useCreateChatSession();
  const loading = agentList.isLoading || memberList.isLoading;
  const failed = agentList.isError || memberList.isError;

  const formatLabels = useMemo(
    () => ({
      checked: t(($) => $.sheet.checked),
      unchecked: t(($) => $.sheet.unchecked),
      emptyValue: t(($) => $.sheet.empty_value),
      truncatedNotice: t(($) => $.sheet.chat_truncated),
    }),
    [t],
  );

  async function startChat(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!agent || session.isPending || loading || failed) return;

    const recordTitle = context.selectedRecord
      ? resolveRecordTitle(
          context.selectedRecord,
          context.visibleFields,
          context.titleFieldId,
          context.recordOptionsByTable,
          formatLabels,
        )
      : null;
    const title = recordTitle
      ? `${context.table.name} · ${recordTitle}`.slice(0, 80)
      : context.table.name.slice(0, 80);

    try {
      const created = await session.mutateAsync({ agent_id: agent.id, title });
      const draft = buildTableChatDraft(context, formatLabels);
      const existingDraft = useChatStore.getState().inputDrafts[created.id];
      if (!existingDraft) {
        useChatStore.getState().setInputDraft(created.id, draft);
      }
      onDone();
      push(paths.chatSession(created.id));
    } catch {
      // Displayed inline via session.isError.
    }
  }

  return (
    <form className="space-y-4" onSubmit={startChat}>
      {agents.length > 0 ? (
        <Select
          items={agents.map(({ id, name }) => ({ value: id, label: name }))}
          value={agent?.id ?? null}
          disabled={session.isPending}
          onValueChange={setSelectedAgentId}
        >
          <SelectTrigger className="w-full" aria-label={t(($) => $.sheet.chat_agent)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {agents.map(({ id, name }) => (
              <SelectItem key={id} value={id}>
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : null}

      {failed ? (
        <div role="alert" className="flex items-center gap-2 text-body text-muted-foreground">
          <span>{t(($) => $.sheet.chat_agents_failed)}</span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => {
              void agentList.refetch();
              void memberList.refetch();
            }}
          >
            {t(($) => $.sheet.retry)}
          </Button>
        </div>
      ) : !loading && !agent ? (
        <p className="text-body text-muted-foreground">{t(($) => $.sheet.chat_no_agent)}</p>
      ) : null}

      {session.isError ? (
        <p role="alert" className="text-body text-destructive">
          {t(($) => $.sheet.chat_failed)}
        </p>
      ) : null}

      <DialogFooter>
        <Button
          type="submit"
          size="sm"
          aria-busy={session.isPending}
          disabled={!agent || session.isPending || loading || failed}
        >
          {session.isPending ? <Spinner /> : null}
          {t(($) => $.sheet.continue_in_chat)}
        </Button>
      </DialogFooter>
    </form>
  );
}

export function TableChatLauncher({
  context,
  mode,
  onLaunched,
}: {
  context: TableChatContext;
  mode: "table" | "record";
  onLaunched?: () => void;
}) {
  const { t } = useT("tables");
  const [open, setOpen] = useState(false);
  const label = mode === "record"
    ? t(($) => $.sheet.chat_about_record)
    : t(($) => $.sheet.chat_with_table);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger
        render={
          <Button
            type="button"
            variant="outline"
            size="sm"
            data-table-chat-trigger={mode}
          />
        }
      >
        <Sparkles />
        {label}
      </DialogTrigger>
      <DialogContent className="sm:max-w-md" data-table-chat-dialog={mode}>
        <DialogTitle className="pr-8">{label}</DialogTitle>
        <DialogDescription>{t(($) => $.sheet.chat_prefill_hint)}</DialogDescription>
        {open ? (
          <TableChatForm
            context={context}
            onDone={() => {
              setOpen(false);
              onLaunched?.();
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
