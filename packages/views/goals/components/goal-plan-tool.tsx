"use client";

import { useId, type ReactNode } from "react";
import { ChevronDown } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";

export function GoalPlanTool({
  label,
  icon,
  status,
  open,
  onToggle,
  children,
}: {
  label: string;
  icon: ReactNode;
  status?: string | null;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  const id = useId();

  return (
    <>
      <Button
        id={`${id}-button`}
        type="button"
        size="xs"
        variant={open ? "secondary" : "ghost"}
        aria-expanded={open}
        aria-controls={id}
        className="max-w-full"
        onClick={onToggle}
      >
        {icon}
        <span>{label}</span>
        {status ? (
          <span className="min-w-0 truncate rounded bg-muted px-1.5 py-0.5 text-caption font-normal text-muted-foreground" title={status}>
            {status}
          </span>
        ) : null}
        <ChevronDown aria-hidden="true" className={open ? "rotate-180 text-muted-foreground" : "text-muted-foreground"} />
      </Button>
      <section
        id={id}
        aria-labelledby={`${id}-button`}
        hidden={!open}
        className="order-last min-w-0 basis-full border-t pt-3 pb-1"
      >
        {children}
      </section>
    </>
  );
}
