// SPDX-License-Identifier: Apache-2.0

import { ArrowRight } from "lucide-react";
import { chipClass } from "./draft-greeting";
import { formatRelativeTime, type LatestChatSummary } from "./latest-chat";

/**
 * The draft screen's one-click "continue the newest chat" affordance —
 * rendered only when there's an eligible pick (see `pickLatestEligibleChat`).
 * The whole label is the button's name, so "Continue …" is reachable by role.
 */
export function ContinueLatestChip({
  latest,
  onContinue,
}: {
  latest: LatestChatSummary | undefined;
  onContinue: (id: string) => void;
}) {
  if (!latest) return null;
  const title = latest.title.trim() || "Untitled chat";
  const when = formatRelativeTime(latest.updatedAt);

  return (
    <div className="mt-3 flex justify-center">
      <button
        className={`${chipClass} inline-flex items-center gap-2`}
        onClick={() => onContinue(latest.id)}
        title="Continue the most recent chat"
        type="button"
      >
        <ArrowRight aria-hidden="true" className="size-4 shrink-0" />
        <span className="min-w-0 truncate">
          Continue <span className="text-foreground">“{title}”</span>
          {when && <span className="text-muted-foreground"> · {when}</span>}
        </span>
      </button>
    </div>
  );
}
