"use client";

import { useParams, useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { useAgentMemory } from "@/features/agent";
import { pageTitleClass } from "@/lib/typography";
import { MemoryFactDetail } from "./_components/memory-fact-detail";

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
            Memory is turned off for this agent.
          </p>
          {memory.disabledReason && (
            <p className="text-sm text-muted-foreground">
              {memory.disabledReason}
            </p>
          )}
        </div>
      );
    }
    // In-page, like the schedules and skills detail pages — never Next's
    // notFound(), which is terminal for the navigation: the index this page
    // reads lands only once the runtime probe settles, so a missing entry
    // must stay recoverable (and honest about an unreachable daemon).
    return (
      <div className="flex min-h-[60vh] flex-col items-center justify-center px-4">
        <div className="w-full max-w-md rounded-xl border bg-card p-8 text-center">
          <h1 className="text-lg font-semibold">Memory not found</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            {memory.harnessLive ? (
              <>
                No remembered fact has the key{" "}
                <code className="font-mono text-xs">{key}</code> — the agent may
                have forgotten or renamed it.
              </>
            ) : (
              "Studio can't reach the agent, so this fact can't be looked up right now."
            )}
          </p>
          <Button
            className="mt-6"
            onClick={() => router.push("/workspace/settings/memory")}
          >
            Back to Memory
          </Button>
        </div>
      </div>
    );
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

        {/* The fact's VALUE, provenance and revisions come from the key-scoped
            read (the TUI's lazy `enter`); the index above only named it. */}
        <MemoryFactDetail
          entryKey={entry.id}
          indexDescription={entry.content}
        />
      </div>
    </div>
  );
}
