import { Navigate, useParams } from "react-router-dom";
import { paths } from "@multica/core/paths";

/** Legacy `/tables` routes redirect to the workspace issue list. */
export function TablesLegacyRedirect() {
  const { workspaceSlug } = useParams<{ workspaceSlug: string }>();
  if (!workspaceSlug) return null;
  return <Navigate to={paths.workspace(workspaceSlug).issues()} replace />;
}
