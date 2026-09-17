import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { gatedReason } from "@/features/agent/composer-builtins";
import {
  DEBUG_ASK_ALREADY_PENDING,
  DEBUG_ASK_NONE_YET,
} from "@/features/agent/debug-ask";
import {
  type BuiltinSlashDeps,
  useBuiltinSlashCommands,
} from "./use-builtin-slash-commands";

/**
 * The `/debug-ask` dispatch: the gate follows the Labs preference, a draft
 * is refused (the panel the fake ask shows in belongs to a chat), a pending
 * ask refuses a second one (the TUI's dedupe), and the injector is the only
 * thing it calls — no daemon call lives here.
 */

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    mode: "external",
    deployment: "",
    serverCapabilities: {},
  }),
}));
vi.mock("@/lib/harness/sessions", () => ({
  fetchHarnessSessionIdentity: vi.fn(),
}));
vi.mock("@/lib/harness/server-info", () => ({
  probeHarnessServerInfo: vi.fn(),
}));

function makeDeps(overrides: Partial<BuiltinSlashDeps> = {}): BuiltinSlashDeps {
  return {
    sessionId: "s1",
    isStreaming: false,
    hasFailedStep: false,
    compactSupported: false,
    developerTools: true,
    onInjectDebugAsk: vi.fn(() => true),
    onCompact: vi.fn(),
    onRetry: vi.fn(),
    onSend: vi.fn(),
    onClearQueue: vi.fn(),
    onClearConversation: vi.fn(),
    resolvedModel: null,
    permissionMode: "default",
    ...overrides,
  };
}

describe("useBuiltinSlashCommands — /debug-ask", () => {
  it("gates the palette row on the developer-tools preference", () => {
    const { result, rerender } = renderHook(
      (deps: BuiltinSlashDeps) => useBuiltinSlashCommands(deps),
      { initialProps: makeDeps({ developerTools: false }) },
    );
    // Off is carried as ABSENT, so a daemon-only gates object is unchanged.
    expect(result.current.builtinGates).toEqual({ manualCompaction: false });
    rerender(makeDeps({ developerTools: true }));
    expect(result.current.builtinGates).toEqual({
      manualCompaction: false,
      developerTools: true,
    });
  });

  it("refuses with the Labs pointer while the tools are off, and never injects", () => {
    const deps = makeDeps({ developerTools: false });
    const { result } = renderHook(() => useBuiltinSlashCommands(deps));
    expect(result.current.handleSlashBuiltin("debug-ask")).toEqual({
      ok: false,
      warning: gatedReason("debug-ask"),
    });
    expect(deps.onInjectDebugAsk).not.toHaveBeenCalled();
  });

  it("refuses on a draft (no chat to show the fake ask in)", () => {
    const deps = makeDeps({ sessionId: null });
    const { result } = renderHook(() => useBuiltinSlashCommands(deps));
    expect(result.current.handleSlashBuiltin("debug-ask")).toEqual({
      ok: false,
      warning: DEBUG_ASK_NONE_YET,
    });
    expect(deps.onInjectDebugAsk).not.toHaveBeenCalled();
  });

  it("injects the fake ask on a chat, and reports a refused injection (an ask already pending)", () => {
    const deps = makeDeps();
    const { result } = renderHook(() => useBuiltinSlashCommands(deps));
    expect(result.current.handleSlashBuiltin("debug-ask")).toEqual({
      ok: true,
    });
    expect(deps.onInjectDebugAsk).toHaveBeenCalledTimes(1);
    // Nothing else fires — the fake ask is local.
    expect(deps.onSend).not.toHaveBeenCalled();
    expect(deps.onCompact).not.toHaveBeenCalled();

    const pending = makeDeps({ onInjectDebugAsk: vi.fn(() => false) });
    const second = renderHook(() => useBuiltinSlashCommands(pending));
    expect(second.result.current.handleSlashBuiltin("debug-ask")).toEqual({
      ok: false,
      warning: DEBUG_ASK_ALREADY_PENDING,
    });
  });

  it("runs even while a run streams — the fake ask is meant to exercise a live panel too", () => {
    const deps = makeDeps({ isStreaming: true });
    const { result } = renderHook(() => useBuiltinSlashCommands(deps));
    expect(result.current.handleSlashBuiltin("debug-ask")).toEqual({
      ok: true,
    });
  });
});
