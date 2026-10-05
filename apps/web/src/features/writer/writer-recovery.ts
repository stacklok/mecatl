// SPDX-License-Identifier: Apache-2.0

import { accountStorageKey } from "../../lib/account-storage";
import type { WriterSnapshot } from "./writer-core";

const key = "studio.writer.recovery";
type Saved = { version: 1; account: string; generation: string; snapshot: WriterSnapshot };
export type RecoveryRead =
  | { kind: "empty" | "unavailable" | "corrupt" }
  | { kind: "saved"; saved: Saved };

function hasOnlyKeys(value: unknown, keys: string[]): boolean {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.keys(value).every((key) => keys.includes(key))
  );
}

function validEntry(value: unknown): boolean {
  return (
    hasOnlyKeys(value, ["role", "text"]) &&
    ["user", "assistant"].includes((value as { role: string }).role) &&
    typeof (value as { text?: unknown }).text === "string" &&
    (value as { text: string }).text.length > 0 &&
    (value as { text: string }).text.length <= 2_000
  );
}

function validSnapshot(snapshot: unknown): snapshot is WriterSnapshot {
  if (!hasOnlyKeys(snapshot, ["document", "brief", "observations", "generalDiscussion"]))
    return false;
  const s = snapshot as Partial<WriterSnapshot>;
  return (
    hasOnlyKeys(s.document, ["content", "revision"]) &&
    typeof s.document?.content === "string" &&
    s.document.content.length <= 100_000 &&
    Number.isSafeInteger(s.document.revision) &&
    s.document.revision >= 0 &&
    typeof s.brief === "string" &&
    s.brief.length <= 2_000 &&
    Array.isArray(s.generalDiscussion) &&
    s.generalDiscussion.length <= 100 &&
    s.generalDiscussion.every(validEntry) &&
    Array.isArray(s.observations) &&
    s.observations.length <= 100 &&
    s.observations.every(
      (o) =>
        hasOnlyKeys(o, [
          "id",
          "revision",
          "text",
          "quote",
          "quotes",
          "status",
          "decision",
          "discussion",
          "timestamp",
        ]) &&
        typeof o.id === "string" &&
        o.id.length <= 100 &&
        Number.isSafeInteger(o.revision) &&
        o.revision >= 0 &&
        o.revision <= (s.document?.revision ?? 0) &&
        typeof o.text === "string" &&
        o.text.length > 0 &&
        o.text.length <= 1_000 &&
        ["open", "addressed", "not-relevant"].includes(o.status) &&
        Number.isSafeInteger(o.timestamp) &&
        o.timestamp >= 0 &&
        (o.decision === undefined ||
          (typeof o.decision === "string" && o.decision.length <= 500)) &&
        (o.quote === undefined || (typeof o.quote === "string" && o.quote.length <= 500)) &&
        (o.quotes === undefined ||
          (Array.isArray(o.quotes) &&
            o.quotes.length <= 3 &&
            o.quotes.every((q) => typeof q === "string" && q.length > 0 && q.length <= 500))) &&
        Array.isArray(o.discussion) &&
        o.discussion.length <= 100 &&
        o.discussion.every(validEntry),
    )
  );
}

export class WriterRecovery {
  private account?: string;
  private generation?: string;
  private corruptRaw?: string;
  constructor(
    private store: Pick<Storage, "getItem" | "setItem" | "removeItem">,
    private locks: Pick<LockManager, "request"> | undefined,
  ) {
    try {
      const account = store.getItem(accountStorageKey);
      if (account && /^[A-Za-z0-9_-]+$/.test(account)) this.account = account;
    } catch {
      /* storage may be unavailable */
    }
  }

  get available() {
    return !!this.account && !!this.locks;
  }
  get storageKey() {
    return key;
  }

  read(): RecoveryRead {
    if (!this.available) return { kind: "unavailable" };
    let raw: string | null;
    try {
      if (this.store.getItem(accountStorageKey) !== this.account) return { kind: "unavailable" };
      raw = this.store.getItem(key);
    } catch {
      return { kind: "unavailable" };
    }
    if (raw === null) return { kind: "empty" };
    this.corruptRaw = raw;
    try {
      const saved: unknown = JSON.parse(raw);
      if (
        typeof saved === "object" &&
        saved &&
        "account" in saved &&
        saved.account !== this.account
      )
        this.corruptRaw = undefined;
      if (raw.length > 1_000_000) return { kind: "corrupt" };
      if (
        typeof saved !== "object" ||
        !saved ||
        !hasOnlyKeys(saved, ["version", "account", "generation", "snapshot"])
      )
        return { kind: "corrupt" };
      const item = saved as Partial<Saved>;
      if (
        item.version !== 1 ||
        item.account !== this.account ||
        typeof item.generation !== "string" ||
        !/^[0-9a-f-]{36}$/.test(item.generation) ||
        !validSnapshot(item.snapshot)
      )
        return { kind: "corrupt" };
      this.generation = item.generation;
      this.corruptRaw = undefined;
      return { kind: "saved", saved: item as Saved };
    } catch {
      return { kind: "corrupt" };
    }
  }

  async save(snapshot: WriterSnapshot): Promise<boolean> {
    if (!this.account || !this.locks || !validSnapshot(snapshot)) return false;
    const expected = this.generation;
    try {
      return await this.locks.request(key, { mode: "exclusive" }, () => {
        if (this.store.getItem(accountStorageKey) !== this.account) return false;
        const raw = this.store.getItem(key);
        if (raw !== null) {
          const parsed: unknown = JSON.parse(raw);
          if (typeof parsed !== "object" || !parsed || (parsed as Saved).generation !== expected)
            return false;
        } else if (expected) return false;
        const generation = crypto.randomUUID();
        const value = JSON.stringify({ version: 1, account: this.account, generation, snapshot });
        if (value.length > 1_000_000) return false;
        this.store.setItem(key, value);
        if (this.store.getItem(key) !== value) return false;
        this.generation = generation;
        this.corruptRaw = undefined;
        return true;
      });
    } catch {
      return false;
    }
  }

  async forget(): Promise<boolean> {
    if (!this.account || !this.locks) return false;
    try {
      return await this.locks.request(key, { mode: "exclusive" }, () => {
        if (this.store.getItem(accountStorageKey) !== this.account) return false;
        const raw = this.store.getItem(key);
        if (raw === null) {
          this.generation = undefined;
          return true;
        }
        if (this.corruptRaw !== undefined && raw !== this.corruptRaw) return false;
        if (raw !== this.corruptRaw) {
          const parsed: unknown = JSON.parse(raw);
          if (
            typeof parsed !== "object" ||
            !parsed ||
            !this.generation ||
            (parsed as Saved).generation !== this.generation ||
            (parsed as Saved).account !== this.account
          )
            return false;
        }
        this.store.removeItem(key);
        if (this.store.getItem(key) !== null) return false;
        this.generation = undefined;
        this.corruptRaw = undefined;
        return true;
      });
    } catch {
      return false;
    }
  }
}
