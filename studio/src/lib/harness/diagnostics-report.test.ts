import { describe, expect, it } from "vitest";
import {
  buildDiagnosticsReport,
  debuggerInitialPrompt,
  diagnosticEndpoint,
  diagnosticToken,
  wrapAsDebuggerRuntimeContext,
} from "./diagnostics-report";

/**
 * The `/diagnostics` report reaches the model, so its sanitizers are pinned
 * exactly: a token is bounded and character-restricted, an endpoint keeps
 * only scheme + host + clean path, and the report carries the documented
 * line set in order — nothing raw ever leaks through.
 */

describe("diagnosticToken", () => {
  it("passes a short opaque identifier through, trimmed", () => {
    expect(diagnosticToken("  abc-1.2_3/x+y ")).toBe("abc-1.2_3/x+y");
    expect(diagnosticToken("openai/gpt-5")).toBe("openai/gpt-5");
  });

  it("rejects an empty, oversized, spaced or control-bearing value", () => {
    expect(diagnosticToken("")).toBe("unavailable");
    expect(diagnosticToken(undefined)).toBe("unavailable");
    expect(diagnosticToken("a".repeat(129))).toBe("unavailable");
    expect(diagnosticToken("a".repeat(128))).toBe("a".repeat(128));
    expect(diagnosticToken("two words")).toBe("unavailable");
    expect(diagnosticToken("tab\there")).toBe("unavailable");
    expect(diagnosticToken("quote'd")).toBe("unavailable");
    expect(diagnosticToken("<script>")).toBe("unavailable");
  });
});

describe("diagnosticEndpoint", () => {
  it("keeps only scheme, host and a clean path", () => {
    expect(
      diagnosticEndpoint(
        "HTTPS://user:secret@api.example.com:8443/v1/../v2//chat/?key=abc#frag",
      ),
    ).toBe("https://api.example.com:8443/v2/chat");
    expect(diagnosticEndpoint("https://api.example.com")).toBe(
      "https://api.example.com/",
    );
  });

  it("reads unavailable for empty, hostless, oversized or control-bearing input", () => {
    expect(diagnosticEndpoint("")).toBe("unavailable");
    expect(diagnosticEndpoint(null)).toBe("unavailable");
    expect(diagnosticEndpoint("not a url")).toBe("unavailable");
    expect(diagnosticEndpoint("mailto:someone@example.com")).toBe(
      "unavailable",
    );
    expect(
      diagnosticEndpoint(`https://example.com/a${String.fromCharCode(0)}b`),
    ).toBe("unavailable");
    expect(diagnosticEndpoint(`https://example.com/${"a".repeat(2048)}`)).toBe(
      "unavailable",
    );
  });
});

describe("buildDiagnosticsReport", () => {
  it("renders exactly the documented line set, sanitized", () => {
    const report = buildDiagnosticsReport({
      platform: "macOS/Chrome-128",
      clientBuild: "0.1.0+abc1234",
      mode: "external",
      serverInfo: {
        buildId: "abc123",
        serverImplementation: "mecated",
        providerEndpoint: "https://key:secret@openrouter.ai/api/v1?x=1",
      },
      serverEndpoint: "http://localhost:3000/api/mecatl",
      lookup: "ok",
      deployment: "staging eu",
      posture: "trusted",
      resolvedModel: { providerId: "openrouter", modelId: "openai/gpt-5" },
      reasoningEffort: "medium",
      permissionMode: "acceptEdits",
    });
    expect(report.split("\n")).toEqual([
      "Mecatl diagnostics (current client state only):",
      "platform: macOS/Chrome-128",
      "client build: 0.1.0+abc1234",
      "server mode: external",
      "server build: abc123",
      "server implementation: mecated",
      "server endpoint: http://localhost:3000/api/mecatl",
      "LLM provider endpoint: https://openrouter.ai/api/v1",
      "server lookup: ok",
      "deployment: unavailable",
      "posture: trusted",
      "active provider: openrouter",
      "active model: openai/gpt-5",
      "reasoning effort: medium",
      "permission mode: acceptEdits",
    ]);
  });

  it("reads unavailable across the board against an older daemon with no session", () => {
    const report = buildDiagnosticsReport({
      platform: "browser",
      clientBuild: "",
      mode: "managed",
      serverInfo: null,
      serverEndpoint: "",
      lookup: "not-supported",
      deployment: "",
      posture: "",
      resolvedModel: null,
      reasoningEffort: "",
      permissionMode: "default",
    });
    expect(report).toContain("client build: unavailable");
    expect(report).toContain("server mode: managed");
    expect(report).toContain("server build: unavailable");
    expect(report).toContain("server endpoint: unavailable");
    expect(report).toContain("LLM provider endpoint: unavailable");
    expect(report).toContain("server lookup: not-supported");
    expect(report).toContain("posture: unavailable");
    expect(report).toContain("active provider: unavailable");
    expect(report).toContain("active model: unavailable");
    // "" is the operator-default sentinel, the picker's "auto" tier.
    expect(report).toContain("reasoning effort: auto");
    expect(report).toContain("permission mode: default");
  });

  it("names the lookup failure class and never prints an unknown token raw", () => {
    const base = {
      platform: "browser",
      clientBuild: "dev",
      mode: "external" as const,
      serverInfo: null,
      serverEndpoint: "",
      deployment: "",
      posture: "",
      resolvedModel: null,
      reasoningEffort: "",
      permissionMode: "default",
    };
    for (const lookup of [
      "not-supported",
      "unreachable",
      "invalid-response",
    ] as const) {
      expect(buildDiagnosticsReport({ ...base, lookup })).toContain(
        `server lookup: ${lookup}`,
      );
    }
    expect(
      buildDiagnosticsReport({
        ...base,
        lookup: "boom <script>" as unknown as "ok",
      }),
    ).toContain("server lookup: invalid-response");
  });
});

describe("debugger runtime context", () => {
  it("fences the report exactly as the TUI does", () => {
    expect(wrapAsDebuggerRuntimeContext("a: 1\nb: 2")).toBe(
      "<<<CURRENT_DEBUGGER_RUNTIME_CONTEXT (not target evidence)\na: 1\nb: 2\nCURRENT_DEBUGGER_RUNTIME_CONTEXT>>>",
    );
  });

  it("builds the TUI's opening prompt around the fenced report", () => {
    const prompt = debuggerInitialPrompt("a: 1", "The run stalls");
    expect(
      prompt.startsWith("Objective\nThe run stalls\n\nRequired workflow\n"),
    ).toBe(true);
    expect(prompt).toContain("1. Call InspectSession with view=status first.");
    expect(prompt).toContain("Expected report\n- Observed facts");
    expect(prompt.endsWith(wrapAsDebuggerRuntimeContext("a: 1"))).toBe(true);
  });
});
