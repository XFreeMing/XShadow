import { useParams } from "react-router-dom";
import { TableSheetPage as TableSheetView } from "@multica/views/tables/components";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function TableSheetPage() {
  const { id } = useParams<{ id: string }>();
  useDocumentTitle("表格");
  if (!id) return null;
  return <TableSheetView tableId={id} />;
}
