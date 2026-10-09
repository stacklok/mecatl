// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, Navigate, useNavigate } from "@tanstack/react-router";
import { ConfiguredSkillDetail } from "../features/knowledge/configured-skill-detail";
import { type KnowledgeView, KnowledgeWorkspace } from "../features/knowledge/knowledge-workspace";

export const Route = createFileRoute("/workspace/skills_/$view/$item")({
  component: KnowledgeDetailPage,
});

function KnowledgeDetailPage() {
  const navigate = useNavigate();
  const { item, view } = Route.useParams();
  if (!isKnowledgeView(view)) {
    return <Navigate search={{ item: undefined, view: "configured" }} to="/workspace/skills" />;
  }
  if (view === "learned")
    return (
      <KnowledgeWorkspace
        item={item}
        onItemChange={(nextItem) =>
          nextItem
            ? void navigate({
                params: { item: nextItem, view },
                to: "/workspace/skills/$view/$item",
              })
            : void navigate({ search: { item: undefined, view }, to: "/workspace/skills" })
        }
        onViewChange={(nextView) =>
          void navigate({ search: { item: undefined, view: nextView }, to: "/workspace/skills" })
        }
        view={view}
      />
    );
  return <ConfiguredSkillDetail name={item} />;
}

function isKnowledgeView(value: string): value is KnowledgeView {
  return value === "configured" || value === "learned";
}
