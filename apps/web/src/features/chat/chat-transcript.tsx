// SPDX-License-Identifier: Apache-2.0

import { ExternalLink, MessageSquareText } from "lucide-react";
import { memo, useCallback, useRef } from "react";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { type AuthorizationHandoff, AuthorizationReviewTrigger } from "./authorization-review";
import { approvalMatchesToolCall, type ChatMessage } from "./chat-state";
import {
  type DelegationActivity,
  DelegationCardRow,
  type DelegationFocus,
} from "./delegation-card";
import { FailedTurnCard } from "./failed-turn-card";
import { type ChatImage, chatImageDisplay } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import type { ToolActivity } from "./tool-activity";

const EMPTY_APPROVALS: ApprovalRequest[] = [];

function approvalsForMessage(
  message: ChatMessage,
  approvals: ApprovalRequest[],
): ApprovalRequest[] {
  const matched = approvals.filter((approval) =>
    message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
  );
  return matched.length ? matched : EMPTY_APPROVALS;
}

export interface TranscriptScrollMetrics {
  clientHeight: number;
  scrollHeight: number;
  scrollTop: number;
}

/** The reader controls following by proximity, not by whether a token arrived. */
export function isNearTranscriptBottom(metrics: TranscriptScrollMetrics): boolean {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight <= 80;
}

export interface TranscriptRowState {
  approvals?: ApprovalRequest[];
  delegations?: DelegationActivity[];
  message: ChatMessage;
  showToolCalls: boolean;
  streaming: boolean;
}

/** Object identity is stable for historical rows while the active row receives deltas. */
export function shouldUpdateTranscriptRow(
  previous: TranscriptRowState,
  next: TranscriptRowState,
): boolean {
  return (
    previous.approvals !== next.approvals ||
    previous.delegations !== next.delegations ||
    previous.message !== next.message ||
    previous.showToolCalls !== next.showToolCalls ||
    previous.streaming !== next.streaming
  );
}

interface TranscriptRowProps extends TranscriptRowState {
  agentName: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  onOpenActivity?: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
  onOpenThread?: (message: ChatMessage) => void;
  onReviewAuthorization?: (authorization: AuthorizationHandoff) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  threadDisabled: boolean;
  threadSessionId?: string;
  userName: string;
}

