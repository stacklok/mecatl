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

interface HiddenSnapshot {
  atMs: number;
  facts: AwayFacts;
  sawApprovalResolved: boolean;
  sawResult: boolean;
}

export class AwayNoticeTracker {
  #generation = 0;
  #hidden?: HiddenSnapshot;
  #returning?: HiddenSnapshot;

  hide(facts: AwayFacts, atMs: number): void {
    if (this.#hidden) return;
    this.restartHide(facts, atMs);
  }

  /** A second hide starts a fresh interval and invalidates an unfinished return refresh. */
  restartHide(facts: AwayFacts, atMs: number): void {
    this.#generation += 1;
    this.#returning = undefined;
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
    this.#generation += 1;
    this.#hidden = undefined;
    this.#returning = undefined;
  }

  beginReturn(): number {
    this.#generation += 1;
    // Freeze the evidence at visibility return, before asynchronous refetches.
    this.#returning = this.#hidden;
    this.#hidden = undefined;
    return this.#generation;
  }

  isCurrentReturn(generation: number): boolean {
    return generation === this.#generation;
  }

  resume(facts: RefreshedAwayFacts, atMs: number, generation: number): ReturnNotice | undefined {
    if (generation !== this.#generation) return undefined;
    const hidden = this.#returning;
    this.#returning = undefined;
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
