// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { type KnowledgeView, KnowledgeWorkspace } from "../features/knowledge/knowledge-workspace";

export const Route = createFileRoute("/workspace/skills")({
  component: SkillsPage,
  validateSearch: (search: Record<string, unknown>) => ({
    item: typeof search.item === "string" ? search.item : undefined,
    view: isKnowledgeView(search.view) ? search.view : ("configured" as const),
  }),
});

function SkillsPage() {
  const navigate = useNavigate();
  const { item, view } = Route.useSearch();
  return (
    <KnowledgeWorkspace
      item={item}
      onItemChange={(nextItem) =>
        nextItem
          ? void navigate({
              params: { item: nextItem, view },
              to: "/workspace/skills/$view/$item",
            })
          : undefined
      }
      onViewChange={(nextView) =>
        void navigate({
          replace: true,
          search: { item: undefined, view: nextView },
          to: "/workspace/skills",
        })
      }
      view={view}
    />
  );
}

function isKnowledgeView(value: unknown): value is KnowledgeView {
  return value === "configured" || value === "learned";
}
