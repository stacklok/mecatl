// @vitest-environment happy-dom
// SPDX-License-Identifier: Apache-2.0

import type { SessionDetailResponse, SessionSummaryResponse } from "@mecatl-studio/contracts";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SessionDetailsView } from "./session-details-dialog";

const session = {
  capabilities: { fork: true, inspect: true, publicChat: true },
  id: "session-a",
  kind: "main",
  modelId: "model-a",
  state: "idle",
  title: "Main chat",
  turns: 1,
  createdAt: "2026-09-24T12:00:00.000Z",
  updatedAt: "2026-09-24T12:00:00.000Z",
} as SessionSummaryResponse;

function effortRow(model: Partial<NonNullable<SessionDetailResponse["model"]>>) {
  const detail = {
    capabilities: { modelSelection: true },
    id: "session-a",
    mode: "default",
    model: {
      contextWindow: "0",
      id: "model-a",
      providerId: "p",
      providerDisplayName: "P",
      ...model,
    },
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  } as SessionDetailResponse;
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(
    <SessionDetailsView
      canInspect
      detail={detail}
      detailError={false}
      detailPending={false}
      inspectUnavailable="Unavailable here."
      notes={[]}
      onRetry={() => {}}
      session={session}
    />,
  );
  const term = [...host.querySelectorAll("dt")].find(
    (node) => node.textContent === "Reasoning effort",
  );
  return term?.nextElementSibling?.textContent;
}

describe("Session details' reasoning effort", () => {
  it("shows the request, and the level sent when the model doesn't accept it", () => {
    expect(effortRow({ reasoningEffort: "high", requestedReasoningEffort: "high" })).toBe("high");
    expect(effortRow({ reasoningEffort: "high", requestedReasoningEffort: "max" })).toBe(
      "max (sent: high)",
    );
    expect(effortRow({ reasoningEffort: "default", requestedReasoningEffort: "xhigh" })).toBe(
      "xhigh (sent: provider default)",
    );
    // A server without per-model levels reports only the applied level.
    expect(effortRow({ reasoningEffort: "medium" })).toBe("medium");
  });

  it("hides the row while both are Auto", () => {
    expect(effortRow({ reasoningEffort: "default", requestedReasoningEffort: "default" })).toBe(
      undefined,
    );
    expect(effortRow({ reasoningEffort: "default" })).toBe(undefined);
  });
});
