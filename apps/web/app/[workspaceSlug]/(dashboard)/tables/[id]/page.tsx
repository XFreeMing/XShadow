"use client";

import { use } from "react";
import { TableSheetPage } from "@multica/views/tables/components";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <TableSheetPage tableId={id} />;
}
