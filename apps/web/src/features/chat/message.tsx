// SPDX-License-Identifier: Apache-2.0

import type { ApprovalRequest, ApprovalVerdict } from "./approval-panel";
import { type ChatMessage, messageOwnsApproval } from "./chat-state";
import { MessageImages } from "./chat-transcript";
import { FailedTurnCard } from "./failed-turn-card";
import type { ChatImage } from "./local-file-preview";
import { MarkdownMessage } from "./markdown-message";
import {
  MessageActions,
  MessageAvatar,
  MessageRow,
  messageAuthorClass,
  messageBodyClass,
  messageRowClass,
} from "./message-bubble";
import { PendingAskCard, pendingAskKey } from "./pending-ask-card";
import type { PlanVerdict } from "./plan-review-card";
import { ReasoningDisclosure } from "./reasoning-disclosure";
import { hasVisibleStopReason, StopReasonChip } from "./stop-reason-chip";
import { StreamingIndicator } from "./streaming-indicator";
import type { ToolActivity } from "./tool-activity";
import { ToolCallList } from "./tool-call-list";

/** Exported so the side-thread panel can render a thread's messages with the exact same primitives as the main transcript, instead of duplicating this JSX. */
export function Message({
  agentAvatar,
  agentName,
  approvalDisabled,
  approvalUncertain,
  approvals = [],
  message,
  onOpenThread,
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
}: {
  agentAvatar: string;
  agentName: string;
  approvalDisabled?: (approval: ApprovalRequest) => boolean;
  approvalUncertain?: (approval: ApprovalRequest) => boolean;
  approvals?: ApprovalRequest[];
  message: ChatMessage;
  onOpenThread?: () => void;
  onPreviewImage?: (image: ChatImage) => void;
  onPreviewTool?: (tool: ToolActivity) => void;
  onRespondToApproval?: (approval: ApprovalRequest, verdict: ApprovalVerdict) => void;
  onRespondToPlan?: (approval: ApprovalRequest, verdict: PlanVerdict) => void;
  planUnavailableReason?: (approval: ApprovalRequest) => string | undefined;
  showToolCalls: boolean;
  streaming: boolean;
  threadDisabled: boolean;
  threadSessionId?: string;
  userAvatar: string;
  userName: string;
}) {
  const user = message.role === "user";
  // A turn that ended with no text, no visible tool activity, and nothing
  // else worth surfacing (reasoning, a failure, a stat line, a stop-reason
  // chip) renders nothing at all — never a "Thinking…" placeholder for a
  // run that has already finished (e.g. StopNoProgress with an empty final
  // turn).
  const matching = approvals.filter((approval) => messageOwnsApproval(message, approval));
  const unmatched = approvals.filter((approval) => !matching.includes(approval));
  const hasVisibleTools = (showToolCalls || matching.length > 0) && Boolean(message.tools?.length);
  const hasExtras =
    Boolean(message.images?.length) ||
    Boolean(message.reasoning) ||
    Boolean(message.failure) ||
    Boolean(message.turnStat) ||
    hasVisibleStopReason(message.stopReason ?? "") ||
    approvals.length > 0;
  if (!user && !streaming && !message.content && !hasVisibleTools && !hasExtras) {
    return null;
  }
  const name = user ? userName : agentName;
  return (
    <article className={messageRowClass}>
      <MessageRow
        actions={
          <MessageActions
            onOpenThread={onOpenThread && message.content ? onOpenThread : undefined}
            threadDisabled={threadDisabled}
            threadOpen={Boolean(threadSessionId)}
          />
        }
        author={<p className={messageAuthorClass}>{name}</p>}
        avatar={
          <MessageAvatar
            avatarUrl={user ? userAvatar : agentAvatar}
            fallback={user ? "user" : "agent"}
            name={name}
          />
        }
      >
        {message.reasoning && (
          <ReasoningDisclosure streaming={streaming} text={message.reasoning} />
        )}
        {message.images && message.images.length > 0 && (
          <MessageImages images={message.images} onPreviewImage={onPreviewImage} />
        )}
        {message.content ? (
          <div className={messageBodyClass}>
            {user ? (
              <p className="whitespace-pre-wrap">{message.content}</p>
            ) : (
              <MarkdownMessage>{message.content}</MarkdownMessage>
            )}
          </div>
        ) : message.tools?.length ? null : streaming ? (
          <StreamingIndicator />
        ) : null}
        {hasVisibleTools && message.tools && (
          <ToolCallList
            approvalDisabled={approvalDisabled}
            approvalUncertain={approvalUncertain}
            approvals={matching}
            onPreview={onPreviewTool}
            onRespondToApproval={onRespondToApproval}
            onRespondToPlan={onRespondToPlan}
            planUnavailableReason={planUnavailableReason}
            tools={message.tools}
          />
        )}
        {unmatched.map((approval) => (
          <PendingAskCard
            approval={approval}
            controls={{
              approvalDisabled,
              approvalUncertain,
              onRespondToApproval,
              onRespondToPlan,
              planUnavailableReason,
            }}
            key={pendingAskKey(approval)}
          />
        ))}
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
      </MessageRow>
    </article>
  );
}
