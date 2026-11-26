import type { ReactNode } from 'react';

export interface TemplateGridColumn {
  id: string;
  name: string;
  typeLabel?: ReactNode;
  icon?: ReactNode;
  required: boolean;
  defaultValue?: ReactNode;
  formula?: string;
  description?: string;
}

export interface TemplateGridLabels {
  title: string;
  required: string;
  defaults: string;
  formulas: string;
  description: string;
  empty: string;
}

export interface TaskTemplateGridProps {
  columns: readonly TemplateGridColumn[];
  labels: TemplateGridLabels;
  className?: string;
}

function hasText(value?: string): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

function renderDefaultValue(value: ReactNode): ReactNode {
  if (value === null || value === undefined) {
    return null;
  }
  if (typeof value === 'boolean') {
    return <span>{String(value)}</span>;
  }
  if (typeof value === 'number') {
    return <span>{value}</span>;
  }
  if (typeof value === 'string') {
    return value.trim().length > 0 ? <span>{value}</span> : null;
  }
  return value;
}

const STICKY_COLUMN_CLASS =
  'sticky left-0 w-24 min-w-[6rem] max-w-[7.5rem] border-r border-surface-border bg-muted px-2.5 py-2 align-top sm:w-32 sm:min-w-[8rem] sm:max-w-[9rem]';

const DATA_COLUMN_CLASS =
  'min-w-[9.5rem] max-w-[15rem] border-r border-surface-border px-3 py-2 align-top last:border-r-0';

export function TaskTemplateGrid({
  columns,
  labels,
  className,
}: TaskTemplateGridProps): ReactNode {
  const hasFormulas = columns.some((column) => hasText(column.formula));
  const hasDescriptions = columns.some((column) => hasText(column.description));

  const rootClassName = [
    'w-full max-w-full overflow-hidden rounded-lg border border-surface-border bg-card',
    className,
  ]
    .filter(Boolean)
    .join(' ');

  if (columns.length === 0) {
    return (
      <div className={rootClassName}>
        <div className="border-b border-surface-border bg-muted px-3 py-2">
          <div className="truncate text-label font-medium text-foreground">
            {labels.title}
          </div>
        </div>
        <div className="px-3 py-6 text-center text-caption text-muted-foreground">
          {labels.empty}
        </div>
      </div>
    );
  }

  return (
    <div className={rootClassName}>
      <div className="border-b border-surface-border bg-muted px-3 py-2">
        <div className="truncate text-label font-medium text-foreground">
          {labels.title}
        </div>
      </div>

      <div
        role="region"
        aria-label={labels.title}
        tabIndex={0}
        className="w-full max-w-full overflow-x-auto focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-muted-foreground"
      >
        <table
          aria-label={labels.title}
          className="w-full min-w-max border-collapse text-left"
        >
          <thead>
            <tr className="border-b border-surface-border bg-muted">
              <td
                aria-hidden="true"
                className={`${STICKY_COLUMN_CLASS} z-20`}
              />
              {columns.map((column) => (
                <th
                  key={column.id}
                  scope="col"
                  className={`${DATA_COLUMN_CLASS} font-normal`}
                >
                  <div className="flex flex-col gap-1">
                    <div className="flex min-w-0 items-center gap-1.5">
                      {column.icon !== undefined && column.icon !== null && (
                        <span
                          aria-hidden="true"
                          className="inline-flex shrink-0 items-center text-muted-foreground"
                        >
                          {column.icon}
                        </span>
                      )}
                      <span className="truncate text-label font-medium text-foreground">
                        {column.name}
                      </span>
                    </div>
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="truncate text-caption text-muted-foreground">
                        {column.typeLabel}
                      </span>
                      {column.required && (
                        <span className="inline-flex shrink-0 items-center rounded border border-surface-border bg-card px-1.5 py-0.5 text-caption font-medium text-foreground">
                          {labels.required}
                        </span>
                      )}
                    </div>
                  </div>
                </th>
              ))}
            </tr>
          </thead>

          <tbody className="bg-card">
            <tr className="border-b border-surface-border last:border-b-0">
              <th
                scope="row"
                className={`${STICKY_COLUMN_CLASS} z-10 text-left text-caption font-medium text-muted-foreground`}
              >
                <span className="block break-words">{labels.defaults}</span>
              </th>
              {columns.map((column) => {
                const defaultNode = renderDefaultValue(column.defaultValue);
                return (
                  <td
                    key={column.id}
                    className={`${DATA_COLUMN_CLASS} bg-card text-caption text-foreground`}
                  >
                    {defaultNode !== null && (
                      <div className="break-words">{defaultNode}</div>
                    )}
                  </td>
                );
              })}
            </tr>

            {hasFormulas && (
              <tr className="border-b border-surface-border last:border-b-0">
                <th
                  scope="row"
                  className={`${STICKY_COLUMN_CLASS} z-10 text-left text-caption font-medium text-muted-foreground`}
                >
                  <span className="block break-words">{labels.formulas}</span>
                </th>
                {columns.map((column) => (
                  <td
                    key={column.id}
                    className={`${DATA_COLUMN_CLASS} bg-card`}
                  >
                    {hasText(column.formula) && (
                      <code className="block whitespace-pre-wrap break-words rounded border border-surface-border bg-muted px-1.5 py-1 font-mono text-caption leading-relaxed text-foreground">
                        {column.formula}
                      </code>
                    )}
                  </td>
                ))}
              </tr>
            )}

            {hasDescriptions && (
              <tr className="border-b border-surface-border last:border-b-0">
                <th
                  scope="row"
                  className={`${STICKY_COLUMN_CLASS} z-10 text-left text-caption font-medium text-muted-foreground`}
                >
                  <span className="block break-words">
                    {labels.description}
                  </span>
                </th>
                {columns.map((column) => (
                  <td
                    key={column.id}
                    className={`${DATA_COLUMN_CLASS} bg-card text-caption leading-relaxed text-muted-foreground`}
                  >
                    {hasText(column.description) && (
                      <p className="m-0 whitespace-pre-wrap break-words">
                        {column.description}
                      </p>
                    )}
                  </td>
                ))}
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
