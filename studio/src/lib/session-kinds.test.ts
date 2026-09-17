import { describe, expect, it } from "vitest";
import type { SessionRelationshipInfo } from "@/lib/protocol/sessions";
import {
  capabilityReasonLabel,
  describeRelationship,
  inspectRowTitle,
  relationshipTerms,
  runInspectHref,
  sessionKindLabel,
  sessionTabFor,
} from "./session-kinds";

/**
 * The inventory taxonomy the sidebar tabs and the search index share: every
 * daemon kind lands on exactly one tab, Drafts keys on the activity state
 * (and only when the feature is offered), the closed capability-reason codes
 * read as plain words, and a related session is described by the links the
 * daemon validated for its kind.
 */

const rel = (
  partial: Partial<SessionRelationshipInfo> = {},
): SessionRelationshipInfo => ({
  parentSessionId: "",
  callId: "",
  branchIndex: null,
  scheduleName: "",
  originSessionId: "",
  teamId: "",
  memberName: "",
  ...partial,
});

describe("sessionTabFor", () => {
  it("places every daemon kind on one tab", () => {
    expect(sessionTabFor({ kind: "main", isChat: true })).toBe("chats");
    expect(sessionTabFor({ kind: "debug", isChat: true })).toBe("chats");
    expect(sessionTabFor({ kind: "subagent", isChat: false })).toBe("runs");
    expect(sessionTabFor({ kind: "parallel_branch", isChat: false })).toBe(
      "runs",
    );
    expect(sessionTabFor({ kind: "team_member", isChat: false })).toBe("runs");
    expect(sessionTabFor({ kind: "scheduled", isChat: false })).toBe(
      "scheduled",
    );
    expect(sessionTabFor({ kind: "unknown", isChat: false })).toBe("other");
    // An older daemon's kind-less inspect-only row is still listed.
    expect(sessionTabFor({ kind: "", isChat: false })).toBe("other");
    expect(sessionTabFor({ isChat: false })).toBe("other");
  });

  it("an omitted isChat reads as a chat (only the decoder sets it false)", () => {
    expect(sessionTabFor({ kind: "main" })).toBe("chats");
    expect(sessionTabFor({})).toBe("chats");
  });

  it("puts a chat on Drafts only when its activity state is draft AND the tab is offered", () => {
    const draft = { kind: "main", isChat: true, activityState: "draft" };
    expect(sessionTabFor(draft)).toBe("drafts");
    expect(sessionTabFor(draft, true)).toBe("drafts");
    // No `session_activity_inventory` feature → no Drafts tab → a chat.
    expect(sessionTabFor(draft, false)).toBe("chats");
    expect(
      sessionTabFor({ kind: "main", isChat: true, activityState: "active" }),
    ).toBe("chats");
    expect(
      sessionTabFor({ kind: "main", isChat: true, activityState: "" }),
    ).toBe("chats");
    // The activity state never moves a non-chat.
    expect(
      sessionTabFor({
        kind: "subagent",
        isChat: false,
        activityState: "draft",
      }),
    ).toBe("runs");
  });
});

describe("capabilityReasonLabel", () => {
  it("spells the daemon's closed reason codes in plain words", () => {
    expect(capabilityReasonLabel("inspect_only_kind")).toBe("Read-only run");
    expect(capabilityReasonLabel("awaiting_approval")).toBe(
      "Waiting for approval",
    );
    expect(capabilityReasonLabel("active_elsewhere")).toBe(
      "Running in another client",
    );
    expect(capabilityReasonLabel("transcript_unavailable")).toBe(
      "Transcript unavailable",
    );
    expect(capabilityReasonLabel("storage_unsupported")).toBe(
      "Store cannot delete",
    );
  });

  it("humanizes an unknown code instead of swallowing it, and reads empty as empty", () => {
    expect(capabilityReasonLabel("some_new_reason")).toBe("Some new reason");
    expect(capabilityReasonLabel("")).toBe("");
    expect(capabilityReasonLabel(undefined)).toBe("");
  });
});

describe("sessionKindLabel", () => {
  it("names every kind, with an honest floor for a missing one", () => {
    expect(sessionKindLabel("main")).toBe("Chat");
    expect(sessionKindLabel("subagent")).toBe("Subagent");
    expect(sessionKindLabel("parallel_branch")).toBe("Parallel branch");
    expect(sessionKindLabel("team_member")).toBe("Team member");
    expect(sessionKindLabel("scheduled")).toBe("Scheduled run");
    expect(sessionKindLabel("debug")).toBe("Debug session");
    expect(sessionKindLabel("")).toBe("Unknown kind");
    expect(sessionKindLabel(undefined)).toBe("Unknown kind");
    expect(sessionKindLabel("future_kind")).toBe("Future kind");
  });
});

describe("describeRelationship", () => {
  it("describes each family from the links the daemon validated", () => {
    expect(
      describeRelationship(rel({ parentSessionId: "main-1", callId: "c7" })),
    ).toBe("Subagent of main-1 · call c7");
    expect(describeRelationship(rel({ parentSessionId: "main-1" }))).toBe(
      "Subagent of main-1",
    );
    expect(
      describeRelationship(rel({ parentSessionId: "main-1", branchIndex: 2 })),
    ).toBe("Branch #2 of main-1");
    expect(
      describeRelationship(rel({ teamId: "t1", memberName: "reviewer" })),
    ).toBe("Team t1 · member reviewer");
    expect(describeRelationship(rel({ scheduleName: "nightly" }))).toBe(
      "Fire of schedule nightly",
    );
    expect(describeRelationship(rel({ originSessionId: "src-1" }))).toBe(
      "Forked from src-1",
    );
  });

  it("is empty for a plain chat", () => {
    expect(describeRelationship(rel())).toBe("");
    expect(describeRelationship(undefined)).toBe("");
  });
});

describe("row helpers", () => {
  it("titles a run by its title, else its relationship, else a plain floor", () => {
    expect(
      inspectRowTitle({
        title: "Scan tests",
        relationship: rel({ parentSessionId: "p" }),
      }),
    ).toBe("Scan tests");
    expect(
      inspectRowTitle({
        title: "",
        relationship: rel({ parentSessionId: "p" }),
      }),
    ).toBe("Subagent of p");
    expect(inspectRowTitle({ title: "" })).toBe("Untitled run");
  });

  it("deep-links a run to its parent chat with ?inspect=, or the draft route without a parent", () => {
    expect(
      runInspectHref({
        id: "subagent-1",
        relationship: rel({ parentSessionId: "main 1" }),
      }),
    ).toBe("/workspace/chat/main%201?inspect=subagent-1");
    expect(
      runInspectHref({
        id: "fire/1",
        relationship: rel({ scheduleName: "n" }),
      }),
    ).toBe("/workspace/chat?inspect=fire%2F1");
  });

  it("collects the relationship's ids and names as search terms", () => {
    expect(
      relationshipTerms(
        rel({
          parentSessionId: "p",
          callId: "c",
          scheduleName: "s",
          originSessionId: "o",
          teamId: "t",
          memberName: "m",
        }),
      ),
    ).toEqual(["p", "c", "s", "o", "t", "m"]);
    expect(relationshipTerms(rel())).toEqual([]);
    expect(relationshipTerms(undefined)).toEqual([]);
  });
});
