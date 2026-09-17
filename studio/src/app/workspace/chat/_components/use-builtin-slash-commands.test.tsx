import {
  act,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceEnrollmentView } from "@/features/agent/hooks/use-workspace-enrollment";
import {
  type BuiltinSlashDeps,
  builtinGatesFor,
  COMPACT_NONE_YET,
  COMPACT_WHILE_STREAMING,
  DIAGNOSTICS_WHILE_STREAMING,
  ENROLLMENT_ALREADY_PENDING,
  ENROLLMENT_CONNECTED,
  ENROLLMENT_NONE_PENDING,
  ENROLLMENT_NONE_YET,
  LEARNING_SETTINGS_ROUTE,
  MCP_PANEL_NONE_YET,
  MCP_PICKER_UNAVAILABLE,
  MEMORY_SETTINGS_ROUTE,
  MODEL_PICKER_UNAVAILABLE,
  POSTURE_PREFIX,
  RETRY_NOTHING_FAILED,
  RETRY_WHILE_STREAMING,
  SCHEDULES_ROUTE,
  SESSION_NONE_YET,
  SKILLS_ROUTE,
  TITLE_NONE_YET,
  TITLE_NOT_ALLOWED,
  useBuiltinSlashCommands,
} from "./use-builtin-slash-commands";
import { NOT_IDLE_HINT } from "./workspace-enrollment-notice";

/**
 * Pins the chat workspace's built-in dispatch: each of the six commands, its
 * idle/streaming/no-session refusals (a refusal keeps the composer text and
 * carries the plain warning), `/clear` handing a live chat — streaming or
 * not — to the Clear conversation handoff (use-clear-conversation.ts owns
 * the daemon call), `/retry` acting ONLY on a held failure (never
 * re-sending a successful turn), and `/diagnostics` being the one built-in
 * that sends — the sanitized report, as a prompt.
 */

const router = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock("next/navigation", () => ({ useRouter: () => router }));

const runtime = vi.hoisted(() => ({
  mode: "external" as "managed" | "external",
  deployment: "staging-eu",
}));
vi.mock("@/features/agent/runtime-status", () => ({
  useOptionalRuntimeStatus: () => null,
  useRuntimeStatus: () => ({
    mode: runtime.mode,
    deployment: runtime.deployment,
    serverCapabilities: {},
  }),
}));

const harness = vi.hoisted(() => ({
  identity: vi.fn(),
  serverInfo: vi.fn(),
  soul: vi.fn(),
}));
vi.mock("@/lib/harness/sessions", () => ({
  fetchHarnessSessionIdentity: harness.identity,
}));
vi.mock("@/lib/harness/server-info", () => ({
  probeHarnessServerInfo: harness.serverInfo,
}));
vi.mock("@/lib/harness/soul", () => ({
  fetchHarnessSoul: harness.soul,
}));

// The three outside openers the picker/panel built-ins call: the composer's
// MCP pickers, its model picker, and the chat view's MCP panel. Each answers
// true (a surface registered) unless a test flips it.
const openers = vi.hoisted(() => ({
  mcpPicker: vi.fn(() => true),
  modelPicker: vi.fn(() => true),
  mcpPanel: vi.fn(),
}));
vi.mock("../../_components/mcp-composer-insert", () => ({
  requestOpenMcpPicker: openers.mcpPicker,
}));
vi.mock("../../_components/model-picker-opener", () => ({
  requestOpenModelPicker: openers.modelPicker,
}));
vi.mock("./mcp-panel", () => ({
  requestOpenMcpPanel: openers.mcpPanel,
}));

function makeDeps(overrides: Partial<BuiltinSlashDeps> = {}): BuiltinSlashDeps {
  return {
    sessionId: "s1",
    isStreaming: false,
    hasFailedStep: false,
    compactSupported: true,
    onCompact: vi.fn(),
    onRetry: vi.fn(),
    onSend: vi.fn(),
    onClearQueue: vi.fn(),
    onClearConversation: vi.fn(),
    resolvedModel: { providerId: "openrouter", modelId: "openai/gpt-5" },
    permissionMode: "default",
    ...overrides,
  };
}

