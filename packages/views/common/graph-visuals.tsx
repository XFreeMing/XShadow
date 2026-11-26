"use client";

import { useId, type ComponentProps } from "react";
import { cn } from "@multica/ui/lib/utils";

interface GraphCardStyleOptions {
  tone?: "current" | "waiting" | "done";
  isSelected?: boolean;
  isDimmed?: boolean;
}

interface GraphCardFrameProps extends ComponentProps<"div">, GraphCardStyleOptions {}

const cardTones = {
  current: "border-brand/70 bg-brand/[0.04]",
  waiting: "border-warning/60",
  done: "border-success/40",
};

export function graphCardClassName(options: GraphCardStyleOptions, extra?: string) {
  const { tone, isSelected, isDimmed } = options;
  return cn(
    "relative flex flex-col justify-between rounded-lg border border-border bg-card px-2.5 py-2 shadow-2xs transition-all",
    tone ? cardTones[tone] : "hover:border-foreground/30",
    isDimmed && "opacity-35",
    extra,
    isSelected && "border-brand ring-2 ring-brand/45",
  );
}

export function GraphCardFrame({
  className,
  tone,
  isSelected,
  isDimmed,
  ...props
}: GraphCardFrameProps) {
  return (
    <div
      {...props}
      className={graphCardClassName({ tone, isSelected, isDimmed }, className)}
    />
  );
}

export interface GraphEdgeItem {
  id: string;
  path: string;
  isHighlighted?: boolean;
  isDimmed?: boolean;
  isDashed?: boolean;
  subtle?: boolean;
  hasArrow?: boolean;
  "data-org-edge"?: string;
}

export function GraphCanvasEdges({ edges }: { edges: readonly GraphEdgeItem[] }) {
  const markerId = useId();

  return (
    <>
      <defs>
        <marker
          id={markerId}
          viewBox="0 0 8 8"
          refX="7"
          refY="4"
          markerWidth="5"
          markerHeight="5"
          orient="auto"
        >
          <path d="M0 0 L8 4 L0 8 Z" fill="context-stroke" />
        </marker>
      </defs>
      {edges.map((edge) => {
        const isHighlighted = !!edge.isHighlighted;
        const isDimmed = !isHighlighted && !!edge.isDimmed;
        const colorClass = isHighlighted
          ? "text-brand"
          : edge.subtle
            ? "text-border"
            : "text-muted-foreground/70";

        return (
          <path
            key={edge.id}
            data-org-edge={edge["data-org-edge"]}
            d={edge.path}
            fill="none"
            stroke="currentColor"
            strokeWidth={isHighlighted ? 2.25 : 1.5}
            strokeDasharray={edge.isDashed ? "5 4" : undefined}
            markerEnd={edge.hasArrow !== false ? `url(#${markerId})` : undefined}
            className={cn("transition-opacity", colorClass)}
            opacity={isDimmed ? 0.16 : 0.9}
          />
        );
      })}
    </>
  );
}
