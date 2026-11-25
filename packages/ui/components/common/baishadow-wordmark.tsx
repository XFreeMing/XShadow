"use client";

import { useId } from "react";
import { cn } from "../../lib/utils";
import { MARK_STOPS, REVERSE_MARK_STOPS, WORDMARK } from "../../lib/baishadow-brand";
import { BaiShadowGradient } from "./baishadow-gradient";

interface BaiShadowWordmarkProps extends React.ComponentProps<"svg"> {
  /** Reveal the mark and lettering in sequence after this delay. */
  writeDelayMs?: number;
  /** Animate the lockup on hover in the website navigation. */
  hop?: boolean;
  appearance?: "auto" | "light" | "dark";
  title?: string;
}

export function BaiShadowWordmark({
  writeDelayMs,
  hop = false,
  appearance = "auto",
  title = "百影 BaiShadow",
  className,
  ...props
}: BaiShadowWordmarkProps) {
  const id = useId();
  return (
    <svg
      viewBox={WORDMARK.viewBox}
      role="img"
      aria-label={title}
      fill="currentColor"
      fillRule="evenodd"
      className={cn(
        "inline-block h-[1em] w-auto shrink-0 overflow-visible",
        appearance === "auto" && "text-baishadow-ink dark:text-baishadow-paper",
        appearance === "light" && "text-baishadow-ink",
        appearance === "dark" && "text-baishadow-paper",
        hop && "baishadow-hop-on-hover",
        className,
      )}
      {...props}
    >
      <defs>
        {appearance !== "dark" && <BaiShadowGradient id={`${id}-light`} stops={MARK_STOPS} />}
        {appearance !== "light" && <BaiShadowGradient id={`${id}-dark`} stops={REVERSE_MARK_STOPS} />}
      </defs>
      {[WORDMARK.symbol, WORDMARK.name, WORDMARK.english].map((path, index) => (
        <g key={path} style={hop ? { animationDelay: `${index * 60}ms` } : undefined}>
          <g
            className={writeDelayMs != null ? "baishadow-fade-in" : undefined}
            style={writeDelayMs != null ? { animationDelay: `${writeDelayMs + index * 150}ms` } : undefined}
          >
            {index === 0 ? (
              <>
                {appearance !== "dark" && (
                  <path d={path} fill={`url(#${id}-light)`} className={appearance === "auto" ? "dark:hidden" : undefined} />
                )}
                {appearance !== "light" && (
                  <path d={path} fill={`url(#${id}-dark)`} className={appearance === "auto" ? "hidden dark:block" : undefined} />
                )}
              </>
            ) : (
              <path d={path} opacity={index === 2 ? 0.82 : undefined} />
            )}
          </g>
        </g>
      ))}
    </svg>
  );
}
