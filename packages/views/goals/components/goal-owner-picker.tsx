"use client";

import { useState } from "react";
import { UserMinus } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import type { GoalOwnerType } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import {
  PickerEmpty,
  PickerItem,
  PickerSection,
  PropertyPicker,
} from "../../issues/components/pickers/property-picker";

export interface GoalOwnerValue {
  type: GoalOwnerType | null;
  id: string | null;
}

export const EMPTY_GOAL_OWNER: GoalOwnerValue = { type: null, id: null };

/**
 * Owner picker for a goal. An owner is responsibility only: selecting one
 * writes the field and starts no run and grants no access. A directory that no
 * longer resolves the current owner shows an unavailable row rather than a
 * fabricated name, and the owner can be cleared or replaced.
 */
export function GoalOwnerPicker({
  value,
  onChange,
  disabled = false,
  align = "start",
}: {
  value: GoalOwnerValue;
  onChange: (next: GoalOwnerValue) => void;
  disabled?: boolean;
  align?: "start" | "center" | "end";
}) {
  const { t } = useT("goals");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: squads = [] } = useQuery(squadListOptions(wsId));
  const { getActorName, hasActor } = useActorName();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const name = value.type && value.id ? getActorName(value.type, value.id) : null;
  // `undefined` means that directory has not produced a snapshot yet; only an
  // authoritative "not there" is an unavailable owner.
  const unavailable = !!(value.type && value.id && hasActor(value.type, value.id) === false);

  const query = filter.trim().toLowerCase();
  const matches = (candidate: string) =>
    candidate.toLowerCase().includes(query) || matchesPinyin(candidate, query);
  const filteredMembers = members.filter((m) => matches(m.name));
  const filteredAgents = agents.filter((a) => !a.archived_at && matches(a.name));
  const filteredSquads = squads.filter((s) => matches(s.name));

  const choose = (type: GoalOwnerType | null, id: string | null) => {
    onChange({ type, id });
    setOpen(false);
    setFilter("");
  };

  return (
    <PropertyPicker
      open={disabled ? false : open}
      onOpenChange={(next) => {
        if (disabled) return;
        setOpen(next);
        if (!next) setFilter("");
      }}
      width="w-60"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.form.owner)}
      onSearchChange={setFilter}
      trigger={
        value.type && value.id ? (
          <>
            <ActorAvatar actorType={value.type} actorId={value.id} size="xs" profileLink={false} />
            <span className="truncate">{name}</span>
            {unavailable && (
              <span className="shrink-0 text-caption text-warning">
                {t(($) => $.detail.unavailable_owner)}
              </span>
            )}
          </>
        ) : (
          <>
            <UserMinus className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate text-muted-foreground">{t(($) => $.form.no_owner)}</span>
          </>
        )
      }
    >
      <PickerItem
        emptyValue
        selected={!value.id}
        onClick={() => choose(null, null)}
      >
        <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.form.no_owner)}</span>
      </PickerItem>

      {filteredMembers.length > 0 && (
        <PickerSection label={t(($) => $.form.members)}>
          {filteredMembers.map((m) => (
            <PickerItem
              key={m.user_id}
              selected={value.type === "member" && value.id === m.user_id}
              onClick={() => choose("member", m.user_id)}
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size="sm" profileLink={false} />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {filteredAgents.length > 0 && (
        <PickerSection label={t(($) => $.form.agents)}>
          {filteredAgents.map((a) => (
            <PickerItem
              key={a.id}
              selected={value.type === "agent" && value.id === a.id}
              onClick={() => choose("agent", a.id)}
            >
              <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot profileLink={false} />
              <span className="truncate">{a.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {filteredSquads.length > 0 && (
        <PickerSection label={t(($) => $.form.squads)}>
          {filteredSquads.map((s) => (
            <PickerItem
              key={s.id}
              selected={value.type === "squad" && value.id === s.id}
              onClick={() => choose("squad", s.id)}
            >
              <ActorAvatar actorType="squad" actorId={s.id} size="sm" profileLink={false} />
              <span className="truncate">{s.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {query &&
        filteredMembers.length === 0 &&
        filteredAgents.length === 0 &&
        filteredSquads.length === 0 && <PickerEmpty />}
    </PropertyPicker>
  );
}