// SPDX-License-Identifier: Apache-2.0

import type { RuntimeResponse, SessionSummaryResponse } from "@mecatl-studio/contracts";
import { clearSession, forkSession } from "@mecatl-studio/contracts/generated";
import {
  getSessionDetailOptions,
  getSessionTranscriptOptions,
  getSessionWorktreesOptions,
  getSoulInspectionOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "../../components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "../../components/ui/dialog";

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
  onDebug: (sessionId: string) => void;
  onOpenSuccessor: (sessionId: string) => void;
  runtime?: RuntimeResponse;
  session: SessionSummaryResponse;
}

const titles: Record<InspectionView, string> = {
  details: "Session details",
  transcript: "Saved transcript",
  soul: "Soul inspection",
  worktrees: "Choose successor worktree",
};

function unavailable(reason: string, fallback: string): string {
  return reason || fallback;
}

function isConflict(caught: unknown): boolean {
  return (
    typeof caught === "object" && caught !== null && "status" in caught && caught.status === 409
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
    ...getSessionWorktreesOptions({ path: { sessionId: session.id } }),
    enabled: view === "worktrees" && canReadWorktrees,
  });

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
      if (isConflict(caught)) {
        setError("Selection is stale. Worktrees were refreshed; select one again.");
        await worktrees.refetch();
      } else {
        setError("Could not create a successor in that worktree. Try again after relisting.");
      }
    } finally {
      setAction(undefined);
    }
  }

  return (
    <Dialog
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      open
    >
      <DialogContent
        aria-describedby={undefined}
        className="flex max-h-[min(85vh,48rem)] flex-col overflow-hidden sm:max-w-2xl"
      >
        <DialogHeader>
          <DialogTitle>{titles[view]}</DialogTitle>
        </DialogHeader>
        <nav aria-label="Inspection views" className="flex flex-wrap gap-2">
          <Button
            onClick={() => changeView("details")}
            size="sm"
            variant={view === "details" ? "secondary" : "outline"}
          >
            Details
          </Button>
          <Button
            disabled={!canReadTranscript}
            onClick={() => changeView("transcript")}
            size="sm"
            variant={view === "transcript" ? "secondary" : "outline"}
          >
            View transcript
          </Button>
          <Button
            disabled={!canReadSoul}
            onClick={() => changeView("soul")}
            size="sm"
            variant={view === "soul" ? "secondary" : "outline"}
          >
            Inspect soul
          </Button>
          <Button
            disabled={!canReadWorktrees}
            onClick={() => changeView("worktrees")}
            size="sm"
            variant={view === "worktrees" ? "secondary" : "outline"}
          >
            Choose worktree
          </Button>
        </nav>
        <div className="min-h-0 overflow-y-auto text-sm" data-testid="inspection-scrollport">
          {view === "details" && (
            <>
              {!canInspect ? (
                <p>
                  {unavailable(
                    session.capabilities.inspectReason,
                    online ? "Inspection unavailable." : "Inspection unavailable while offline.",
                  )}
                </p>
              ) : detail.isPending ? (
                <p>Loading details…</p>
              ) : detail.isError ? (
                <p>Session details could not be read.</p>
              ) : (
                detail.data && (
                  <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 break-words">
                    <dt>ID</dt>
                    <dd>{session.capabilities.copyId ? session.id : "Unavailable"}</dd>
                    <dt>Kind</dt>
                    <dd>{detail.data.kind}</dd>
                    <dt>State</dt>
                    <dd>{detail.data.state}</dd>
                    <dt>Model</dt>
                    <dd>{detail.data.model?.id ?? session.modelId ?? "Unavailable"}</dd>
                    <dt>Input tokens</dt>
                    <dd>{detail.data.usage.inputTokens}</dd>
                    <dt>Output tokens</dt>
                    <dd>{detail.data.usage.outputTokens}</dd>
                    <dt>Cache read tokens</dt>
                    <dd>{detail.data.usage.cacheReadTokens}</dd>
                    <dt>Cache write tokens</dt>
                    <dd>{detail.data.usage.cacheWriteTokens}</dd>
                    <dt>Reasoning tokens</dt>
                    <dd>{detail.data.usage.reasoningTokens}</dd>
                    {detail.data.placement && (
                      <>
                        <dt>Placement</dt>
                        <dd>{detail.data.placement.label}</dd>
                        <dt>Branch</dt>
                        <dd>{detail.data.placement.branch}</dd>
                        <dt>Revision</dt>
                        <dd>{detail.data.placement.revision}</dd>
                      </>
                    )}
                  </dl>
                )
              )}
              {session.capabilities.copyId ? (
                <Button
                  onClick={() => void navigator.clipboard.writeText(session.id)}
                  size="sm"
                  variant="outline"
                >
                  Copy session ID
                </Button>
              ) : (
                <p>{unavailable(session.capabilities.copyIdReason, "ID copying unavailable.")}</p>
              )}
              {!canReadTranscript && (
                <p>
                  {!online
                    ? "Transcript unavailable while offline."
                    : unavailable(
                        session.capabilities.viewTranscriptReason,
                        "Transcript unavailable.",
                      )}
                </p>
              )}
              {!canReadSoul && (
                <p>
                  Soul inspection unavailable{!online ? " while offline" : " on this connection"}.
                </p>
              )}
              {!canReadWorktrees && (
                <p>Worktrees unavailable{!online ? " while offline" : " for this session"}.</p>
              )}
              {online && runtime?.capabilities.sessionDebug ? (
                <Button
                  onClick={() => {
                    onDebug(session.id);
                    onClose();
                  }}
                  size="sm"
                  variant="outline"
                >
                  Debug with AI
                </Button>
              ) : (
                <p>Debug unavailable{!online ? " while offline" : " on this connection"}.</p>
              )}
            </>
          )}
          {view === "transcript" &&
            (!canReadTranscript ? (
              <p>
                {!online
                  ? "Transcript unavailable while offline."
                  : unavailable(
                      session.capabilities.viewTranscriptReason,
                      "Transcript unavailable.",
                    )}
              </p>
            ) : transcript.isPending ? (
              <p>Loading saved transcript…</p>
            ) : transcript.isError ? (
              <p>Saved transcript could not be read.</p>
            ) : (
              transcript.data && (
                <>
                  {!transcript.data.complete && <p role="status">Incomplete saved transcript</p>}
                  <ol className="space-y-3">
                    {transcript.data.messages.map((message, index) => (
                      // biome-ignore lint/suspicious/noArrayIndexKey: the saved transcript has no durable row ID; its ordinal identifies the occurrence
                      <li className="rounded border p-3" key={index}>
                        <strong>{message.role}</strong>
                        <p className="whitespace-pre-wrap break-words">{message.text}</p>
                      </li>
                    ))}
                  </ol>
                </>
              )
            ))}
          {view === "soul" &&
            (!canReadSoul ? (
              <p>
                Soul inspection unavailable{!online ? " while offline" : " on this connection"}.
              </p>
            ) : soul.isPending ? (
              <p>Loading soul…</p>
            ) : soul.isError ? (
              <p>Soul could not be read.</p>
            ) : (
              soul.data && (
                <>
                  <p>
                    {soul.data.present
                      ? `Provenance: ${soul.data.provenance}`
                      : "No soul is present."}
                  </p>
                  <pre className="whitespace-pre-wrap break-words">{soul.data.content}</pre>
                </>
              )
            ))}
          {view === "worktrees" &&
            (!canReadWorktrees ? (
              <p>Worktrees unavailable{!online ? " while offline" : " for this session"}.</p>
            ) : worktrees.isPending ? (
              <p>Loading worktrees…</p>
            ) : worktrees.isError ? (
              <p>Worktrees could not be read.</p>
            ) : (
              worktrees.data && (
                <>
                  {worktrees.data.items.length === 0 && (
                    <p>No eligible worktrees for this session.</p>
                  )}
                  <fieldset>
                    <legend className="sr-only">Eligible worktrees</legend>
                    {worktrees.data.items.map((item) => (
                      <label className="flex items-center gap-2 py-2" key={item.selector}>
                        <input
                          checked={selectedSelector === item.selector}
                          name="successor-worktree"
                          onChange={() => setSelectedSelector(item.selector)}
                          type="radio"
                        />
                        <span>
                          {item.label}
                          {item.branch && ` · ${item.branch}`}
                          {item.revision && ` · ${item.revision}`}
                        </span>
                      </label>
                    ))}
                  </fieldset>
                  {session.capabilities.fork && detail.data?.model && (
                    <Button
                      disabled={
                        !worktrees.data.items.some((item) => item.selector === selectedSelector) ||
                        Boolean(action)
                      }
                      onClick={() => void createSuccessor("fork")}
                      size="sm"
                    >
                      Fork in selected worktree
                    </Button>
                  )}
                  {session.capabilities.fork && (
                    <Button
                      disabled={
                        !worktrees.data.items.some((item) => item.selector === selectedSelector) ||
                        Boolean(action)
                      }
                      onClick={() => void createSuccessor("clear")}
                      size="sm"
                      variant="outline"
                    >
                      Clear in selected worktree
                    </Button>
                  )}
                  {!session.capabilities.fork && (
                    <p>
                      {unavailable(
                        session.capabilities.forkReason,
                        "Successor creation unavailable.",
                      )}
                    </p>
                  )}
                </>
              )
            ))}
          {error && <p role="alert">{error}</p>}
        </div>
      </DialogContent>
    </Dialog>
  );
}
