"use client";

import { useId, type CSSProperties } from "react";
import { cn } from "../../lib/utils";
import {
  ICON_TILE_RADIUS,
  MARK_SEGMENTS,
  REVERSE_MARK_STOPS,
  TILE_GRADIENT,
  TILE_STOPS,
  iconMarkScale,
  markTransform,
} from "../../lib/baishadow-brand";
import { BaiShadowGradient } from "./baishadow-gradient";

export type BaiShadowIconMode =
  /** The mark at rest. */
  | "static"
  /** The three shadows light up in turn while agents are working. */
  | "think"
  /** Tile pops in, shadows grow out of the centre as the mark turns into place — app launch, sign-in. */
  | "intro"
  /** `intro`, then `think` until whatever is loading unmounts it. */
  | "launch";

interface BaiShadowIconProps extends Omit<React.ComponentProps<"svg">, "mode"> {
  mode?: BaiShadowIconMode;
  /** `circle` is the avatar form. */
  shape?: "square" | "circle";
  /**
   * Rendered size in px. Sets width/height and picks the optical size (24px and
   * below draws a larger mark). Omit to size with `className` (1em
   * default); the small optical size is used then.
   */
  size?: number;
  /** Accessible name. Without one the mark is decorative. */
  title?: string;
}

/** When the launch sequence hands over from the settled mark to the think loop. */
const LAUNCH_THINK_AT = "1.5s";
const INTRO_AT_S = 0.15;
const CLONE_STAGGER_MS = 55;
/** Think: one lap of the three shadows. */
const THINK_LAP_MS = 800;

/**
 * The gradient mark on a midnight-blue tile.
 */
export function BaiShadowIcon({
  mode = "static",
  shape = "square",
  size,
  title,
  className,
  style,
  ...props
}: BaiShadowIconProps) {
  const scale = iconMarkScale(size, shape);
  const intro = mode === "intro" || mode === "launch";
  const id = useId();
  const tileFill = `url(#${id}-tile)`;
  const markFill = `url(#${id}-mark)`;
  const delay = (ms: number | string): CSSProperties => ({
    animationDelay: typeof ms === "number" ? `${ms}ms` : ms,
  });

  const shadows = (variant: "static" | "grow" | "think") =>
    MARK_SEGMENTS.map((path, index) => (
      <path
        key={path}
        d={path}
        fill={markFill}
        className={cn(
          variant === "grow" && "baishadow-clone-grow",
          variant === "think" && "baishadow-clone-fade",
        )}
        style={
          variant === "grow"
            ? delay(INTRO_AT_S * 1000 + index * CLONE_STAGGER_MS)
            : variant === "think"
              ? delay(index * (THINK_LAP_MS / MARK_SEGMENTS.length) - THINK_LAP_MS)
              : undefined
        }
      />
    ));
  const settled = (
    <g className="baishadow-settle" style={delay(INTRO_AT_S * 1000)}>
      {shadows("grow")}
    </g>
  );

  return (
    <svg
      viewBox="0 0 100 100"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      className={cn("inline-block shrink-0", size == null && "size-[1em]", intro && "baishadow-pop", className)}
      style={{ ...(size != null && { width: size, height: size }), ...(intro && delay("0.1s")), ...style }}
      {...props}
    >
      <defs>
        <BaiShadowGradient id={`${id}-tile`} stops={TILE_STOPS} axis={TILE_GRADIENT} />
        <BaiShadowGradient id={`${id}-mark`} stops={REVERSE_MARK_STOPS} />
      </defs>
      {shape === "circle" ? (
        <circle cx={50} cy={50} r={50} fill={tileFill} />
      ) : (
        <rect width={100} height={100} rx={ICON_TILE_RADIUS} fill={tileFill} />
      )}
      <g transform={markTransform(scale)}>
        {mode === "static" ? shadows("static") : null}
        {mode === "think" ? shadows("think") : null}
        {mode === "intro" ? settled : null}
        {mode === "launch" ? (
          <>
            <g className="baishadow-fade-out" style={delay(LAUNCH_THINK_AT)}>{settled}</g>
            <g className="baishadow-fade-in" style={delay(LAUNCH_THINK_AT)}>{shadows("think")}</g>
          </>
        ) : null}
      </g>
    </svg>
  );
}
