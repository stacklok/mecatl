// SPDX-License-Identifier: Apache-2.0

/**
 * AI session debugger (ADR 0254): a debug chat is a normal session created
 * with `debugTargetSessionId` set. The daemon requires no filesystem, binds
 * the target server-side, and never copies the target's conversation — the
 * new chat starts empty and reads the target only through its own tools.
 */

/** The consent disclosure shown before creating a debug chat. */
export const DEBUG_SESSION_CONSENT =
  "This creates a separate diagnostic chat bound to this one. The session's " +
  "stored transcript and event evidence — including anything sensitive it " +
  "contains — will be sent to the model as debugging evidence. The session " +
  "itself is read-only to the debugger and is never modified.";

/** The diagnostic objective seeded into a freshly created debug chat. */
export const DEBUG_OPENING_PROMPT =
  "Diagnose the bound target session and explain the most likely cause of its reported behavior.";
