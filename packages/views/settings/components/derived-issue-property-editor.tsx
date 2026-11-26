"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import { propertyListOptions } from "@multica/core/properties";
import { genericSheetOptions } from "@multica/core/tables";
import type { IssueProperty } from "@multica/core/types";
import { useT } from "../../i18n";

export function IssueLookupPropertyEditor({
  wsId,
  relationPropertyId,
  targetFieldId,
  onRelationChange,
  onTargetFieldChange,
}: {
  wsId: string;
  relationPropertyId: string;
  targetFieldId: string;
  onRelationChange: (propertyId: string) => void;
  onTargetFieldChange: (fieldId: string) => void;
}) {
  const { t } = useT("settings");
  const { data: properties = [] } = useQuery(propertyListOptions(wsId, true));
  const relationProperties = useMemo(
    () => properties.filter((property: IssueProperty) =>
      !property.archived && property.type === "record_relation" && property.config.target_table_id,
    ),
    [properties],
  );
  const selectedRelation = relationProperties.find((property) => property.id === relationPropertyId);
  const targetTableId = selectedRelation?.config.target_table_id ?? "";
  const targetSheetQuery = useQuery({
    ...genericSheetOptions(wsId, targetTableId, null),
    enabled: Boolean(wsId && targetTableId),
  });
  const targetFields = targetSheetQuery.data?.fields ?? [];

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <FieldLabel>{t(($) => $.properties.editor.lookup_relation)}</FieldLabel>
        <Select
          items={relationProperties.map((property) => ({
            value: property.id,
            label: property.name,
          }))}
          value={relationPropertyId || null}
          onValueChange={(value) => value && onRelationChange(value)}
        >
          <SelectTrigger>
            <SelectValue placeholder={t(($) => $.properties.editor.lookup_relation_placeholder)} />
          </SelectTrigger>
          <SelectContent>
            {relationProperties.map((property) => (
              <SelectItem key={property.id} value={property.id}>
                {property.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <FieldLabel>{t(($) => $.properties.editor.lookup_target_field)}</FieldLabel>
        <Select
          items={targetFields.map((field) => ({
            value: field.id,
            label: field.name,
          }))}
          value={targetFieldId || null}
          onValueChange={(value) => value && onTargetFieldChange(value)}
          disabled={!targetTableId}
        >
          <SelectTrigger>
            <SelectValue placeholder={t(($) => $.properties.editor.lookup_target_field_placeholder)} />
          </SelectTrigger>
          <SelectContent>
            {targetFields.map((field) => (
              <SelectItem key={field.id} value={field.id}>
                {field.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}

export function IssueFormulaPropertyEditor({
  expression,
  onExpressionChange,
}: {
  expression: string;
  onExpressionChange: (value: string) => void;
}) {
  const { t } = useT("settings");
  return (
    <div className="space-y-2">
      <FieldLabel htmlFor="formula-expression">
        {t(($) => $.properties.editor.formula_expression)}
      </FieldLabel>
      <Textarea
        id="formula-expression"
        rows={4}
        value={expression}
        onChange={(event) => onExpressionChange(event.target.value)}
        placeholder={t(($) => $.properties.editor.formula_expression_placeholder)}
      />
    </div>
  );
}
