// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, Navigate, useNavigate } from "@tanstack/react-router";
import { type KnowledgeView, KnowledgeWorkspace } from "../features/knowledge/knowledge-workspace";
import { LearnedSkillDetail } from "../features/knowledge/learned-skill-detail";

export const Route = createFileRoute("/workspace/skills_/$view/$item")({
  component: KnowledgeDetailPage,
});

function KnowledgeDetailPage() {
  const navigate = useNavigate();
  const { item, view } = Route.useParams();
  if (!isKnowledgeView(view)) {
    return <Navigate search={{ item: undefined, view: "configured" }} to="/workspace/skills" />;
  }
  if (view === "learned") return <LearnedSkillDetail skillId={item} />;
  return (
    <KnowledgeWorkspace
      item={item}
      onItemChange={(nextItem) => {
        if (nextItem) {
          void navigate({ params: { item: nextItem, view }, to: "/workspace/skills/$view/$item" });
        } else {
          void navigate({ search: { item: undefined, view }, to: "/workspace/skills" });
        }
      }}
      onViewChange={(nextView) =>
        void navigate({ search: { item: undefined, view: nextView }, to: "/workspace/skills" })
      }
      view={view}
    />
  );
}

function isKnowledgeView(value: string): value is KnowledgeView {
  return value === "configured" || value === "learned";
}
