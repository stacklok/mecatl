import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useAgentChat } from "./use-agent-chat";

/**
 * The hook's `steerSupported` gate — the one value chat-workspace uses to
 * hand (or withhold) `onSteerMessage` — honours the daemon's live
 * `capabilities.steer` over the static `http_steer` feature row: after the
 * operator turns steering off (`--no-steer` / `steer: false`) a rebuilt
 * daemon still lists the route, and reading the row first would keep every
 * steer affordance lit while each mid-run send round-tripped to a
 * `too_late` refusal. The row stays the fallback for an older daemon that
 * reports no `steer` key at all.
 */

const runtime = vi.hoisted(() => ({
  connected: true,
  features: new Set<string>(),
  serverCapabilities: {} as Record<string, unknown>,
  refresh: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  fetchSessionTranscriptMessages: vi.fn(async () => []),
}));

vi.mock("../runtime-status", () => ({ useRuntimeStatus: () => runtime }));

vi.mock("../composer-capabilities", () => ({
  refreshSlashCommands: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/watch", () => ({
  watchSessionEvents: vi.fn(),
}));

beforeEach(() => {
  runtime.features = new Set<string>();
  runtime.serverCapabilities = {};
});

describe("useAgentChat steerSupported", () => {
  it("is off when the daemon reports steer: false although http_steer is listed", () => {
    runtime.serverCapabilities = { steer: false };
    runtime.features = new Set(["http_steer"]);
    const { result } = renderHook(() => useAgentChat(null));
    expect(result.current.steerSupported).toBe(false);
  });

  it("is on when the daemon reports steer: true", () => {
    runtime.serverCapabilities = { steer: true };
    const { result } = renderHook(() => useAgentChat(null));
    expect(result.current.steerSupported).toBe(true);
  });

  it("falls back to the http_steer row for an older daemon without the key", () => {
    runtime.features = new Set(["http_steer"]);
    const { result } = renderHook(() => useAgentChat(null));
    expect(result.current.steerSupported).toBe(true);
  });

  it("is off when neither signal is present", () => {
    const { result } = renderHook(() => useAgentChat(null));
    expect(result.current.steerSupported).toBe(false);
  });
});
