// SPDX-License-Identifier: Apache-2.0

import type { RuntimeResponse } from "@mecatl-studio/contracts";

export type AwayPhase = "working" | "awaiting" | "idle" | "unknown";

export interface AwayFacts {
  connection: RuntimeResponse["connection"];
  phase: AwayPhase;
  sessionId: string;
}

export interface RefreshedAwayFacts extends AwayFacts {
  refreshed: boolean;
}

export interface ReturnNotice {
  durationMs: number;
  text: string;
}

export class AwayNoticeTracker {
  #hidden?: {
    atMs: number;
    facts: AwayFacts;
    sawApprovalResolved: boolean;
    sawResult: boolean;
  };

  hide(facts: AwayFacts, atMs: number): void {
    if (this.#hidden) return;
    this.#hidden = {
      atMs,
      facts,
      sawApprovalResolved: false,
      sawResult: false,
    };
  }

  record(sessionId: string, kind: "result" | "approval-resolved"): void {
    if (this.#hidden?.facts.sessionId !== sessionId) return;
    if (kind === "result") this.#hidden.sawResult = true;
    else this.#hidden.sawApprovalResolved = true;
  }

  clear(): void {
    this.#hidden = undefined;
  }

  resume(facts: RefreshedAwayFacts, atMs: number): ReturnNotice | undefined {
    const hidden = this.#hidden;
    this.#hidden = undefined;
    if (
      !hidden ||
      !facts.refreshed ||
      facts.sessionId !== hidden.facts.sessionId ||
      atMs - hidden.atMs < 20_000
    ) {
      return undefined;
    }

    let text: string | undefined;
    if (facts.connection === "offline") text = "Mecatl is offline.";
    else if (facts.connection !== "online") text = "Studio is reconnecting to Mecatl.";
    else if (facts.phase === "working") text = "Mecatl is still working in this chat.";
    else if (facts.phase === "awaiting") text = "This chat is waiting for approval.";
    else if (facts.phase === "idle" && hidden.sawResult)
      text = "The run finished while you were away.";
    else if (facts.phase === "idle" && hidden.sawApprovalResolved)
      text = "An approval was resolved while you were away.";
    else if (facts.phase === "idle") text = "This chat is idle.";

    return text ? { durationMs: 8_000, text } : undefined;
  }
}
