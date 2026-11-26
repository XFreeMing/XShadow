import { redirect } from "next/navigation";
import { paths } from "@multica/core/paths";

export default async function TablesLegacyRedirectPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(paths.workspace(workspaceSlug).issues());
}
