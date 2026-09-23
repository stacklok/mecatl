// SPDX-License-Identifier: Apache-2.0

import type { GetStorageHealthResponse } from "@mecatl-studio/contracts/generated";
import { describe, expect, it } from "vitest";
import { describeSessionMix, formatBytes, storageHealthStatus } from "./storage-settings";

function health(overrides: Partial<GetStorageHealthResponse> = {}): GetStorageHealthResponse {
  return {
    activeJob: false,
    available: true,
    childCount: "0",
    corruptCount: "0",
    currentBytes: "0",
    lastFailure: false,
    mainCount: "0",
    reclaimableBytes: "0",
    scheduledCount: "0",
    sessionCount: "0",
    supported: true,
    unknownCount: "0",
    ...overrides,
  };
}

describe("storageHealthStatus", () => {
  it("reports needs-attention when storage is unavailable", () => {
    expect(storageHealthStatus(health({ available: false }))).toEqual({
      detail: "The agent cannot read its saved chats right now.",
      label: "Needs attention",
    });
  });

  it("reports needs-attention with a count when items are corrupt", () => {
    expect(storageHealthStatus(health({ corruptCount: "1" }))).toEqual({
      detail: "1 saved item can no longer be opened.",
      label: "Needs attention",
    });
    expect(storageHealthStatus(health({ corruptCount: "3" }))).toEqual({
      detail: "3 saved items can no longer be opened.",
      label: "Needs attention",
    });
  });

  it("reports needs-attention when a recent clean-up failed", () => {
    expect(storageHealthStatus(health({ lastFailure: true }))).toEqual({
      detail: "A recent automatic clean-up did not finish.",
      label: "Needs attention",
    });
  });

  it("is healthy with no detail when nothing is wrong and no job is active", () => {
    expect(storageHealthStatus(health())).toEqual({ detail: "", label: "Healthy" });
  });

  it("is healthy but notes an active job", () => {
    expect(storageHealthStatus(health({ activeJob: true }))).toEqual({
      detail: "Storage maintenance is running.",
      label: "Healthy",
    });
  });
});

describe("describeSessionMix", () => {
  it("joins only the non-zero counts, in a fixed order", () => {
    expect(
      describeSessionMix(health({ childCount: "4", mainCount: "5", scheduledCount: "2" })),
    ).toBe("5 chats · 4 agent runs · 2 scheduled runs");
  });

  it("singularizes a count of one", () => {
    expect(describeSessionMix(health({ mainCount: "1", unknownCount: "1" }))).toBe(
      "1 chat · 1 other",
    );
  });

  it("is empty when every count is zero", () => {
    expect(describeSessionMix(health())).toBe("");
  });
});

describe("formatBytes", () => {
  it("keeps sub-1024 byte counts exact", () => {
    expect(formatBytes("0")).toBe("0 B");
    expect(formatBytes("512")).toBe("512 B");
  });

  it("scales through KB/MB/GB", () => {
    expect(formatBytes("2048")).toBe("2.0 KB");
    expect(formatBytes(String(3.5 * 1024 * 1024))).toBe("3.5 MB");
    expect(formatBytes(String(1024 * 1024 * 1024))).toBe("1.0 GB");
  });

  it("drops the decimal once the scaled value reaches double digits", () => {
    expect(formatBytes(String(12 * 1024))).toBe("12 KB");
  });
});