function TranscriptRow({
  agentName,
  approvalDisabled,
  approvalUncertain,
  approvals,
  delegations,
  message,
  onOpenActivity,
  onOpenThread,
  onReviewAuthorization,
  onPreviewImage,
  onPreviewTool,
  onRespondToApproval,
  showToolCalls,
  streaming,
  threadDisabled,
  threadSessionId,
  userName,
}: TranscriptRowProps) {
  const user = message.role === "user";
  const label = message.delivery
    ? `Scheduled task ${message.delivery.scheduleName}`
    : user
      ? userName
      : agentName;
  const hasContent =
    message.content ||
    message.delivery ||
    message.images?.length ||
    message.reasoning ||
    ((showToolCalls ||
      approvals?.some((approval) =>
        message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
      )) &&
      message.tools?.length) ||
    message.authorizations?.length ||
    (delegations && delegations.length > 0) ||
    message.failure ||
    hasVisibleStopReason(message.stopReason ?? "") ||
    message.turnStat;
  if (!user && !streaming && !hasContent) return null;

  return (
    <article
      aria-label={`${label} message`}
      className="min-w-0 max-w-full break-words border-b border-border/60 pb-5 last:border-b-0"
      id={`chat-message-${message.id}`}
    >
      <h3 className="mb-2 text-xs font-semibold text-muted-foreground">{label}</h3>
      {message.reasoning && <ReasoningDisclosure streaming={streaming} text={message.reasoning} />}
      {message.images && message.images.length > 0 && (
        <div className="mb-3 grid min-w-0 grid-cols-2 gap-2">
          {message.images.map((image, index) => {
            const display = chatImageDisplay(image);
            return (
              <div className="min-w-0 overflow-hidden rounded-lg border" key={image.id ?? index}>
                {display.kind === "inline" ? (
                  onPreviewImage ? (
                    <button
                      aria-label={`Preview ${image.name}`}
                      className="block w-full"
                      onClick={() => onPreviewImage(image)}
                      type="button"
                    >
                      <img
                        alt={image.name}
                        className="max-h-48 w-full object-contain"
                        src={display.src}
                      />
                    </button>
                  ) : (
                    <img
                      alt={image.name}
                      className="max-h-48 w-full object-contain"
                      src={display.src}
                    />
                  )
                ) : display.kind === "link" ? (
                  <a
                    className="block break-all px-3 py-2 text-sm underline"
                    href={display.href}
                    rel="noreferrer"
                    target="_blank"
                  >
                    {image.name} <ExternalLink aria-hidden="true" className="inline size-3" />
                  </a>
                ) : (
                  <span className="block truncate px-3 py-2 text-sm">{image.name}</span>
                )}
              </div>
            );
          })}
        </div>
      )}
      {message.delivery ? (
        <div className="rounded-lg border bg-muted/30 p-3 text-sm" data-delivery-note>
          <p className="font-medium">
            Scheduled task {message.delivery.scheduleName} {message.delivery.kind}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            Fire {message.delivery.fireId}
            {message.delivery.stop ? ` · Stop reason: ${message.delivery.stop}` : ""}
          </p>
          {message.content && (
            <p className="mt-2 whitespace-pre-wrap break-words">{message.content}</p>
          )}
        </div>
      ) : message.content ? (
        <MarkdownMessage>{message.content}</MarkdownMessage>
      ) : streaming ? (
        <StreamingIndicator />
      ) : null}
      {(showToolCalls ||
        approvals?.some((approval) =>
          message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
        )) &&
        message.tools &&
        message.tools.length > 0 && (
          <ol className="mt-3 space-y-2">
            {message.tools.map((tool) => (
              <li className="min-w-0 rounded-lg border bg-muted/20 p-3 text-xs" key={tool.id}>
                <p className="font-mono font-semibold">Tool: {tool.name}</p>
                <p className="mt-2 text-muted-foreground">Input</p>
                <pre className="mt-1 max-w-full overflow-x-auto whitespace-pre-wrap break-words rounded bg-background p-2 font-mono">
                  {tool.args || "{}"}
                </pre>
                {tool.output !== undefined && (
                  <>
                    <p className="mt-2 text-muted-foreground">
                      {tool.isError ? "Failed result" : "Result"}
                    </p>
                    <pre className="mt-1 max-w-full overflow-x-auto whitespace-pre-wrap break-words rounded bg-background p-2 font-mono">
                      {tool.output || "No output"}
                    </pre>
                    {onPreviewTool && (
                      <button
                        className="mt-2 underline"
                        onClick={() => onPreviewTool(tool)}
                        type="button"
                      >
                        Open result
                      </button>
                    )}
                  </>
                )}
                {message.authorizations
                  ?.filter(
                    (authorization) =>
                      authorization.callId === tool.id && authorization.runId === tool.runId,
                  )
                  .map((authorization) => (
                    <AuthorizationReviewTrigger
                      authorization={authorization}
                      key={authorization.authorizationId}
                      onReview={onReviewAuthorization ?? (() => undefined)}
                    />
                  ))}
                {approvals
                  ?.filter((approval) => approvalMatchesToolCall(approval, tool))
                  .map((approval) => (
                    <ApprovalPanel
                      approval={approval}
                      disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
                      key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
                      onRespond={(verdict) => onRespondToApproval?.(approval, verdict)}
                      uncertain={approvalUncertain?.(approval)}
                    />
                  ))}
              </li>
            ))}
          </ol>
        )}
      {message.authorizations
        ?.filter(
          (authorization) =>
            !showToolCalls ||
            !message.tools?.some(
              (tool) => tool.id === authorization.callId && tool.runId === authorization.runId,
            ),
        )
        .map((authorization) => (
          <AuthorizationReviewTrigger
            authorization={authorization}
            key={authorization.authorizationId}
            onReview={onReviewAuthorization ?? (() => undefined)}
          />
        ))}
      {!user && delegations && delegations.length > 0 && onOpenActivity && (
        <DelegationCardRow activities={delegations} onOpen={onOpenActivity} />
      )}
      {!streaming && !message.failure && <StopReasonChip stopReason={message.stopReason ?? ""} />}
      {message.failure && (
        <FailedTurnCard
          detail={message.failure.detail}
          permanent={message.failure.permanent}
          summary={message.failure.message}
        />
      )}
      {!streaming && message.turnStat && (
        <p className="mt-2 text-xs tabular-nums text-muted-foreground">{message.turnStat}</p>
      )}
      {onOpenThread && message.content && !message.delivery && (
        <button
          aria-label={threadSessionId ? "Open side thread" : "Reply in side thread"}
          className="mt-2 flex items-center gap-1 text-xs text-muted-foreground underline disabled:opacity-50"
          disabled={threadDisabled}
          onClick={() => onOpenThread(message)}
          type="button"
        >
          <MessageSquareText aria-hidden="true" className="size-3" />
          {threadSessionId ? "Open thread" : "Reply in thread"}
        </button>
      )}
    </article>
  );
}

const MemoTranscriptRow = memo(
  TranscriptRow,
  (previous, next) =>
    !shouldUpdateTranscriptRow(previous, next) &&
    previous.agentName === next.agentName &&
    previous.userName === next.userName &&
    previous.threadDisabled === next.threadDisabled &&
    previous.threadSessionId === next.threadSessionId &&
    previous.onOpenActivity === next.onOpenActivity &&
    previous.onOpenThread === next.onOpenThread &&
    previous.onReviewAuthorization === next.onReviewAuthorization &&
    previous.onPreviewImage === next.onPreviewImage &&
    previous.onPreviewTool === next.onPreviewTool &&
    previous.approvalDisabled === next.approvalDisabled &&
    previous.approvalUncertain === next.approvalUncertain &&
    previous.onRespondToApproval === next.onRespondToApproval,
);

