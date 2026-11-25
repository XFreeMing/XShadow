"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Table2 } from "@multica/ui/icons";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { ApiError, api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { genericTableKeys, genericTableListOptions } from "@multica/core/tables";
import { CollectionPageHeader } from "../../layout/collection-page";
import { useT } from "../../i18n";
import { AppLink, useNavigation } from "../../navigation";

export function TablesPage() {
  const { t } = useT("tables");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { push } = useNavigation();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const list = useQuery(genericTableListOptions(wsId));
  const create = useMutation({
    mutationFn: (tableName: string) => api.createGenericTable(tableName),
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: genericTableKeys.list(wsId) });
      push(paths.table(created.table.id, created.view.id));
    },
  });
  const message = create.error instanceof ApiError
    ? create.error.message
    : list.error instanceof ApiError
      ? list.error.message
      : "";

  return (
    <>
      <CollectionPageHeader icon={Table2} title={t(($) => $.list.title)} />
      <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-16">
        <form
          className="flex max-w-xl items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            const tableName = name.trim();
            if (!tableName || create.isPending) return;
            create.mutate(tableName);
          }}
        >
          <Input
            aria-label={t(($) => $.list.name)}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t(($) => $.list.name)}
          />
          <Button type="submit" disabled={create.isPending || name.trim() === ""}>
            {t(($) => $.list.create)}
          </Button>
        </form>
        {message ? <p className="mt-3 text-body text-destructive">{message}</p> : null}
        {list.isLoading ? <p className="mt-6 text-body text-muted-foreground">{t(($) => $.list.loading)}</p> : null}
        {list.data && list.data.tables.length === 0 ? (
          <p className="mt-6 text-body text-muted-foreground">{t(($) => $.list.empty)}</p>
        ) : null}
        <ul className="mt-4 max-w-xl space-y-1">
          {list.data?.tables.map((table) => (
            <li key={table.id}>
              <AppLink
                href={paths.table(table.id)}
                className="flex min-h-9 items-center rounded-md px-3 text-body hover:bg-nav-item-hover"
              >
                {table.name}
              </AppLink>
            </li>
          ))}
        </ul>
      </div>
    </>
  );
}
