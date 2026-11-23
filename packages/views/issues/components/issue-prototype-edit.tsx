"use client";

import { useId, useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useChatStore } from "@multica/core/chat";
import { useCreateChatSession } from "@multica/core/chat/mutations";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue } from "@multica/core/types";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogTitle, DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { Label } from "@multica/ui/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Sparkles } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";
import { issuePrototypeImages, type IssuePrototypeImage } from "../utils/prototype-images";
import { canAssignAgent } from "./pickers/assignee-picker";

function PrototypeEditForm({
  issue, images, onDone,
}: {
  issue: Issue;
  images: IssuePrototypeImage[];
  onDone: () => void;
}) {
  const { t } = useT("issues");
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
  const [request, setRequest] = useState("");
  const requestId = useId();
  const session = useCreateChatSession();
  const loading = agentList.isLoading || memberList.isLoading;
  const failed = agentList.isError || memberList.isError;

  async function startConversation(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!agent || !request.trim() || session.isPending || loading || failed) return;

    const title = t(($) => $.prototype.chat_title, { issue: issue.identifier });
    try {
      const created = await session.mutateAsync({ agent_id: agent.id, project_id: issue.project_id, title });
      const originals = images.map(({ url, alt }, index) => `${index + 1}. ${alt} <${url}>`).join("\n");
      const instructions = t(($) => $.prototype.chat_draft, {
        issue: issue.identifier,
        id: issue.id,
        request: request.trim(),
        originals,
        command: `\`multica issue update ${issue.id} --description-file <updated.md> --expected-revision <revision> --attachment <image> --no-start\``,
        interpolation: { escapeValue: false },
      });
      useChatStore.getState().setInputDraft(created.id, instructions);
      onDone();
      push(paths.chatSession(created.id));
    } catch {
      // The mutation error is displayed in the form so the request can be retried.
    }
  }

  return (
    <form className="space-y-4" onSubmit={startConversation}>
      <div className="space-y-2">
        <Label htmlFor={requestId}>{t(($) => $.prototype.request)}</Label>
        <Textarea id={requestId} value={request} required maxLength={4000}
          placeholder={t(($) => $.prototype.request_example)}
          className="min-h-24 resize-y"
          disabled={session.isPending}
          onChange={(event) => setRequest(event.target.value)} />
      </div>
      {agents.length > 0 && (
        <Select items={agents.map(({ id, name }) => ({ value: id, label: name }))}
          value={agent?.id ?? null} disabled={session.isPending} onValueChange={setSelectedAgentId}>
          <SelectTrigger className="w-full" aria-label={t(($) => $.prototype.agent)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {agents.map(({ id, name }) => <SelectItem key={id} value={id}>{name}</SelectItem>)}
          </SelectContent>
        </Select>
      )}
      {failed ? (
        <div role="alert" className="flex items-center gap-2 text-muted-foreground">
          {t(($) => $.prototype.agents_failed)}
          <Button variant="ghost" size="sm" onClick={() => { void agentList.refetch(); void memberList.refetch(); }}>
            {t(($) => $.prototype.retry)}
          </Button>
        </div>
      ) : !loading && !agent && (
        <p className="text-muted-foreground">{t(($) => $.prototype.no_agent)}</p>
      )}
      {session.isError && <p role="alert" className="text-destructive">{t(($) => $.prototype.chat_failed)}</p>}
      <DialogFooter>
        <Button type="submit" aria-busy={session.isPending}
          disabled={!agent || !request.trim() || session.isPending || loading || failed}>
          {session.isPending && <Spinner />}
          {t(($) => $.prototype.continue_in_chat)}
        </Button>
      </DialogFooter>
    </form>
  );
}

export function IssuePrototypeEdit({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(false);
  const images = useMemo(() => issuePrototypeImages(issue.description), [issue.description]);
  if (images.length === 0) return null;
  return (
    <div className="mb-3 flex flex-wrap items-center justify-between gap-2 border-b border-border/60 pb-2">
      <h2 className="text-label font-medium">{t(($) => $.prototype.title)}</h2>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger render={<Button size="sm" variant="outline" />}>
          <Sparkles />
          {t(($) => $.prototype.adjust)}
        </DialogTrigger>
        <DialogContent className="sm:max-w-md">
          <DialogTitle className="pr-8">{t(($) => $.prototype.adjust)} · {issue.identifier}</DialogTitle>
          <DialogDescription>{t(($) => $.prototype.review_first)}</DialogDescription>
          {open && <PrototypeEditForm issue={issue} images={images} onDone={() => setOpen(false)} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}