export interface ChatTranscriptProps {
  agentName?: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals?: ApprovalRequest[];
  delegationsByMessageId?: Record<string, DelegationActivity[]>;
  messages: ChatMessage[];
  onOpenActivity?: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
  onOpenThread?: (message: ChatMessage) => void;
  onReviewAuthorization?: (authorization: AuthorizationHandoff) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  showToolCalls: boolean;
  streamingMessageId?: string;
  threadDisabled?: boolean;
  threadSessionIdForMessage?: (message: ChatMessage) => string | undefined;
  userName?: string;
}

/** Flat, left-aligned conversation rows; settled rows keep their React identity. */
export function ChatTranscript({
  agentName = "Mecatl",
  approvalDisabled,
  approvalUncertain,
  approvals = EMPTY_APPROVALS,
  delegationsByMessageId,
  messages,
  onOpenActivity,
  onOpenThread,
  onReviewAuthorization,
  onPreviewImage,
  onPreviewTool,
  onRespondToApproval,
  showToolCalls,
  streamingMessageId,
  threadDisabled = false,
  threadSessionIdForMessage,
  userName = "You",
}: ChatTranscriptProps) {
  const actions = useRef({
    approvalDisabled,
    approvalUncertain,
    onOpenActivity,
    onOpenThread,
    onPreviewImage,
    onPreviewTool,
    onReviewAuthorization,
    onRespondToApproval,
  });
  actions.current = {
    approvalDisabled,
    approvalUncertain,
    onOpenActivity,
    onOpenThread,
    onPreviewImage,
    onPreviewTool,
    onReviewAuthorization,
    onRespondToApproval,
  };
  const openActivity = useCallback(
    (focus: DelegationFocus, opener: HTMLButtonElement) =>
      actions.current.onOpenActivity?.(focus, opener),
    [],
  );
  const previewImage = useCallback(
    (image: ChatImage) => actions.current.onPreviewImage?.(image),
    [],
  );
  const previewTool = useCallback(
    (tool: ToolActivity) => actions.current.onPreviewTool?.(tool),
    [],
  );
  const reviewAuthorization = useCallback(
    (authorization: AuthorizationHandoff) => actions.current.onReviewAuthorization?.(authorization),
    [],
  );
  const openThread = useCallback(
    (message: ChatMessage) => actions.current.onOpenThread?.(message),
    [],
  );
  const respondToApproval = useCallback(
    (approval: ApprovalRequest, verdict: ApprovalVerdict) =>
      actions.current.onRespondToApproval?.(approval, verdict),
    [],
  );
  const isApprovalDisabled = useCallback(
    (approval: ApprovalRequest) => actions.current.approvalDisabled?.(approval) ?? false,
    [],
  );
  const isApprovalUncertain = useCallback(
    (approval: ApprovalRequest) => actions.current.approvalUncertain?.(approval) ?? false,
    [],
  );

  const unmatched = approvals.filter(
    (approval) =>
      !messages.some((message) =>
        message.tools?.some((tool) => approvalMatchesToolCall(approval, tool)),
      ),
  );

  return (
    <div className="min-w-0 max-w-full space-y-5">
      {messages.map((message) => (
        <MemoTranscriptRow
          agentName={agentName}
          approvalDisabled={approvalDisabled ? isApprovalDisabled : undefined}
          approvalUncertain={approvalUncertain ? isApprovalUncertain : undefined}
          approvals={approvalsForMessage(message, approvals)}
          delegations={delegationsByMessageId?.[message.id]}
          key={message.id}
          message={message}
          onOpenActivity={onOpenActivity ? openActivity : undefined}
          onOpenThread={onOpenThread ? openThread : undefined}
          onReviewAuthorization={onReviewAuthorization ? reviewAuthorization : undefined}
          onPreviewImage={onPreviewImage ? previewImage : undefined}
          onPreviewTool={onPreviewTool ? previewTool : undefined}
          onRespondToApproval={onRespondToApproval ? respondToApproval : undefined}
          showToolCalls={showToolCalls}
          streaming={message.id === streamingMessageId}
          threadDisabled={threadDisabled}
          threadSessionId={threadSessionIdForMessage?.(message)}
          userName={userName}
        />
      ))}
      {unmatched.map((approval) => (
        <ApprovalPanel
          approval={approval}
          disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
          key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
          onRespond={(verdict) => respondToApproval(approval, verdict)}
          uncertain={approvalUncertain?.(approval)}
        />
      ))}
    </div>
  );
}
