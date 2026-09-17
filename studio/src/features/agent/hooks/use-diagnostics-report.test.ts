import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  studioServerEndpoint,
  useDiagnosticsReport,
} from "./use-diagnostics-report";

/**
 * The ONE composition point for the `/diagnostics` report: it folds the
 * runtime status (mode, deployment, effective posture), Studio's own build
 * and platform tokens, the identity probe (or a probe already in hand) and
 * the caller's session context into the documented line set — so the
 * composer's built-in and the About card's "Send to a new chat" agree.
 */

const runtime = vi.hoisted(() => ({
  mode: "external" as "managed" | "external",
  deployment: "staging-eu",
  serverCapabilities: {} as Record<string, unknown>,
}));
vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const probe = vi.hoisted(() => vi.fn());
vi.mock("@/lib/harness/server-info", () => ({
  probeHarnessServerInfo: probe,
}));

beforeEach(() => {
  runtime.mode = "external";
  runtime.deployment = "staging-eu";
  runtime.serverCapabilities = { posture: "trusted" };
  probe.mockReset();
  probe.mockResolvedValue({
    info: {
      buildId: "fixture",
      serverImplementation: "fixture-daemon",
      providerEndpoint: "https://openrouter.ai/api/v1",
    },
    lookup: "ok",
  });
  vi.stubEnv("NEXT_PUBLIC_STUDIO_BUILD", "0.1.0+abc1234");
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("useDiagnosticsReport", () => {
  it("composes the full report from runtime status, the probe and the session", async () => {
    const { result } = renderHook(() => useDiagnosticsReport());
    const report = await result.current.compose({
      resolvedModel: {
        providerId: "openrouter",
        modelId: "openai/gpt-5",
        reasoningEffort: "high",
      },
      permissionMode: "acceptEdits",
    });
    // The probe projects the SESSION's provider endpoint.
    expect(probe).toHaveBeenCalledWith("openrouter");
    const lines = report.split("\n");
    expect(lines[0]).toBe("Mecatl diagnostics (current client state only):");
    expect(lines).toContain("client build: 0.1.0+abc1234");
    expect(lines).toContain("server mode: external");
    expect(lines).toContain("server build: fixture");
    expect(lines).toContain("server implementation: fixture-daemon");
    expect(lines).toContain(`server endpoint: ${studioServerEndpoint()}`);
    expect(lines).toContain(
      "LLM provider endpoint: https://openrouter.ai/api/v1",
    );
    expect(lines).toContain("server lookup: ok");
    expect(lines).toContain("deployment: staging-eu");
    expect(lines).toContain("posture: trusted");
    expect(lines).toContain("active provider: openrouter");
    expect(lines).toContain("active model: openai/gpt-5");
    expect(lines).toContain("reasoning effort: high");
    expect(lines).toContain("permission mode: acceptEdits");
  });

  it("composes with no session at all, reading unavailable, and skips the probe when one is in hand", async () => {
    runtime.mode = "managed";
    runtime.serverCapabilities = {};
    const { result } = renderHook(() => useDiagnosticsReport());
    const report = await result.current.compose({
      server: { info: null, lookup: "not-supported" },
    });
    expect(probe).not.toHaveBeenCalled();
    expect(report).toContain("server mode: managed");
    expect(report).toContain("server build: unavailable");
    expect(report).toContain("server lookup: not-supported");
    expect(report).toContain("posture: unavailable");
    expect(report).toContain("active provider: unavailable");
    expect(report).toContain("active model: unavailable");
    expect(report).toContain("reasoning effort: auto");
    expect(report).toContain("permission mode: unavailable");
  });

  it("probes without a provider when the session has none and names the failure class", async () => {
    probe.mockResolvedValue({ info: null, lookup: "unreachable" });
    const { result } = renderHook(() => useDiagnosticsReport());
    const report = await result.current.compose({ permissionMode: "default" });
    expect(probe).toHaveBeenCalledWith(undefined);
    expect(report).toContain("server lookup: unreachable");
    expect(report).toContain("LLM provider endpoint: unavailable");
  });

  it("keeps compose referentially stable across runtime polls", () => {
    const { result, rerender } = renderHook(() => useDiagnosticsReport());
    const first = result.current.compose;
    runtime.deployment = "prod";
    rerender();
    expect(result.current.compose).toBe(first);
  });
});

describe("studioServerEndpoint", () => {
  it("is the same-origin proxy, never a remote daemon address", () => {
    expect(studioServerEndpoint()).toBe(
      new URL("/api/mecatl", window.location.origin).toString(),
    );
    expect(studioServerEndpoint().endsWith("/api/mecatl")).toBe(true);
  });
});
