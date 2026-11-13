"use client";

import { useWorkspaceId } from "@multica/core/hooks";
import { GoalConversationButton } from "./goal-conversation-button";
import { GoalList } from "./goal-list";

export function ProjectGoalsTab({ projectId, projectTitle }: {
  projectId: string;
  projectTitle: string;
}) {
  const wsId = useWorkspaceId();

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center justify-end px-4 pb-2">
        <GoalConversationButton projectTitle={projectTitle} />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-16">
        <GoalList wsId={wsId} projectId={projectId} compactEmpty />
      </div>
    </div>
  );
}
