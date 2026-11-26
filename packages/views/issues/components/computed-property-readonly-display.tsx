"use client";

import type { IssueComputedPropertyValue } from "@multica/core/types";
import { useT } from "../../i18n";

export function formatComputedPropertyValue(
  value: IssueComputedPropertyValue,
): string {
  if (Array.isArray(value)) {
    return value.map(formatComputedPropertyValue).join(", ");
  }
  return value === null ? "" : String(value);
}

/** Read-only presentation for server-projected lookup / formula values. */
export function ComputedPropertyReadonlyDisplay({
  value,
}: {
  value: IssueComputedPropertyValue | undefined;
}) {
  const { t } = useT("issues");
  if (value === undefined) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.pickers.custom_property.empty)}
      </span>
    );
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      return (
        <span className="text-muted-foreground">
          {t(($) => $.pickers.custom_property.empty)}
        </span>
      );
    }
    return (
      <span className="flex min-w-0 flex-wrap items-center gap-1">
        {value.map((item, index) => (
          <span
            key={`${index}:${String(item)}`}
            className="inline-flex max-w-48 items-center rounded-full border border-surface-border px-1.5 py-px text-micro"
          >
            <span className="truncate">{formatComputedPropertyValue(item)}</span>
          </span>
        ))}
      </span>
    );
  }
  if (value === null) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.pickers.custom_property.empty)}
      </span>
    );
  }
  const label = formatComputedPropertyValue(value);
  return (
    <span className="min-w-0 truncate tabular-nums" title={label}>
      {label}
    </span>
  );
}