"use client";

import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import { useChatStore } from "@multica/core/chat";
import { useOpenGoalConversation } from "@multica/core/goals";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { MessageSquare } from "@multica/ui/icons";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";

export function GoalConversationButton({ projectTitle }: { projectTitle?: string }) {
  const { t } = useT("goals");
  const { push } = useNavigation();
  const paths = useWorkspacePaths();
  const conversation = useOpenGoalConversation();

  async function openConversation() {
    try {
      const session = await conversation.mutateAsync();
      if (projectTitle) {
        const chat = useChatStore.getState();
        if (!chat.inputDrafts[session.id]?.trim()) {
          chat.setInputDraft(session.id, t(($) => $.list.project_chat_draft, {
            project: projectTitle,
          }));
        }
      }
      push(paths.chatSession(session.id));
    } catch (err) {
      toast.error(errorCode(err) === "goal_chief_unavailable"
        ? t(($) => $.list.chief_unavailable)
        : t(($) => $.list.chat_open_failed));
    }
  }

  return (
    <Button
      size="sm"
      variant="outline"
      onClick={openConversation}
      disabled={conversation.isPending}
      aria-busy={conversation.isPending}
    >
      <MessageSquare />
      {t(($) => $.list.talk_to_chief)}
    </Button>
  );
}
