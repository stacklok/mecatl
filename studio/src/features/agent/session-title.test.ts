import { describe, expect, it } from "vitest";
import { applySessionTitle, shouldAdoptTitle } from "./session-title";
import type { AgentSession } from "./types";

/**
 * Live title adoption: the daemon's `session.title` event is authoritative,
 * ordered by the title lifecycle revision so a replayed older title never
 * regresses a row, tolerant of an unknown revision on either side, and never
 * blanks a row on a pending-generation event.
 */

const row = (over: Partial<AgentSession> = {}): AgentSession => ({
  id: "s1",
  title: "Untitled chat",
  projectId: null,
  model: "",
  createdAt: 0,
  updatedAt: 0,
  pinned: false,
  archived: false,
  unread: false,
  messageCount: 0,
  isStreaming: false,
  inputTokens: 0,
  outputTokens: 0,
  estimatedCost: null,
  contextLength: null,
  lastPromptTokens: null,
  thresholdTokens: null,
  titleProvenance: "",
  titleRevision: null,
  ...over,
});

describe("shouldAdoptTitle", () => {
  it("adopts when either side has no revision to order by", () => {
    expect(shouldAdoptTitle(null, 3)).toBe(true);
    expect(shouldAdoptTitle(undefined, 3)).toBe(true);
    expect(shouldAdoptTitle(7, null)).toBe(true);
  });

  it("adopts only a strictly newer revision when both are known", () => {
    expect(shouldAdoptTitle(2, 3)).toBe(true);
    expect(shouldAdoptTitle(3, 3)).toBe(false);
    expect(shouldAdoptTitle(4, 3)).toBe(false);
  });
});

describe("applySessionTitle", () => {
  it("adopts a newer revision's title and provenance onto the named row only", () => {
    const sessions = [
      row({ id: "s1", titleRevision: 1 }),
      row({ id: "s2", title: "Other", titleRevision: 5 }),
    ];
    const next = applySessionTitle(sessions, "s1", {
      title: "Fix the scheduler flake",
      provenance: "first-prompt",
      revision: 2,
    });
    expect(next[0]).toMatchObject({
      title: "Fix the scheduler flake",
      titleProvenance: "first-prompt",
      titleRevision: 2,
    });
    expect(next[1]).toBe(sessions[1]);
  });

  it("ignores a lower revision (a replayed older title) and returns the same array", () => {
    const sessions = [
      row({ title: "Newer", titleProvenance: "operator", titleRevision: 4 }),
    ];
    const next = applySessionTitle(sessions, "s1", {
      title: "Older",
      provenance: "first-prompt",
      revision: 3,
    });
    expect(next).toBe(sessions);
  });

  it("treats an equal revision as a re-delivery, not a change", () => {
    const sessions = [row({ title: "Same", titleRevision: 4 })];
    expect(
      applySessionTitle(sessions, "s1", {
        title: "Same",
        provenance: "first-prompt",
        revision: 4,
      }),
    ).toBe(sessions);
  });

  it("adopts when the row's revision is unknown (legacy row / older daemon)", () => {
    const sessions = [row({ titleRevision: null })];
    const next = applySessionTitle(sessions, "s1", {
      title: "From the daemon",
      provenance: "first-prompt",
      revision: 1,
    });
    expect(next[0].title).toBe("From the daemon");
    expect(next[0].titleRevision).toBe(1);
  });

  it("adopts an event with no revision and keeps the row's own revision", () => {
    const sessions = [row({ titleRevision: 2 })];
    const next = applySessionTitle(sessions, "s1", {
      title: "Renamed elsewhere",
      provenance: "operator",
      revision: null,
    });
    expect(next[0]).toMatchObject({
      title: "Renamed elsewhere",
      titleProvenance: "operator",
      titleRevision: 2,
    });
  });

  it("advances the revision on a pending-generation event without blanking the row", () => {
    const sessions = [
      row({
        title: "Keep me",
        titleProvenance: "first-prompt",
        titleRevision: 1,
      }),
    ];
    const next = applySessionTitle(sessions, "s1", {
      title: "",
      provenance: "",
      revision: 2,
    });
    expect(next[0]).toMatchObject({
      title: "Keep me",
      titleProvenance: "first-prompt",
      titleRevision: 2,
    });
  });

  it("leaves an unknown session id untouched", () => {
    const sessions = [row()];
    expect(
      applySessionTitle(sessions, "nope", {
        title: "x",
        provenance: "operator",
        revision: 1,
      }),
    ).toBe(sessions);
  });
});
