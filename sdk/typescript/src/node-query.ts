import type { Client, CreateSessionOptions } from "./client.js";
import type { PromptInput } from "./media.js";
import type { PlanApprovalResponder } from "./plan.js";
import { type Query, queryInternal } from "./query.js";
import type { PermissionAskResponder } from "./run.js";
import { type SpawnOptions, spawn } from "./spawn.js";

/** Options for one Node.js or Bun `query()` call. @public */
export interface QueryOptions {
  /** Use an existing client instead of spawning a local daemon. The client remains caller-owned. */
  client?: Client;
  /** Automatically answer permission asks. With no responder, query denies each ask safely. */
  onPermissionAsk?: PermissionAskResponder;
  /** Required in plan mode and invoked only for PresentPlan approval asks. */
  onPlanApproval?: PlanApprovalResponder;
  /** Keep the created session after the query. SDK-spawned daemons use an in-memory store. */
  retainSession?: boolean;
  /** Fields applied when query creates its session. */
  session?: CreateSessionOptions;
  /** Abort this query and clean up every resource it created. */
  signal?: AbortSignal;
  /** Daemon options used only when query creates its own client. */
  spawn?: SpawnOptions;
}

export type { Query } from "./query.js";

/**
 * Spawns if needed, creates one session, runs one prompt, and cleans up owned resources.
 *
 * @param prompt - Text or ordered text, image, and audio parts for the run.
 * @param options - Session, responder, cancellation, retention, and daemon options.
 * @returns A single-consumption event stream for the query-created session.
 * @throws `PlanApprovalRequiredError` when plan mode has no approval responder.
 * @public
 */
export function query(prompt: PromptInput, options: QueryOptions = {}): Promise<Query> {
  return queryInternal(prompt, options, { spawn });
}
