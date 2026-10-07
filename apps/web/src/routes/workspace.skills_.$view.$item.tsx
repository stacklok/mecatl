// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, Navigate } from "@tanstack/react-router";
import { ConfiguredSkillDetail } from "../features/knowledge/configured-skill-detail";
import type { KnowledgeView } from "../features/knowledge/knowledge-workspace";
import { LearnedSkillDetail } from "../features/knowledge/learned-skill-detail";

export const Route = createFileRoute("/workspace/skills_/$view/$item")({
  component: KnowledgeDetailPage,
});

function KnowledgeDetailPage() {
  const { item, view } = Route.useParams();
  if (!isKnowledgeView(view)) {
    return <Navigate search={{ item: undefined, view: "configured" }} to="/workspace/skills" />;
  }
  if (view === "learned") return <LearnedSkillDetail skillId={item} />;
  return <ConfiguredSkillDetail name={item} />;
}

function isKnowledgeView(value: string): value is KnowledgeView {
  return value === "configured" || value === "learned";
}
