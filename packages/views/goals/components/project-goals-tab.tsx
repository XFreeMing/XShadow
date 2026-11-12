"use client";

import { useState } from "react";
import { Plus } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { GoalFormDialog } from "./goal-form-dialog";
import { GoalList } from "./goal-list";

/**
 * A project's Goals tab (`?tab=goals`). Share-owned: web mounts it now and the
 * desktop router can render the same component for the same route segment.
 */
export function ProjectGoalsTab({ projectId }: { projectId: string }) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const router = useNavigation();
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center justify-end px-4 pb-2">
        <Button size="sm" variant="outline" onClick={() => setCreateOpen(true)}>
          <Plus className="size-3.5" />
          {t(($) => $.list.new_goal)}
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto pb-16">
        <GoalList
          wsId={wsId}
          projectId={projectId}
          compactEmpty
          emptyAction={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              {t(($) => $.list.new_goal)}
            </Button>
          }
        />
      </div>
      <GoalFormDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        defaultProjectId={projectId}
        onSaved={(goal) => router.push(wsPaths.goalDetail(goal.id))}
      />
    </div>
  );
}