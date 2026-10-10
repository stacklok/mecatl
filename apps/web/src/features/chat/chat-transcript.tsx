// SPDX-License-Identifier: Apache-2.0

import { ExternalLink } from "lucide-react";
import { memo, useCallback, useRef } from "react";
import { ApprovalPanel, type ApprovalRequest, type ApprovalVerdict } from "./approval-panel";
import { type AuthorizationHandoff, AuthorizationReviewTrigger } from "./authorization-review";
import { type ChatMessage, messageOwnsApproval, unmatchedApprovals } from "./chat-state";
import {
  type DelegationActivity,
  DelegationCardRow,
  type DelegationFocus,
} from "./delegation-card";
import { DeliveryNoteCard } from "./delivery-note-card";
import { FailedTurnCard } from "./failed-turn-card";
import { type ChatImage, chatImageDisplay } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import {
  MessageActions,
  MessageAvatar,
  MessageRow,
  messageAuthorClass,
  messageBodyClass,
  messageRowClass,
} from "./message-bubble";
import { PlanReviewCard, type PlanVerdict } from "./plan-review-card";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import type { ToolActivity } from "./tool-activity";
import { ToolCallList } from "./tool-call-list";

const EMPTY_APPROVALS: ApprovalRequest[] = [];

function approvalsForMessage(
  message: ChatMessage,
  approvals: ApprovalRequest[],
): ApprovalRequest[] {
  const matched = approvals.filter((approval) => messageOwnsApproval(message, approval));
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

/** Shared visibility rule for rendered rows and their minimap targets. */
export function isVisibleTranscriptMessage(
  message: ChatMessage,
  showToolCalls: boolean,
  streaming: boolean,
  delegations: DelegationActivity[] | undefined,
  approvals: ApprovalRequest[],
): boolean {
  if (message.role === "user" || streaming) return true;
  return Boolean(
    message.content ||
      message.delivery ||
      message.images?.length ||
      message.reasoning ||
      ((showToolCalls || approvals.some((approval) => messageOwnsApproval(message, approval))) &&
        message.tools?.length) ||
      message.authorizations?.length ||
      delegations?.length ||
      message.failure ||
      hasVisibleStopReason(message.stopReason ?? "") ||
      message.turnStat,
  );
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
  agentAvatar: string;
  agentName: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  onCopyMessage?: (message: ChatMessage) => void;
  onOpenActivity?: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
  onOpenThread?: (message: ChatMessage) => void;
  onReviewAuthorization?: (authorization: AuthorizationHandoff) => void;
  onRelinkThread?: (message: ChatMessage) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
  threadDisabled: boolean;
  legacyThreadSessionId?: string;
  threadSessionId?: string;
  userAvatar: string;
  userName: string;
}

/** A turn's images: inline data previews, a link for a remote one, else the name. */
export function MessageImages({
  images,
  onPreviewImage,
}: {
  images: ChatImage[];
  onPreviewImage?: (image: ChatImage) => void;
}) {
  return (
    <div className="mt-1 mb-2 grid min-w-0 grid-cols-2 gap-2">
      {images.map((image, index) => {
        const display = chatImageDisplay(image);
        return (
          <div
            className="min-w-0 overflow-hidden rounded-lg border bg-background"
            key={image.id ?? index}
          >
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
                    className="max-h-48 w-full object-cover"
                    src={display.src}
                  />
                </button>
              ) : (
                <img alt={image.name} className="max-h-48 w-full object-cover" src={display.src} />
              )
            ) : display.kind === "link" ? (
              <a
                className="flex items-center gap-2 px-3 py-2 text-sm text-foreground underline underline-offset-2 hover:text-brand-ink"
                href={display.href}
                rel="noreferrer"
                target="_blank"
              >
                <ExternalLink aria-hidden="true" className="size-4 shrink-0" />
                <span className="min-w-0 truncate">{image.name}</span>
              </a>
            ) : (
              <span className="block truncate px-3 py-2 text-sm text-muted-foreground">
                {image.name}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}

function TranscriptRow({
  agentAvatar,
  agentName,
  approvalDisabled,
  approvalUncertain,
  approvals,
  delegations,
  message,
  legacyThreadSessionId,
  onCopyMessage,
  onOpenActivity,
  onOpenThread,
  onReviewAuthorization,
  onRelinkThread,
  onPreviewImage,
  onPreviewTool,
  onRespondToApproval,
  onRespondToPlan,
  planUnavailableReason,
  showToolCalls,
  streaming,
  threadDisabled,
  threadSessionId,
  userAvatar,
  userName,
}: TranscriptRowProps) {
  const user = message.role === "user";
  const label = message.delivery
    ? `Scheduled task ${message.delivery.scheduleName}`
    : user
      ? userName
      : agentName;
  if (!isVisibleTranscriptMessage(message, showToolCalls, streaming, delegations, approvals ?? []))
    return null;

  // A delivery note is the schedule's card, never a speaker's turn: its text
  // is model-authored and renders as plain text inside the card.
  if (message.delivery) {
    return (
      <article
        aria-label={`${label} message`}
        className="min-w-0 max-w-full py-1"
        id={`chat-message-${message.id}`}
      >
        <h3 className="sr-only">{label}</h3>
        <DeliveryNoteCard body={message.content} delivery={message.delivery} />
      </article>
    );
  }

  const toolsShown =
    (showToolCalls || approvals?.some((approval) => messageOwnsApproval(message, approval))) &&
    Boolean(message.tools?.length);

  return (
    <article
      aria-label={`${label} message`}
      className={messageRowClass}
      id={`chat-message-${message.id}`}
    >
      <MessageRow
        actions={
          <MessageActions
            onCopy={onCopyMessage && message.content ? () => onCopyMessage(message) : undefined}
            onOpenThread={onOpenThread && message.content ? () => onOpenThread(message) : undefined}
            threadDisabled={threadDisabled}
            threadOpen={Boolean(threadSessionId)}
          />
        }
        avatar={
          <MessageAvatar
            avatarUrl={user ? userAvatar : agentAvatar}
            fallback={user ? "user" : "agent"}
            name={label}
          />
        }
      >
        <h3 className={messageAuthorClass}>{label}</h3>
        {message.reasoning && (
          <ReasoningDisclosure streaming={streaming} text={message.reasoning} />
        )}
        {message.images && message.images.length > 0 && (
          <MessageImages images={message.images} onPreviewImage={onPreviewImage} />
        )}
        {message.content ? (
          <div className={messageBodyClass}>
            <MarkdownMessage>{message.content}</MarkdownMessage>
          </div>
        ) : streaming ? (
          <StreamingIndicator />
        ) : null}
        {toolsShown && message.tools && (
          <ToolCallList
            approvalDisabled={approvalDisabled}
            approvalUncertain={approvalUncertain}
            approvals={approvals}
            authorizations={showToolCalls ? message.authorizations : undefined}
            onPreview={onPreviewTool}
            onRespondToApproval={onRespondToApproval}
            onRespondToPlan={onRespondToPlan}
            onReviewAuthorization={onReviewAuthorization}
            planUnavailableReason={planUnavailableReason}
            tools={message.tools}
          />
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
          <p
            className="mt-1 text-[11px] tabular-nums text-muted-foreground/70"
            title="This turn's tokens sent ↑ and received ↓, model time, and the share of input served from the prompt cache."
          >
            {message.turnStat}
          </p>
        )}
        {onRelinkThread && legacyThreadSessionId && (
          <button
            aria-label={`Relink older side thread ${legacyThreadSessionId} to this message`}
            className="mt-1.5 rounded px-1 text-xs text-muted-foreground underline hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand"
            disabled={threadDisabled}
            onClick={() => onRelinkThread(message)}
            type="button"
          >
            Relink older thread
          </button>
        )}
      </MessageRow>
    </article>
  );
}

const MemoTranscriptRow = memo(
  TranscriptRow,
  (previous, next) =>
    !shouldUpdateTranscriptRow(previous, next) &&
    previous.agentAvatar === next.agentAvatar &&
    previous.agentName === next.agentName &&
    previous.userAvatar === next.userAvatar &&
    previous.onCopyMessage === next.onCopyMessage &&
    previous.userName === next.userName &&
    previous.threadDisabled === next.threadDisabled &&
    previous.threadSessionId === next.threadSessionId &&
    previous.onOpenActivity === next.onOpenActivity &&
    previous.legacyThreadSessionId === next.legacyThreadSessionId &&
    previous.onOpenThread === next.onOpenThread &&
    previous.onReviewAuthorization === next.onReviewAuthorization &&
    previous.onRelinkThread === next.onRelinkThread &&
    previous.onPreviewImage === next.onPreviewImage &&
    previous.onPreviewTool === next.onPreviewTool &&
    previous.approvalDisabled === next.approvalDisabled &&
    previous.approvalUncertain === next.approvalUncertain &&
    previous.onRespondToApproval === next.onRespondToApproval,
);

export interface ChatTranscriptProps {
  agentAvatar?: string;
  agentName?: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals?: ApprovalRequest[];
  delegationsByMessageId?: Record<string, DelegationActivity[]>;
  messages: ChatMessage[];
  /** Offers a turn's Copy action; without it, rows have no copy button. */
  onCopyMessage?: (message: ChatMessage) => void;
  onOpenActivity?: (focus: DelegationFocus, opener: HTMLButtonElement) => void;
  onOpenThread?: (message: ChatMessage) => void;
  onReviewAuthorization?: (authorization: AuthorizationHandoff) => void;
  onRelinkThread?: (message: ChatMessage) => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
  showToolCalls: boolean;
  streamingMessageId?: string;
  threadDisabled?: boolean;
  legacyThreadSessionIdForMessage?: (message: ChatMessage) => string | undefined;
  threadSessionIdForMessage?: (message: ChatMessage) => string | undefined;
  userAvatar?: string;
  userName?: string;
}

/** Flat, left-aligned conversation rows; settled rows keep their React identity. */
export function ChatTranscript({
  agentAvatar = "",
  agentName = "Mecatl",
  approvalDisabled,
  approvalUncertain,
  approvals = EMPTY_APPROVALS,
  delegationsByMessageId,
  messages,
  onCopyMessage,
  onOpenActivity,
  onOpenThread,
  onReviewAuthorization,
  onRelinkThread,
  onPreviewImage,
  onPreviewTool,
  onRespondToApproval,
  onRespondToPlan,
  planUnavailableReason,
  showToolCalls,
  streamingMessageId,
  threadDisabled = false,
  legacyThreadSessionIdForMessage,
  threadSessionIdForMessage,
  userAvatar = "",
  userName = "You",
}: ChatTranscriptProps) {
  const actions = useRef({
    approvalDisabled,
    approvalUncertain,
    onCopyMessage,
    onOpenActivity,
    onOpenThread,
    onRelinkThread,
    onPreviewImage,
    onPreviewTool,
    onReviewAuthorization,
    onRespondToApproval,
    onRespondToPlan,
    planUnavailableReason,
  });
  actions.current = {
    approvalDisabled,
    approvalUncertain,
    onCopyMessage,
    onOpenActivity,
    onOpenThread,
    onRelinkThread,
    onPreviewImage,
    onPreviewTool,
    onReviewAuthorization,
    onRespondToApproval,
    onRespondToPlan,
    planUnavailableReason,
  };
  const copyMessage = useCallback(
    (message: ChatMessage) => actions.current.onCopyMessage?.(message),
    [],
  );
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
  const relinkThread = useCallback(
    (message: ChatMessage) => actions.current.onRelinkThread?.(message),
    [],
  );
  const respondToApproval = useCallback(
    (approval: ApprovalRequest, verdict: ApprovalVerdict) =>
      actions.current.onRespondToApproval?.(approval, verdict),
    [],
  );
  const respondToPlan = useCallback(
    (approval: ApprovalRequest, verdict: PlanVerdict) =>
      actions.current.onRespondToPlan?.(approval, verdict),
    [],
  );
  const planUnavailable = useCallback(
    (approval: ApprovalRequest) => actions.current.planUnavailableReason?.(approval),
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

  const unmatched = unmatchedApprovals(approvals, messages);

  return (
    <div className="min-w-0 max-w-full space-y-2">
      {messages.map((message) => (
        <MemoTranscriptRow
          agentAvatar={agentAvatar}
          agentName={agentName}
          approvalDisabled={approvalDisabled ? isApprovalDisabled : undefined}
          approvalUncertain={approvalUncertain ? isApprovalUncertain : undefined}
          approvals={approvalsForMessage(message, approvals)}
          delegations={delegationsByMessageId?.[message.id]}
          key={message.id}
          legacyThreadSessionId={legacyThreadSessionIdForMessage?.(message)}
          message={message}
          onCopyMessage={onCopyMessage ? copyMessage : undefined}
          onOpenActivity={onOpenActivity ? openActivity : undefined}
          onOpenThread={onOpenThread ? openThread : undefined}
          onReviewAuthorization={onReviewAuthorization ? reviewAuthorization : undefined}
          onRelinkThread={onRelinkThread ? relinkThread : undefined}
          onPreviewImage={onPreviewImage ? previewImage : undefined}
          onPreviewTool={onPreviewTool ? previewTool : undefined}
          onRespondToApproval={onRespondToApproval ? respondToApproval : undefined}
          onRespondToPlan={onRespondToPlan ? respondToPlan : undefined}
          planUnavailableReason={planUnavailableReason ? planUnavailable : undefined}
          showToolCalls={showToolCalls}
          streaming={message.id === streamingMessageId}
          threadDisabled={threadDisabled}
          threadSessionId={threadSessionIdForMessage?.(message)}
          userAvatar={userAvatar}
          userName={userName}
        />
      ))}
      {unmatched.map((approval) =>
        approval.tool === "PresentPlan" ? (
          <PlanReviewCard
            approval={approval}
            disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
            key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
            onRespond={(verdict) => respondToPlan(approval, verdict)}
            uncertain={approvalUncertain?.(approval)}
            unavailableReason={planUnavailableReason?.(approval)}
          />
        ) : (
          <ApprovalPanel
            approval={approval}
            disabled={!approval.controlTarget || (approvalDisabled?.(approval) ?? false)}
            key={`${approval.controlTarget?.runId ?? ""}:${approval.askId}`}
            onRespond={(verdict) => respondToApproval(approval, verdict)}
            uncertain={approvalUncertain?.(approval)}
          />
        ),
      )}
    </div>
  );
}
