// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { capabilityReasonLabel, sessionKindLabel } from "./session-kinds";

describe("sessionKindLabel", () => {
  it("names every daemon kind and humanizes an unknown one", () => {
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

describe("capabilityReasonLabel", () => {
  it("words the closed reason codes and keeps an unknown one readable", () => {
    expect(capabilityReasonLabel("inspect_only_kind")).toBe("Read-only run");
    expect(capabilityReasonLabel("awaiting_approval")).toBe("Waiting for approval");
    expect(capabilityReasonLabel("active_elsewhere")).toBe("Running in another client");
    expect(capabilityReasonLabel("transcript_unavailable")).toBe("Transcript unavailable");
    expect(capabilityReasonLabel("storage_unsupported")).toBe("Store cannot delete");
    expect(capabilityReasonLabel("brand_new-code")).toBe("Brand new code");
    expect(capabilityReasonLabel("")).toBe("");
    expect(capabilityReasonLabel(undefined)).toBe("");
  });
});