beforeEach(() => {
  harness.identity.mockReset();
  harness.identity.mockResolvedValue({
    id: "s1",
    title: "Chat",
    titleProvenance: "",
    state: "idle",
    kind: "main",
    mode: "default",
    resolvedModel: null,
    placement: null,
    createdAtUnix: 0,
    turns: 0,
    toolCalls: 0,
    limits: null,
    relationship: null,
  });
  harness.serverInfo.mockReset();
  harness.serverInfo.mockResolvedValue({
    info: {
      buildId: "fixture",
      serverImplementation: "fixture-daemon",
      providerEndpoint: "https://openrouter.ai/api/v1",
    },
    lookup: "ok",
  });
});

describe("useBuiltinSlashCommands", () => {
  it("gates /compact on manual compaction", () => {
    const { result, rerender } = renderHook(
      (deps: BuiltinSlashDeps) => useBuiltinSlashCommands(deps),
      { initialProps: makeDeps({ compactSupported: false }) },
    );
    expect(result.current.builtinGates).toEqual({ manualCompaction: false });
    rerender(makeDeps({ compactSupported: true }));
    expect(result.current.builtinGates).toEqual({ manualCompaction: true });
  });

  it("/help opens the shortcuts reference", () => {
    const { result } = renderHook(() => useBuiltinSlashCommands(makeDeps()));
    expect(result.current.handleSlashBuiltin("help")).toEqual({ ok: true });
    expect(router.push).toHaveBeenCalledWith("/workspace/shortcuts");
  });

  describe("/clear", () => {
    it("on a draft drops the held queue and clears the composer", () => {
      const deps = makeDeps({ sessionId: null });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("clear")).toEqual({ ok: true });
      expect(deps.onClearQueue).toHaveBeenCalledTimes(1);
      expect(deps.onClearConversation).not.toHaveBeenCalled();
    });

    it("hands a live chat to the Clear conversation handoff", () => {
      const deps = makeDeps();
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("clear")).toEqual({ ok: true });
      expect(deps.onClearConversation).toHaveBeenCalledTimes(1);
      // The handoff owns the queue drop (after the daemon answered), not
      // the built-in.
      expect(deps.onClearQueue).not.toHaveBeenCalled();
    });

    it("is never refused while a run streams — the daemon cancels the run", () => {
      const deps = makeDeps({ isStreaming: true });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("clear")).toEqual({ ok: true });
      expect(deps.onClearConversation).toHaveBeenCalledTimes(1);
    });
  });

  describe("/retry", () => {
    it("refuses while a run is active", () => {
      const deps = makeDeps({ isStreaming: true, hasFailedStep: true });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("retry")).toEqual({
        ok: false,
        warning: RETRY_WHILE_STREAMING,
      });
      expect(deps.onRetry).not.toHaveBeenCalled();
    });

    it("never re-sends a turn that did not fail", () => {
      const deps = makeDeps({ hasFailedStep: false });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("retry")).toEqual({
        ok: false,
        warning: RETRY_NOTHING_FAILED,
      });
      expect(deps.onRetry).not.toHaveBeenCalled();
    });

    it("re-drives the failed step when one is held", () => {
      const deps = makeDeps({ hasFailedStep: true });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("retry")).toEqual({ ok: true });
      expect(deps.onRetry).toHaveBeenCalledTimes(1);
    });
  });

  describe("/compact", () => {
    it("refuses when the daemon lacks manual compaction", () => {
      const deps = makeDeps({ compactSupported: false });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("compact")).toEqual({
        ok: false,
        warning: "/compact is not available on this daemon",
      });
    });

    it("refuses while streaming and on a draft, otherwise compacts", () => {
      const streaming = makeDeps({ isStreaming: true });
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(streaming),
        ).result.current.handleSlashBuiltin("compact"),
      ).toEqual({ ok: false, warning: COMPACT_WHILE_STREAMING });
      const draft = makeDeps({ sessionId: null });
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(draft),
        ).result.current.handleSlashBuiltin("compact"),
      ).toEqual({ ok: false, warning: COMPACT_NONE_YET });
      const idle = makeDeps();
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(idle),
        ).result.current.handleSlashBuiltin("compact"),
      ).toEqual({ ok: true });
      expect(idle.onCompact).toHaveBeenCalledTimes(1);
    });
  });

  describe("/diagnostics", () => {
    it("refuses while a run is active", () => {
      const deps = makeDeps({ isStreaming: true });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("diagnostics")).toEqual({
        ok: false,
        warning: DIAGNOSTICS_WHILE_STREAMING,
      });
      expect(deps.onSend).not.toHaveBeenCalled();
    });

    it("sends the sanitized report as a prompt", async () => {
      const deps = makeDeps({ permissionMode: "acceptEdits" });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("diagnostics")).toEqual({
        ok: true,
      });
      await waitFor(() => expect(deps.onSend).toHaveBeenCalledTimes(1));
      expect(harness.serverInfo).toHaveBeenCalledWith("openrouter");
      const report = (deps.onSend as ReturnType<typeof vi.fn>).mock
        .calls[0]?.[0] as string;
      const lines = report.split("\n");
      expect(lines[0]).toBe("Mecatl diagnostics (current client state only):");
      expect(lines).toContain("server mode: external");
      expect(lines).toContain("server build: fixture");
      expect(lines).toContain("server implementation: fixture-daemon");
      expect(lines).toContain(
        "LLM provider endpoint: https://openrouter.ai/api/v1",
      );
      expect(lines).toContain("deployment: staging-eu");
      expect(lines).toContain("active provider: openrouter");
      expect(lines).toContain("active model: openai/gpt-5");
      expect(lines).toContain("permission mode: acceptEdits");
    });

    it("still sends when the identity probe fails, naming the lookup class", async () => {
      harness.serverInfo.mockResolvedValue({
        info: null,
        lookup: "unreachable",
      });
      const deps = makeDeps({ resolvedModel: null });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      result.current.handleSlashBuiltin("diagnostics");
      await waitFor(() => expect(deps.onSend).toHaveBeenCalledTimes(1));
      const report = (deps.onSend as ReturnType<typeof vi.fn>).mock
        .calls[0]?.[0] as string;
      expect(report).toContain("server build: unavailable");
      expect(report).toContain("server lookup: unreachable");
      expect(report).toContain("active model: unavailable");
    });
  });

  describe("/session", () => {
    it("refuses on a draft", () => {
      const deps = makeDeps({ sessionId: null });
      const { result } = renderHook(() => useBuiltinSlashCommands(deps));
      expect(result.current.handleSlashBuiltin("session")).toEqual({
        ok: false,
        warning: SESSION_NONE_YET,
      });
    });

    it("opens the details dialog for the active session", async () => {
      const { result } = renderHook(() => useBuiltinSlashCommands(makeDeps()));
      const view = render(result.current.sessionDetailsDialog);
      expect(screen.queryByRole("dialog")).toBeNull();
      act(() => {
        expect(result.current.handleSlashBuiltin("session")).toEqual({
          ok: true,
        });
      });
      view.rerender(result.current.sessionDetailsDialog);
      expect(await screen.findByRole("dialog")).toBeInTheDocument();
      expect(screen.getByText("Session details")).toBeInTheDocument();
      await waitFor(() =>
        expect(harness.identity).toHaveBeenCalledWith(
          "s1",
          expect.any(AbortSignal),
        ),
      );
    });

    it("opens the same dialog from the menu item / ⌘I, carrying the row extras", async () => {
      const { result } = renderHook(() =>
        useBuiltinSlashCommands(
          makeDeps({
            sessionDetails: { canCopyId: false, copyIdReason: "inspect_only" },
          }),
        ),
      );
      const view = render(result.current.sessionDetailsDialog);
      act(() => result.current.openSessionDetails());
      view.rerender(result.current.sessionDetailsDialog);
      expect(await screen.findByRole("dialog")).toBeInTheDocument();
      await waitFor(() =>
        expect(harness.identity).toHaveBeenCalledWith(
          "s1",
          expect.any(AbortSignal),
        ),
      );
      // The inventory row's copy_id denial reaches the dialog's Copy button.
      expect(
        screen.getByRole("button", { name: "Copy session ID" }),
      ).toBeDisabled();
    });

    it("says there is no session yet when opened on a draft", () => {
      const { result } = renderHook(() =>
        useBuiltinSlashCommands(makeDeps({ sessionId: null })),
      );
      render(result.current.sessionDetailsDialog);
      act(() => result.current.openSessionDetails());
      expect(toast.info).toHaveBeenCalledWith(SESSION_NONE_YET);
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(harness.identity).not.toHaveBeenCalled();
    });
  });
});

