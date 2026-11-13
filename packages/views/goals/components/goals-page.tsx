"use client";

import { useState } from "react";
import { Target } from "@multica/ui/icons";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  CollectionPageHeader,
} from "../../layout/collection-page";
import { useT } from "../../i18n";
import { GOAL_STATUS_ORDER } from "../status";
import { GoalConversationButton } from "./goal-conversation-button";
import { GoalList } from "./goal-list";

const ALL_STATUSES = "all";

export function GoalsPage() {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const [status, setStatus] = useState<string>(ALL_STATUSES);

  const statusItems = [
    { value: ALL_STATUSES, label: t(($) => $.list.all_statuses) },
    ...GOAL_STATUS_ORDER.map((value) => ({
      value,
      label: t(($) => $.status[value]),
    })),
  ];

  return (
    <>
      <CollectionPageHeader
        icon={Target}
        title={t(($) => $.list.title)}
        tools={
          <Select
            items={statusItems}
            value={status}
            onValueChange={(next) => next && setStatus(next)}
          >
            <SelectTrigger size="sm" className="w-36" aria-label={t(($) => $.list.filter_status)}>
              <SelectValue>
                {statusItems.find((item) => item.value === status)?.label}
              </SelectValue>
            </SelectTrigger>
            <SelectContent align="start">
              {statusItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        actions={<GoalConversationButton />}
      />

      <div className="min-h-0 flex-1 overflow-y-auto pb-16">
        <GoalList
          wsId={wsId}
          status={status === ALL_STATUSES ? null : status}
        />
      </div>
    </>
  );
}