// SPDX-License-Identifier: Apache-2.0

import type { RuntimeResponse, SessionSummaryResponse } from "@mecatl-studio/contracts";
import { clearSession, forkSession } from "@mecatl-studio/contracts/generated";
import {
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  getSessionWorktreesOptions,
  getSoulInspectionOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { Button } from "../../components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../../components/ui/dialog";
import { cn } from "../../lib/utils";
import { SessionDetailsView } from "./session-details-dialog";
import { SoulView } from "./soul-dialog";
import { TranscriptView } from "./transcript-dialog";
import { WorktreePickerView } from "./worktree-picker-dialog";

export type InspectionView = "details" | "transcript" | "soul" | "worktrees";

export function isInspectOnlySession(session: SessionSummaryResponse): boolean {
  return (
    !session.capabilities.publicChat &&
    session.capabilities.publicChatReason === "inspect_only_kind" &&
    !session.debugTargetSessionId
  );
}

export function isProvenChatSession(session: SessionSummaryResponse): boolean {
  return (
    session.capabilities.publicChat ||
    Boolean(session.debugTargetSessionId) ||
    (session.kind === "main" &&
      (session.capabilities.publicChatReason === "awaiting_approval" ||
        session.capabilities.publicChatReason === "active_elsewhere"))
  );
}

interface SessionInspectionProps {
  initialView?: InspectionView;
  onClose: () => void;
  /** Absent when this session is not the open chat: debugging binds to the open chat only. */
  onDebug?: (sessionId: string) => void;
  onOpenSuccessor: (sessionId: string) => void;
  runtime?: RuntimeResponse;
  session: SessionSummaryResponse;
}

const descriptions: Record<Exclude<InspectionView, "transcript">, string> = {
  details: "This session as the daemon records it.",
  soul: "The instructions the daemon adds to every run, as the model receives them.",
  worktrees:
    "Start a chat rooted at another worktree of this repository. This chat stays in the list.",
};

const titles: Record<InspectionView, string> = {
  details: "Session details",
  transcript: "Saved transcript",
  soul: "Soul inspection",
  worktrees: "Choose successor worktree",
};

function unavailable(reason: string, fallback: string): string {
  return reason || fallback;
}

/** Only a selector-specific rejection warrants relisting; other failures are not the choice's fault. */
function isSelectorRejection(caught: unknown): boolean {
  return (
    typeof caught === "object" &&
    caught !== null &&
    "code" in caught &&
    typeof caught.code === "string" &&
    caught.code.startsWith("placement_selector_")
  );
}

export function SessionInspection({
  initialView = "details",
  onClose,
  onDebug,
  onOpenSuccessor,
  runtime,
  session,
}: SessionInspectionProps) {
  const [view, setView] = useState<InspectionView>(initialView);
  const [selectedSelector, setSelectedSelector] = useState<string>();
  const [action, setAction] = useState<"fork" | "clear">();
  const [error, setError] = useState<string>();
  const queryClient = useQueryClient();
  const worktreeOptions = useMemo(
    () => getSessionWorktreesOptions({ path: { sessionId: session.id } }),
    [session.id],
  );
  const online = runtime?.connection === "online";
  const canInspect = session.capabilities.inspect && online;
  const canReadTranscript = canInspect && session.capabilities.viewTranscript;
  const canReadSoul = online && runtime?.capabilities.soul === true;
  const canReadWorktrees = canInspect && runtime?.capabilities.worktrees === true;
  const detail = useQuery({
    ...getSessionDetailOptions({ path: { sessionId: session.id } }),
    enabled: canInspect,
  });
  const transcript = useQuery({
    ...getSessionTranscriptOptions({ path: { sessionId: session.id } }),
    enabled: view === "transcript" && canReadTranscript,
  });
  const soul = useQuery({ ...getSoulInspectionOptions(), enabled: view === "soul" && canReadSoul });
  const worktrees = useQuery({
    ...worktreeOptions,
    enabled: view === "worktrees" && canReadWorktrees,
  });
  // Discovery returns source-scoped selectors. Evict them when the picker is
  // hidden or unmounted, so a later opening must discover fresh choices.
  useEffect(() => {
    if (view !== "worktrees") {
      queryClient.removeQueries({ exact: true, queryKey: worktreeOptions.queryKey });
    }
    return () => {
      if (view === "worktrees") {
        queryClient.removeQueries({ exact: true, queryKey: worktreeOptions.queryKey });
      }
    };
  }, [queryClient, view, worktreeOptions]);

  function changeView(next: InspectionView) {
    setView(next);
    setError(undefined);
    setSelectedSelector(undefined);
  }

  async function createSuccessor(nextAction: "fork" | "clear") {
    const selected = worktrees.data?.items.find((item) => item.selector === selectedSelector);
    if (
      !selected ||
      !session.capabilities.fork ||
      !canReadWorktrees ||
      (nextAction === "fork" && !detail.data?.model)
    )
      return;
    setAction(nextAction);
    setError(undefined);
    try {
      const result =
        nextAction === "fork"
          ? await forkSession({
              body: {
                model: {
                  id: detail.data?.model?.id ?? "",
                  providerId: detail.data?.model?.providerId ?? "",
                },
                reasoningEffort: detail.data?.model?.reasoningEffort ?? "default",
                worktreeSelector: selected.selector,
              },
              path: { sessionId: session.id },
              throwOnError: true,
            })
          : await clearSession({
              body: { worktreeSelector: selected.selector },
              path: { sessionId: session.id },
              throwOnError: true,
            });
      onOpenSuccessor(result.data.id);
      onClose();
    } catch (caught) {
      setSelectedSelector(undefined);
      if (isSelectorRejection(caught)) {
        setError("Selection is stale. Worktrees were refreshed; select one again.");
        await worktrees.refetch();
      } else {
        setError("Could not create a successor in that worktree. Try again after relisting.");
      }
    } finally {
      setAction(undefined);
    }
  }

  const transcriptUnavailable = !online
    ? "Transcript unavailable while offline."
    : unavailable(session.capabilities.viewTranscriptReason, "Transcript unavailable.");
  const soulUnavailable = `Soul inspection unavailable${!online ? " while offline" : " on this connection"}.`;
  const worktreesUnavailable = `Worktrees unavailable${!online ? " while offline" : " for this session"}.`;
  const debugSupported = online && runtime?.capabilities.sessionDebug === true;
  const notes = [
    ...(canReadTranscript ? [] : [transcriptUnavailable]),
    ...(canReadSoul ? [] : [soulUnavailable]),
    ...(canReadWorktrees ? [] : [worktreesUnavailable]),
    ...(!debugSupported
      ? [`Debug unavailable${!online ? " while offline" : " on this connection"}.`]
      : !onDebug
        ? ["Open this chat to debug it with AI."]
        : []),
  ];
  const views: { label: string; view: InspectionView; enabled: boolean }[] = [
    { enabled: true, label: "Details", view: "details" },
    { enabled: canReadTranscript, label: "View transcript", view: "transcript" },
    { enabled: canReadSoul, label: "Inspect soul", view: "soul" },
    { enabled: canReadWorktrees, label: "Choose worktree", view: "worktrees" },
  ];
  const chosen = Boolean(worktrees.data?.items.some((item) => item.selector === selectedSelector));

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open
    >
      <DialogContent className="flex max-h-[min(85vh,48rem)] flex-col gap-4 overflow-hidden sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{titles[view]}</DialogTitle>
          <DialogDescription
            className={view === "transcript" ? "break-all font-mono text-xs" : undefined}
          >
            {view === "transcript"
              ? `${session.title || "Untitled chat"} · ${session.id}`
              : descriptions[view]}
          </DialogDescription>
        </DialogHeader>
        <nav
          aria-label="Inspection views"
          className="-mt-1 flex w-fit max-w-full flex-wrap gap-0.5 rounded-full bg-muted p-1"
        >
          {views.map((item) => (
            <Button
              aria-current={view === item.view ? "page" : undefined}
              className={cn(
                "h-7 rounded-full px-3 text-xs text-muted-foreground hover:text-foreground",
                view === item.view && "bg-background text-foreground shadow-sm hover:bg-background",
              )}
              disabled={!item.enabled}
              key={item.view}
              onClick={() => changeView(item.view)}
              size="sm"
              variant="ghost"
            >
              {item.label}
            </Button>
          ))}
        </nav>
        <div
          className="-mx-6 flex min-h-0 flex-col gap-4 overflow-y-auto px-6 text-sm"
          data-testid="inspection-scrollport"
        >
          {view === "details" && (
            <SessionDetailsView
              canInspect={canInspect}
              detail={detail.data}
              detailError={detail.isError}
              detailPending={detail.isPending}
              inspectUnavailable={unavailable(
                session.capabilities.inspectReason,
                online ? "Inspection unavailable." : "Inspection unavailable while offline.",
              )}
              notes={notes}
              onDebug={
                debugSupported && onDebug
                  ? () => {
                      onDebug(session.id);
                      onClose();
                    }
                  : undefined
              }
              onRetry={() => void detail.refetch()}
              runtime={runtime}
              session={session}
            />
          )}
          {view === "transcript" &&
            (!canReadTranscript ? (
              <p className="text-muted-foreground">{transcriptUnavailable}</p>
            ) : (
              <TranscriptView
                error={transcript.isError}
                onRetry={() => void transcript.refetch()}
                pending={transcript.isPending}
                transcript={transcript.data}
              />
            ))}
          {view === "soul" &&
            (!canReadSoul ? (
              <p className="text-muted-foreground">{soulUnavailable}</p>
            ) : (
              <SoulView
                error={soul.isError}
                onRetry={() => void soul.refetch()}
                pending={soul.isPending}
                soul={soul.data}
              />
            ))}
          {view === "worktrees" &&
            (!canReadWorktrees ? (
              <p className="text-muted-foreground">{worktreesUnavailable}</p>
            ) : (
              <WorktreePickerView
                busy={Boolean(action)}
                canClear={session.capabilities.fork}
                canFork={session.capabilities.fork && Boolean(detail.data?.model)}
                error={worktrees.isError}
                onClear={() => void createSuccessor("clear")}
                onFork={() => void createSuccessor("fork")}
                onRetry={() => void worktrees.refetch()}
                onSelect={setSelectedSelector}
                pending={worktrees.isPending}
                selected={chosen ? selectedSelector : undefined}
                unavailable={unavailable(
                  session.capabilities.forkReason,
                  "Successor creation unavailable.",
                )}
                worktrees={worktrees.data}
              />
            ))}
          {error && (
            <p className="text-destructive" role="alert">
              {error}
            </p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
