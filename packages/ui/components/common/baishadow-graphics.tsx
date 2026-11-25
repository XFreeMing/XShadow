import { cn } from "../../lib/utils";
import { MARK_PATH } from "../../lib/baishadow-brand";

const MARK_STAGGER_S = 0.11;
/** Space between marks in a row, as a share of one mark's width. */
const ROW_GAP = 0.4;

interface BaiShadowMarkRowProps extends React.ComponentProps<"svg"> {
  /** Number of marks; each one stands for a teammate. */
  count: number;
  /**
   * `center`: marks turn into place from the middle outwards (backgrounds).
   * `join`: left to right, and the last one arrives after a beat (someone just
   * joined). Omit for a static row.
   */
  enter?: "center" | "join";
  /** Seconds before the first mark moves. */
  delay?: number;
  /** Classes for the last mark, e.g. to light up the newest teammate. */
  lastClassName?: string;
}

/**
 * A row of marks, one per teammate. Decorative; fill follows `currentColor`.
 */
export function BaiShadowMarkRow({
  count,
  enter,
  delay = 0,
  lastClassName,
  className,
  ...props
}: BaiShadowMarkRowProps) {
  const step = 100 * (1 + ROW_GAP);
  const width = step * (count - 1) + 100;
  const mid = (count - 1) / 2;
  const delayOf = (i: number) => {
    if (enter === "center") return delay + Math.abs(i - mid) * MARK_STAGGER_S;
    if (i === count - 1) return delay + i * MARK_STAGGER_S + 0.3;
    return delay + i * MARK_STAGGER_S;
  };
  return (
    <svg
      viewBox={`0 0 ${width} 100`}
      fill="currentColor"
      aria-hidden="true"
      className={cn("block", className)}
      {...props}
    >
      {Array.from({ length: count }, (_, i) => (
        <g key={i} transform={`translate(${step * i} 0)`}>
          <path
            d={MARK_PATH}
            className={cn(enter && "baishadow-mark-enter", i === count - 1 && lastClassName)}
            style={enter ? { animationDelay: `${delayOf(i)}s` } : undefined}
          />
        </g>
      ))}
    </svg>
  );
}

interface BaiShadowBleedMarkProps extends React.ComponentProps<"svg"> {
  /** Turn into place once, after this many seconds. Omit for static. */
  enterDelay?: number;
}

/**
 * A large mark meant to bleed off the edges of its container — the big
 * graphic from the brand guidelines. Position it with `className`; fill
 * follows `currentColor`.
 */
export function BaiShadowBleedMark({ enterDelay, className, ...props }: BaiShadowBleedMarkProps) {
  return (
    <svg viewBox="0 0 100 100" fill="currentColor" aria-hidden="true" className={cn("block", className)} {...props}>
      <path
        d={MARK_PATH}
        className={enterDelay != null ? "baishadow-mark-enter" : undefined}
        style={enterDelay != null ? { animationDelay: `${enterDelay}s` } : undefined}
      />
    </svg>
  );
}
