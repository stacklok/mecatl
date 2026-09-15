"use client";

import { notFound, useParams, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { useAgentMemory } from "@/features/agent";
import { pageTitleClass } from "@/lib/typography";

/**
 * A remembered fact as its own full page — deliberately OUTSIDE the settings
 * layout (no settings nav), the same dedicated-detail treatment skills and
 * schedules get. The list it backs out to stays under Settings → Memory.
 */
export default function MemoryDetailPage() {
  const router = useRouter();
  const params = useParams<{ memoryId: string }>();
  const memory = useAgentMemory();
  // The route segment arrives URL-encoded; entry ids are the raw store keys.
  const key = decodeURIComponent(params.memoryId);
  const entry = memory.entries.find((e) => e.id === key);

  if (!entry) {
    if (memory.isLoading) {
      return (
        <div className="flex min-h-[40vh] items-center justify-center text-sm text-muted-foreground">
          Loading…
        </div>
      );
    }
    if (!memory.isSupported) {
      return (
        <div className="flex min-h-[40vh] flex-col items-center justify-center gap-2 px-6 text-center">
          <p className="text-sm font-medium">
            Memory is disabled on this daemon
          </p>
          {memory.disabledReason && (
            <p className="text-sm text-muted-foreground">
              {memory.disabledReason}
            </p>
          )}
        </div>
      );
    }
    return notFound();
  }

  return (
    <div className="h-full overflow-y-auto px-3 pt-6 pb-8 min-[500px]:px-4">
      <div className="space-y-5">
        <Button
          variant="outline"
          size="sm"
          className="h-9 w-fit gap-1 self-start rounded-full px-4"
          onClick={() => router.push("/workspace/settings/memory")}
        >
          <span aria-hidden="true">‹</span>
          Back
        </Button>

        {/* The schedules/skills detail grammar: serif title, pill row, the
            content leading unlabelled as its own card, then grouped facts. */}
        <h1
          className={pageTitleClass(
            "break-all text-[44px] leading-[1.05] max-[499px]:text-3xl",
          )}
        >
          {entry.title}
        </h1>

        <div className="max-w-4xl space-y-8">
          <p className="rounded-xl border bg-card p-5 text-sm leading-relaxed whitespace-pre-wrap">
            {entry.content || "No description recorded."}
          </p>

          <div className="space-y-2">
            <h2 className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
              Details
            </h2>
            <div className="divide-y rounded-lg border bg-background">
              <div className="flex items-center justify-between gap-3 px-4 py-3">
                <span className="text-sm">Key</span>
                <span className="break-all text-right font-mono text-sm text-muted-foreground">
                  {entry.id}
                </span>
              </div>
              <div className="flex items-center justify-between gap-3 px-4 py-3">
                <span className="text-sm">Source</span>
                <span className="text-right text-sm text-muted-foreground">
                  Learned in conversation
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