/**
 * The capability-gated and page-backed built-ins (the TUI's `/mcp /agents
 * /skills /soul /usermodel /reflections /reflect /dream /models /effort
 * /schedule /tools-connect /tools-cancel /posture`, plus `/title` and
 * `/learning`): each reads the SAME gate the palette applied (a hidden row
 * typed anyway is refused with the daemon warning, never sent), routes to
 * the Studio surface that owns it, or asks a registered opener — and says
 * so when none answered.
 */
describe("useBuiltinSlashCommands — capability-gated built-ins", () => {
  const CAPS: Record<string, unknown> = {
    mcp: true,
    agents: true,
    skills: true,
    soul: true,
    user_model: true,
    learning_proposals: true,
    reflection: true,
    manual_dream: { project_memory: {} },
    scheduling: true,
    workspace_enrollment: true,
  };

  function enrollment(
    overrides: Partial<WorkspaceEnrollmentView> = {},
  ): WorkspaceEnrollmentView {
    return {
      supported: true,
      phase: "not_connected",
      enrollmentId: "",
      requiredServices: 1,
      outcome: "",
      error: null,
      busy: false,
      idle: true,
      dismissed: false,
      window: "closed" as WorkspaceEnrollmentView["window"],
      connect: vi.fn(),
      retry: vi.fn(),
      cancel: vi.fn(),
      dismiss: vi.fn(),
      reopenWindow: vi.fn(),
      ...overrides,
    };
  }

  const gatedDeps = (overrides: Partial<BuiltinSlashDeps> = {}) =>
    makeDeps({ serverCapabilities: CAPS, posture: "trusted", ...overrides });

  beforeEach(() => {
    router.push.mockReset();
    openers.mcpPicker.mockReset().mockReturnValue(true);
    openers.modelPicker.mockReset().mockReturnValue(true);
    openers.mcpPanel.mockReset();
    (toast.info as ReturnType<typeof vi.fn>).mockReset?.();
  });

  it("carries the capability document and posture into the palette gates", () => {
    expect(
      builtinGatesFor({
        compactSupported: true,
        serverCapabilities: { mcp: true },
        posture: "auto",
      }),
    ).toEqual({
      manualCompaction: true,
      capabilities: { mcp: true, posture: "auto" },
    });
    // No document and no posture: no `capabilities` key at all (closed).
    expect(builtinGatesFor({ compactSupported: false })).toEqual({
      manualCompaction: false,
    });
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    expect(result.current.builtinGates.capabilities).toMatchObject({
      skills: true,
      posture: "trusted",
    });
  });

  it("refuses a built-in the daemon's capabilities hide, with the same warning the palette gate implies", () => {
    const { result } = renderHook(() =>
      useBuiltinSlashCommands(makeDeps({ serverCapabilities: {} })),
    );
    for (const name of ["mcp", "skills", "agents", "schedule"] as const) {
      expect(result.current.handleSlashBuiltin(name)).toEqual({
        ok: false,
        warning: `/${name} is not available on this daemon`,
      });
    }
    expect(router.push).not.toHaveBeenCalled();
    expect(openers.mcpPanel).not.toHaveBeenCalled();
  });

  it("/title opens the Rename prompt; refused on a draft or an unrenamable row", () => {
    const onRename = vi.fn();
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(makeDeps({ onRename })),
      ).result.current.handleSlashBuiltin("title"),
    ).toEqual({ ok: true });
    expect(onRename).toHaveBeenCalledTimes(1);
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(makeDeps({ sessionId: null, onRename })),
      ).result.current.handleSlashBuiltin("title"),
    ).toEqual({ ok: false, warning: TITLE_NONE_YET });
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(makeDeps()),
      ).result.current.handleSlashBuiltin("title"),
    ).toEqual({ ok: false, warning: TITLE_NOT_ALLOWED });
  });

  it("/mcp asks the chat view for the MCP panel; a draft has none to open", () => {
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(gatedDeps()),
      ).result.current.handleSlashBuiltin("mcp"),
    ).toEqual({ ok: true });
    expect(openers.mcpPanel).toHaveBeenCalledTimes(1);
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(gatedDeps({ sessionId: null })),
      ).result.current.handleSlashBuiltin("mcp"),
    ).toEqual({ ok: false, warning: MCP_PANEL_NONE_YET });
    // The connector-status-only daemon still gets the panel.
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(
          makeDeps({ serverCapabilities: { mcp_connector_status: true } }),
        ),
      ).result.current.handleSlashBuiltin("mcp"),
    ).toEqual({ ok: true });
  });

  it("/prompts and /resources open the composer's MCP pickers, and say so when no composer answers", () => {
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    expect(result.current.handleSlashBuiltin("prompts")).toEqual({ ok: true });
    expect(openers.mcpPicker).toHaveBeenLastCalledWith("prompt");
    expect(result.current.handleSlashBuiltin("resources")).toEqual({
      ok: true,
    });
    expect(openers.mcpPicker).toHaveBeenLastCalledWith("resource");
    openers.mcpPicker.mockReturnValue(false);
    expect(result.current.handleSlashBuiltin("prompts")).toEqual({
      ok: false,
      warning: MCP_PICKER_UNAVAILABLE,
    });
    // A connector-status-only daemon never receives a prompt/resource RPC.
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(
          makeDeps({ serverCapabilities: { mcp_connector_status: true } }),
        ),
      ).result.current.handleSlashBuiltin("prompts"),
    ).toEqual({
      ok: false,
      warning: "/prompts is not available on this daemon",
    });
  });

  it("/agents hands the composer an `@` to type into the emptied field", () => {
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    expect(result.current.handleSlashBuiltin("agents")).toEqual({
      ok: true,
      insertText: "@",
    });
  });

  it("routes the page-backed built-ins to the surface that owns each", () => {
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    const expectRoute = (
      name: Parameters<typeof result.current.handleSlashBuiltin>[0],
      route: string,
    ) => {
      router.push.mockClear();
      expect(result.current.handleSlashBuiltin(name)).toEqual({ ok: true });
      expect(router.push).toHaveBeenCalledWith(route);
    };
    expectRoute("skills", SKILLS_ROUTE);
    expectRoute("usermodel", MEMORY_SETTINGS_ROUTE);
    expectRoute("dream", MEMORY_SETTINGS_ROUTE);
    expectRoute("reflections", LEARNING_SETTINGS_ROUTE);
    expectRoute("reflect", LEARNING_SETTINGS_ROUTE);
    expectRoute("schedule", SCHEDULES_ROUTE);
    // `/learning` is an operator setting: always offered, no capability.
    router.push.mockClear();
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(makeDeps()),
      ).result.current.handleSlashBuiltin("learning"),
    ).toEqual({ ok: true });
    expect(router.push).toHaveBeenCalledWith(LEARNING_SETTINGS_ROUTE);
  });

  it("/soul opens the soul dialog, which reads the daemon's resolved soul", async () => {
    harness.soul.mockResolvedValue({
      present: true,
      content: "Be terse.",
      sizeBytes: 9,
      sha256: "abc",
      provenance: "user",
      trusted: true,
      drifted: false,
    });
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    const view = render(result.current.soulDialog);
    expect(screen.queryByRole("dialog")).toBeNull();
    act(() => {
      expect(result.current.handleSlashBuiltin("soul")).toEqual({ ok: true });
    });
    view.rerender(result.current.soulDialog);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(await screen.findByTestId("soul-content")).toHaveTextContent(
      "Be terse.",
    );
    expect(harness.soul).toHaveBeenCalledWith(expect.any(AbortSignal));
  });

  it("/models and /effort open the composer's model picker; refused when none is registered", () => {
    const { result } = renderHook(() => useBuiltinSlashCommands(gatedDeps()));
    expect(result.current.handleSlashBuiltin("models")).toEqual({ ok: true });
    expect(result.current.handleSlashBuiltin("effort")).toEqual({ ok: true });
    expect(openers.modelPicker).toHaveBeenCalledTimes(2);
    openers.modelPicker.mockReturnValue(false);
    expect(result.current.handleSlashBuiltin("models")).toEqual({
      ok: false,
      warning: MODEL_PICKER_UNAVAILABLE,
    });
    // The daemon that turned model selection off hides the picker AND the rows.
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(
          makeDeps({ serverCapabilities: { model_selection: false } }),
        ),
      ).result.current.handleSlashBuiltin("effort"),
    ).toEqual({
      ok: false,
      warning: "/effort is not available on this daemon",
    });
  });

  describe("/tools-connect and /tools-cancel", () => {
    it("connect starts an enrollment (retry after a failure), refusing when there is nothing to do", () => {
      const fresh = enrollment();
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: fresh })),
        ).result.current.handleSlashBuiltin("tools-connect"),
      ).toEqual({ ok: true });
      expect(fresh.connect).toHaveBeenCalledTimes(1);
      expect(fresh.retry).not.toHaveBeenCalled();

      const failed = enrollment({ phase: "failed", outcome: "denied" });
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: failed })),
        ).result.current.handleSlashBuiltin("tools-connect"),
      ).toEqual({ ok: true });
      expect(failed.retry).toHaveBeenCalledTimes(1);
      expect(failed.connect).not.toHaveBeenCalled();

      const cases: [Partial<WorkspaceEnrollmentView>, string][] = [
        [{ phase: "connected" }, ENROLLMENT_CONNECTED],
        [{ phase: "pending", enrollmentId: "e1" }, ENROLLMENT_ALREADY_PENDING],
        [{ idle: false }, NOT_IDLE_HINT],
        [{ supported: false }, ENROLLMENT_NONE_YET],
      ];
      for (const [state, warning] of cases) {
        const e = enrollment(state);
        expect(
          renderHook(() =>
            useBuiltinSlashCommands(gatedDeps({ enrollment: e })),
          ).result.current.handleSlashBuiltin("tools-connect"),
        ).toEqual({ ok: false, warning });
        expect(e.connect).not.toHaveBeenCalled();
      }
      // No enrollment view at all (a draft): the same "no session" answer.
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: null })),
        ).result.current.handleSlashBuiltin("tools-connect"),
      ).toEqual({ ok: false, warning: ENROLLMENT_NONE_YET });
    });

    it("cancel stops the pending enrollment only", () => {
      const pending = enrollment({ phase: "pending", enrollmentId: "e1" });
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: pending })),
        ).result.current.handleSlashBuiltin("tools-cancel"),
      ).toEqual({ ok: true });
      expect(pending.cancel).toHaveBeenCalledTimes(1);

      const idle = enrollment();
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: idle })),
        ).result.current.handleSlashBuiltin("tools-cancel"),
      ).toEqual({ ok: false, warning: ENROLLMENT_NONE_PENDING });
      expect(idle.cancel).not.toHaveBeenCalled();

      const busy = enrollment({
        phase: "pending",
        enrollmentId: "e1",
        idle: false,
      });
      expect(
        renderHook(() =>
          useBuiltinSlashCommands(gatedDeps({ enrollment: busy })),
        ).result.current.handleSlashBuiltin("tools-cancel"),
      ).toEqual({ ok: false, warning: NOT_IDLE_HINT });
    });

    it("both are refused where the daemon lacks workspace enrollment", () => {
      const e = enrollment({ phase: "pending", enrollmentId: "e1" });
      const { result } = renderHook(() =>
        useBuiltinSlashCommands(
          makeDeps({ serverCapabilities: {}, enrollment: e }),
        ),
      );
      expect(result.current.handleSlashBuiltin("tools-connect")).toEqual({
        ok: false,
        warning: "/tools-connect is not available on this daemon",
      });
      expect(result.current.handleSlashBuiltin("tools-cancel")).toEqual({
        ok: false,
        warning: "/tools-cancel is not available on this daemon",
      });
      expect(e.cancel).not.toHaveBeenCalled();
    });
  });

  it("/posture states the daemon's effective tier; hidden on an older daemon", () => {
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(gatedDeps({ posture: "yolo" })),
      ).result.current.handleSlashBuiltin("posture"),
    ).toEqual({ ok: true });
    expect(toast.info).toHaveBeenCalledWith(`${POSTURE_PREFIX}yolo`);
    expect(
      renderHook(() =>
        useBuiltinSlashCommands(makeDeps({ posture: "" })),
      ).result.current.handleSlashBuiltin("posture"),
    ).toEqual({
      ok: false,
      warning: "/posture is not available on this daemon",
    });
  });
});
