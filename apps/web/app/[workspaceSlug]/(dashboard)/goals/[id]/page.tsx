"use client";

import { use } from "react";
import { GoalDetailPage } from "@multica/views/goals/components";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <GoalDetailPage goalId={id} />;
}