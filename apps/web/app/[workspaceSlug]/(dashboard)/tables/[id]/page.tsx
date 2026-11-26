import { redirect } from "next/navigation";
import { paths } from "@multica/core/paths";

export default async function TableSheetLegacyRedirectPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string; id: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(paths.workspace(workspaceSlug).issues());
}
