// SPDX-License-Identifier: Apache-2.0

import { ExternalLink, ShieldCheck } from "lucide-react";
import { useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import type { ChatMessage } from "./chat-state";

/** Safe event correlation only. The presentation URL is never stored here. */
export interface AuthorizationHandoff {
  authorizationId: string;
  callId: string;
  displayName: string;
  runId: string;
  sessionId: string;
  status: string;
}

export type AuthorizationOperation = "recheck" | "cancel";

/** Fold only the safe correlation from one observed event into its originating call. */
export function recordAuthorizationEvent(
  messages: ChatMessage[],
  event: { kind: string; payload?: unknown; runId: string },
  sessionId: string,
  activeAssistantId: string,
): ChatMessage[] {
  if (event.kind !== "authorization.required" && event.kind !== "authorization.resolved") {
    return messages;
  }
  if (!event.payload || typeof event.payload !== "object" || Array.isArray(event.payload)) {
    return messages;
  }
  const payload = event.payload as Record<string, unknown>;
  const { authorizationId, callId, displayName, status } = payload;
  if (
    typeof authorizationId !== "string" ||
    !authorizationId ||
    typeof callId !== "string" ||
    !callId ||
    typeof displayName !== "string" ||
    typeof status !== "string" ||
    !status ||
    !sessionId
  )
    return messages;

  const existing = messages
    .flatMap((message) => message.authorizations ?? [])
    .find(
      (authorization) =>
        authorization.sessionId === sessionId && authorization.authorizationId === authorizationId,
    );
  if (existing) {
    if (existing.callId !== callId || (existing.status !== "pending" && status === "pending")) {
      return messages;
    }
    if (existing.status === status) return messages;
    return messages.map((message) =>
      message.authorizations?.some((authorization) => authorization === existing)
        ? {
            ...message,
            authorizations: message.authorizations.map((authorization) =>
              authorization === existing ? { ...authorization, status } : authorization,
            ),
          }
        : message,
    );
  }
  // A resolution without the originating handoff cannot be attached safely.
  if (event.kind !== "authorization.required" || status !== "pending" || !event.runId) {
    return messages;
  }

  const handoff: AuthorizationHandoff = {
    authorizationId,
    callId,
    displayName,
    runId: event.runId,
    sessionId,
    status,
  };
  let target = -1;
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.tools?.some((tool) => tool.id === callId)) {
      target = index;
      break;
    }
  }
  if (target < 0) target = messages.findIndex((message) => message.id === activeAssistantId);
  if (target < 0) {
    return [
      ...messages,
      { authorizations: [handoff], content: "", id: activeAssistantId, role: "assistant" },
    ];
  }
  return messages.map((message, index) =>
    index === target
      ? { ...message, authorizations: [...(message.authorizations ?? []), handoff] }
      : message,
  );
}

export function authorizationStatusLabel(status: string): string {
  switch (status) {
    case "pending":
      return "Pending authorization";
    case "granted":
      return "Access granted";
    case "denied":
      return "Access denied";
    case "expired":
      return "Authorization expired";
    case "cancelled":
      return "Authorization cancelled";
    case "failed":
      return "Authorization failed";
    case "interrupted":
      return "Authorization interrupted";
    case "closed":
      return "Authorization closed";
    default:
      return "Authorization status unknown";
  }
}

/** A small row on the originating call; the shared side-panel host owns review. */
export function AuthorizationReviewTrigger({
  authorization,
  onReview,
}: {
  authorization: AuthorizationHandoff;
  onReview: (authorization: AuthorizationHandoff) => void;
}) {
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2 rounded-lg border bg-background p-2 text-sm">
      <ShieldCheck aria-hidden="true" className="size-4 shrink-0" />
      <span className="font-medium">{authorization.displayName}</span>
      <span className="text-muted-foreground">
        {authorizationStatusLabel(authorization.status)}
      </span>
      <Button onClick={() => onReview(authorization)} size="sm" variant="outline">
        Review authorization
      </Button>
    </div>
  );
}

/** Review content rendered by the existing side-panel frame. */
export function AuthorizationReview({
  authorization,
  disabled = false,
  onOperate,
}: {
  authorization: AuthorizationHandoff;
  disabled?: boolean;
  onOperate: (
    operation: AuthorizationOperation,
    authorization: AuthorizationHandoff,
  ) => Promise<void>;
}) {
  const submitting = useRef(false);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const pending = authorization.status === "pending";
  const presentationPath = `/api/v1/sessions/${encodeURIComponent(authorization.sessionId)}/authorizations/${encodeURIComponent(authorization.authorizationId)}/presentation`;

  async function operate(operation: AuthorizationOperation) {
    if (submitting.current || disabled || !pending || uncertain) return;
    submitting.current = true;
    setBusy(true);
    try {
      await onOperate(operation, authorization);
    } catch {
      // A lost SSE response cannot certify what the daemon committed. A new
      // mutation requires activity reconciliation, never an automatic retry.
      setUncertain(true);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  return (
    <section aria-label="Authorization review" className="space-y-4 p-4 text-sm">
      <div>
        <p className="font-semibold">{authorization.displayName}</p>
        <p className="mt-1 text-muted-foreground" role="status">
          {authorizationStatusLabel(authorization.status)}
        </p>
      </div>
      <p className="break-all text-xs text-muted-foreground">
        Run {authorization.runId} · Call {authorization.callId}
      </p>
      {pending ? (
        <>
          <p className="text-muted-foreground">
            Opening the page does not grant access. Complete the external step, then recheck here.
          </p>
          <a
            className="inline-flex items-center gap-2 rounded-md border px-3 py-2 font-medium underline-offset-2 hover:underline"
            href={presentationPath}
            rel="noopener noreferrer"
            target="_blank"
          >
            Open authorization <ExternalLink aria-hidden="true" className="size-4" />
          </a>
          {uncertain && (
            <p role="alert">The outcome is uncertain. Refresh activity before another action.</p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={busy || disabled || uncertain}
              onClick={() => void operate("recheck")}
              size="sm"
            >
              Recheck
            </Button>
            <Button
              disabled={busy || disabled || uncertain}
              onClick={() => void operate("cancel")}
              size="sm"
              variant="outline"
            >
              Cancel authorization
            </Button>
          </div>
        </>
      ) : (
        <p className="text-muted-foreground">This is the observed authorization outcome.</p>
      )}
    </section>
  );
}
