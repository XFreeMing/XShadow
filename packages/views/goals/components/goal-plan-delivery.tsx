"use client";

import {
  GoalPlanDeliveryTypeSchema,
  type GoalPlanDelivery,
} from "@multica/core/types/goal-plan-delivery";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

export interface PlanMethodOption {
  id: string;
  name: string;
  reference: Pick<GoalPlanDelivery, "task_template_id" | "method_id">;
}

function useDeliveryCopy() {
  const { t } = useT("goals");
  return {
    types: {
      feature: t(($) => $.plan.delivery_feature),
      defect: t(($) => $.plan.delivery_defect),
      research: t(($) => $.plan.delivery_research),
      outcome: t(($) => $.plan.delivery_outcome),
    },
    hints: {
      feature: t(($) => $.plan.delivery_feature_hint),
      defect: t(($) => $.plan.delivery_defect_hint),
      research: t(($) => $.plan.delivery_research_hint),
      outcome: t(($) => $.plan.delivery_outcome_hint),
    },
  };
}

export function DeliveryType({ delivery }: { delivery: GoalPlanDelivery }) {
  const { types } = useDeliveryCopy();
  return <span>{types[delivery.type]}</span>;
}

export function PlannedDeliverySummary({ delivery }: { delivery: GoalPlanDelivery }) {
  const { t } = useT("goals");
  return (
    <details className="space-y-2 rounded-md border px-3 py-2">
      <summary className="cursor-pointer text-label">
        <DeliveryType delivery={delivery} /> · {t(($) => $.plan.delivery_criteria)} ({delivery.criteria.length})
      </summary>
      <ol className="space-y-2 text-caption">
        {delivery.criteria.map(({ id, expected, verification }) => (
          <li key={id} className="space-y-1">
            <p className="font-medium">{id} · {expected}</p>
            <p className="whitespace-pre-wrap text-muted-foreground">{verification}</p>
          </li>
        ))}
      </ol>
    </details>
  );
}

export function PlanDeliveryEditor({
  delivery,
  methods,
  disabled,
  onChange,
}: {
  delivery?: GoalPlanDelivery | null;
  methods: PlanMethodOption[];
  disabled: boolean;
  onChange: (value: GoalPlanDelivery) => void;
}) {
  const { t } = useT("goals");
  const { types, hints } = useDeliveryCopy();
  const typesList = GoalPlanDeliveryTypeSchema.options;
  if (!delivery) {
    return (
      <Button type="button" size="sm" variant="outline" disabled={disabled} onClick={() =>
        onChange({ type: "feature", criteria: [{ id: "C1", expected: "", verification: "" }] })
      }>
        {t(($) => $.plan.configure_delivery)}
      </Button>
    );
  }

  const criteria = delivery.criteria;
  const updateCriterion = (index: number, patch: Partial<GoalPlanDelivery["criteria"][number]>) =>
    onChange({ ...delivery, criteria: criteria.map((criterion, i) => index === i ? { ...criterion, ...patch } : criterion) });
  const addCriterion = () => {
    let number = criteria.length + 1;
    while (criteria.some((criterion) => criterion.id === `C${number}`)) number++;
    onChange({ ...delivery, criteria: [...criteria, { id: `C${number}`, expected: "", verification: "" }] });
  };
  const methodLabel = t(($) => $.plan.delivery_method);
  const typeLabel = t(($) => $.plan.delivery_type);
  return (
    <details className="space-y-3 rounded-md border px-3 py-2">
      <summary className="cursor-pointer text-label">
        {types[delivery.type]} · {t(($) => $.plan.delivery_criteria)} ({criteria.length})
      </summary>
      <div className="flex flex-wrap gap-3">
        <div className="space-y-1">
          <Label>{typeLabel}</Label>
          <Select
            value={delivery.type}
            disabled={disabled}
            items={typesList.map((value) => ({ value, label: types[value] }))}
            onValueChange={(next) => {
              const type = GoalPlanDeliveryTypeSchema.safeParse(next);
              if (type.success) onChange({ ...delivery, type: type.data });
            }}
          >
            <SelectTrigger size="sm" aria-label={typeLabel}><SelectValue /></SelectTrigger>
            <SelectContent>
              {typesList.map((type) => <SelectItem key={type} value={type}>{types[type]}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1">
          <Label>{methodLabel}</Label>
          <Select
            value={delivery.task_template_id || delivery.method_id || null}
            disabled={disabled || methods.length === 0}
            items={methods.map(({ id, name }) => ({ value: id, label: name }))}
            onValueChange={(id) => {
              const choice = methods.find((method) => method.id === id);
              if (choice) onChange({ type: delivery.type, criteria, ...choice.reference });
            }}
          >
            <SelectTrigger size="sm" aria-label={methodLabel}>
              <SelectValue placeholder={t(($) => $.plan.delivery_method_choose)} />
            </SelectTrigger>
            <SelectContent>
              {methods.map(({ id, name }) => <SelectItem key={id} value={id}>{name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      </div>
      <p className="text-caption text-muted-foreground">{hints[delivery.type]}</p>
      {criteria.map((criterion, index) => (
        <fieldset key={index} className="grid gap-2 border-t pt-2 sm:grid-cols-2">
          <label className="space-y-1 sm:col-span-2">
            <span className="text-caption">{t(($) => $.plan.delivery_criterion_id)}</span>
            <Input className="w-32" value={criterion.id} disabled={disabled}
              onChange={(event) => updateCriterion(index, { id: event.target.value })} />
          </label>
          <label className="space-y-1">
            <span className="text-caption">{t(($) => $.plan.delivery_expected)}</span>
            <Textarea value={criterion.expected} disabled={disabled}
              onChange={(event) => updateCriterion(index, { expected: event.target.value })} />
          </label>
          <label className="space-y-1">
            <span className="text-caption">{t(($) => $.plan.delivery_verification)}</span>
            <Textarea value={criterion.verification} disabled={disabled}
              onChange={(event) => updateCriterion(index, { verification: event.target.value })} />
          </label>
          {criteria.length > 1 && (
            <Button type="button" size="xs" variant="ghost" disabled={disabled}
              onClick={() => onChange({ ...delivery, criteria: criteria.filter((_, i) => i !== index) })}>
              {t(($) => $.plan.delivery_remove_criterion, { id: criterion.id })}
            </Button>
          )}
        </fieldset>
      ))}
      <Button type="button" size="sm" variant="outline" disabled={disabled || criteria.length >= 30} onClick={addCriterion}>
        {t(($) => $.plan.delivery_new_criterion)}
      </Button>
    </details>
  );
}
