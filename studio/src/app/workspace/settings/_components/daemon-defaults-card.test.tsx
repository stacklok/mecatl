import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { useDaemonDefaults } from "@/features/agent/hooks/use-daemon-defaults";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import type {
  HarnessControlStatus,
  HarnessDaemonDefaults,
} from "@/lib/harness/client";
import { EMPTY_DAEMON_DEFAULTS } from "@/lib/harness/client";
import {
  DaemonDefaultsCard,
  defaultsFromDraft,
  draftFromDefaults,
  modelDefaultsKind,
  RESTART_WARNING,
} from "./daemon-defaults-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;
type DefaultsHook = ReturnType<typeof useDaemonDefaults>;

/**
 * Settings → Provider → "Default model": the one control an office user
 * needs from the saved defaults document — the default model for the
 * ACTIVE provider. Pins that (1) only that picker renders (no effort,
 * window, timeout, caching or advanced rows), (2) Save stays disabled until
 * the draft differs and always confirms with the restart sentence, (3) the
 * body sent is the whole saved document with the active provider's pair
 * folded into `models` — every other field rides along unchanged, and
 * (4) offline, missing and offline-mode states render notes, not a form,
 * while external mode renders nothing at all.
 */

const saved: HarnessDaemonDefaults = {
  ...EMPTY_DAEMON_DEFAULTS,
  models: {
    openrouter: { defaultModel: "anthropic/claude", subagentModel: "x/y" },
    anthropic: { defaultModel: "claude-sonnet-4-5", subagentModel: "" },
  },
  reasoningEffort: "high",
  contextWindowOverride: 32_000,
  promptCache: { disabled: true, anthropicTtl: "1h" },
  // The validator normalises every built-in kind's override, blank or not.
  baseUrls: {
    openrouter: "https://gw.example/v1",
    openai: "",
    anthropic: "",
    opencode: "",
  },
  aliases: { fast: "openai/gpt-4o-mini" },
  slots: { compaction: "fast" },
  apiKeyFile: "/home/op/.config/mecatl/team.yaml",
  activeProvider: "openrouter",
};

function controlStatus(
  overrides: Partial<HarnessControlStatus> = {},
): HarnessControlStatus {
  return {
    mode: "managed",
    provider: "OpenRouter",
    isMock: false,
    running: true,
    gateway: null,
    toolhiveGateway: {
      available: true,
      active: false,
      baseURL: "http://127.0.0.1:14000/v1",
    },
    modelRouter: null,
    operatorSettings: false,
    skillsDir: "",
    memoryDir: "",
    configuredProviders: ["openrouter", "anthropic"],
    selectedProvider: "openrouter",
    authFile: "/home/op/.config/mecatl/auth.yaml",
    workspace: "/repo",
    permissions: null,
    storage: null,
    retention: null,
    daemonDefaults: saved,
    ...overrides,
  };
}

const refresh = vi.fn(async () => {});

function fakeRuntime(overrides: Partial<Runtime> = {}): Runtime {
  return {
    live: true,
    mode: "managed",
    status: controlStatus(),
    permissions: null,
    models: [
      {
        id: "anthropic/claude",
        providerId: "openrouter",
        displayName: "Claude",
        contextLimit: 200_000,
        image: true,
        reasoning: true,
      },
      {
        id: "openai/gpt-5",
        providerId: "openrouter",
        displayName: "GPT-5",
        contextLimit: 400_000,
        image: true,
        reasoning: true,
      },
      {
        id: "claude-sonnet-4-5",
        providerId: "anthropic",
        displayName: "Sonnet",
        contextLimit: 200_000,
        image: true,
        reasoning: true,
      },
    ],
    isLoading: false,
    busy: "",
    error: null,
    notice: null,
    refresh,
    connectGateway: vi.fn(async () => {}),
    connectGatewayOAuth: vi.fn(async () => {}),
    savePermissions: vi.fn(async () => {}),
    saveStorage: vi.fn(async () => {}),
    ...overrides,
  } as Runtime;
}

const save = vi.fn(async (_next: unknown) => true);

function fakeDefaults(overrides: Partial<DefaultsHook> = {}): DefaultsHook {
  return {
    live: true,
    manageable: true,
    defaults: saved,
    isLoading: false,
    busy: false,
    error: null,
    notice: null,
    reload: vi.fn(async () => saved),
    save,
    ...overrides,
  };
}

beforeEach(() => {
  save.mockClear();
  refresh.mockClear();
});

describe("DaemonDefaultsCard", () => {
  it("renders only the active provider's default model picker, in plain words", () => {
    render(
      <DaemonDefaultsCard runtime={fakeRuntime()} defaults={fakeDefaults()} />,
    );
    expect(
      screen.getByRole("heading", { name: "Default model" }),
    ).toBeVisible();
    expect(
      screen.getByText(
        "The model the agent uses when a chat does not pick one.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "Default model" }),
    ).toHaveTextContent("Claude");
    expect(screen.getAllByRole("combobox")).toHaveLength(1);
    expect(
      document.querySelectorAll("input, textarea, [role=switch]"),
    ).toHaveLength(0);
    // The removed rows are gone with their jargon.
    expect(screen.queryByText(/Subagent/)).not.toBeInTheDocument();
    expect(screen.queryByText(/reasoning effort/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Context window/)).not.toBeInTheDocument();
    expect(screen.queryByText(/timeout/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/caching/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Advanced" })).toBeNull();
    expect(screen.queryByText(/--|daemon|mecated|spawn|flag/)).toBeNull();
    expect(
      screen.getByText(
        "Changes restart the agent. Anything running will stop.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("picks a default model, confirms the restart, and PUTs the pair folded into models with the rest of the document unchanged", async () => {
    const user = userEvent.setup();
    render(
      <DaemonDefaultsCard runtime={fakeRuntime()} defaults={fakeDefaults()} />,
    );
    await user.click(screen.getByRole("combobox", { name: "Default model" }));
    await user.click(await screen.findByRole("option", { name: "GPT-5" }));
    const saveButton = screen.getByRole("button", { name: "Save" });
    expect(saveButton).toBeEnabled();

    await user.click(saveButton);
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Save and restart the agent?");
    expect(dialog).toHaveTextContent(RESTART_WARNING);
    expect(dialog).not.toHaveTextContent(/daemon|mecated|refused/);
    expect(save).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(save).not.toHaveBeenCalled();

    await user.click(saveButton);
    await user.click(
      await screen.findByRole("button", { name: "Save and restart" }),
    );
    expect(save).toHaveBeenCalledTimes(1);
    // Everything an operator configured elsewhere — the helper model, the
    // effort tier, the window, caching, base URLs, aliases, slots, the key
    // file — rides along byte for byte.
    expect(save.mock.calls[0][0]).toEqual({
      ...saved,
      models: {
        anthropic: { defaultModel: "claude-sonnet-4-5", subagentModel: "" },
        openrouter: { defaultModel: "openai/gpt-5", subagentModel: "x/y" },
      },
      activeProvider: null,
    });
    // The status poll is refreshed so the provider list reads the new state.
    expect(refresh).toHaveBeenCalled();
  });

  it("offers the provider's own default and keeps a saved model the inventory no longer lists", async () => {
    const user = userEvent.setup();
    render(
      <DaemonDefaultsCard
        runtime={fakeRuntime()}
        defaults={fakeDefaults({
          defaults: {
            ...saved,
            models: {
              ...saved.models,
              openrouter: { defaultModel: "gone/model", subagentModel: "" },
            },
          },
        })}
      />,
    );
    const picker = screen.getByRole("combobox", { name: "Default model" });
    expect(picker).toHaveTextContent("gone/model (saved, no longer listed)");
    await user.click(picker);
    expect(
      await screen.findByRole("option", { name: "Provider's default" }),
    ).toBeInTheDocument();
  });

  it("Discard returns to the saved value and disables Save again", async () => {
    const user = userEvent.setup();
    render(
      <DaemonDefaultsCard runtime={fakeRuntime()} defaults={fakeDefaults()} />,
    );
    await user.click(screen.getByRole("combobox", { name: "Default model" }));
    await user.click(await screen.findByRole("option", { name: "GPT-5" }));
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Discard" }));
    expect(
      screen.getByRole("combobox", { name: "Default model" }),
    ).toHaveTextContent("Claude");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows a note instead of the picker while the agent is in offline mode", () => {
    render(
      <DaemonDefaultsCard
        runtime={fakeRuntime({
          status: controlStatus({
            provider: "offline mock",
            isMock: true,
            selectedProvider: "mock",
          }),
        })}
        defaults={fakeDefaults()}
      />,
    );
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(
      screen.getByText(
        "Set a provider as active above to choose its default model.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("surfaces the hook's error and shows Saving… while busy", () => {
    render(
      <DaemonDefaultsCard
        runtime={fakeRuntime()}
        defaults={fakeDefaults({
          busy: true,
          error: '--default-model "nope": not catalogued',
          notice: null,
        })}
      />,
    );
    expect(screen.getByText(/not catalogued/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
  });

  it("renders nothing in external mode", () => {
    const { container } = render(
      <DaemonDefaultsCard
        runtime={fakeRuntime({
          mode: "external",
          status: controlStatus({ mode: "external", daemonDefaults: null }),
        })}
        defaults={fakeDefaults({ manageable: false, defaults: null })}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders the offline note when the runtime is unreachable", () => {
    render(
      <DaemonDefaultsCard
        runtime={fakeRuntime({ live: false })}
        defaults={fakeDefaults()}
      />,
    );
    expect(
      screen.getByText(/The agent is offline, so these settings/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("says the default model could not be read instead of inventing one", () => {
    render(
      <DaemonDefaultsCard
        runtime={fakeRuntime()}
        defaults={fakeDefaults({ defaults: null, isLoading: false })}
      />,
    );
    expect(
      screen.getByText("The default model could not be read right now."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});

describe("draftFromDefaults / defaultsFromDraft", () => {
  it("round-trips the saved document for the active provider and keeps other providers' pairs", () => {
    const draft = draftFromDefaults(saved, "openrouter");
    expect(draft.defaultModel).toBe("anthropic/claude");
    expect(draft.subagentModel).toBe("x/y");
    expect(draft.aliases).toEqual([
      { key: "fast", value: "openai/gpt-4o-mini" },
    ]);
    expect(draft.contextWindowOverride).toBe("32000");
    expect(defaultsFromDraft(draft, saved, "openrouter")).toEqual({
      ...saved,
      activeProvider: null,
    });
    const moved = defaultsFromDraft(
      { ...draft, defaultModel: "", subagentModel: "openai/gpt-5" },
      saved,
      "openrouter",
    );
    expect(moved.models).toEqual({
      anthropic: { defaultModel: "claude-sonnet-4-5", subagentModel: "" },
      openrouter: { defaultModel: "", subagentModel: "openai/gpt-5" },
    });
  });

  it("drops abandoned blank rows and parses the typed window", () => {
    const draft = draftFromDefaults(saved, null);
    const document = defaultsFromDraft(
      {
        ...draft,
        contextWindowOverride: " 64000 ",
        aliases: [
          { key: "fast", value: "openai/gpt-4o-mini" },
          { key: "", value: "" },
        ],
      },
      saved,
      null,
    );
    expect(document.contextWindowOverride).toBe(64_000);
    expect(document.aliases).toEqual({ fast: "openai/gpt-4o-mini" });
    // A null kind (offline mode) leaves every saved pair alone.
    expect(document.models).toEqual(saved.models);
  });

  it("renders the LLM timeouts as digit strings and reads a blanked field as the agent's default", () => {
    const draft = draftFromDefaults(
      {
        ...saved,
        llmTimeouts: { perAttemptSeconds: 0, streamIdleSeconds: 45 },
      },
      null,
    );
    expect(draft.llmPerAttemptTimeout).toBe("0");
    expect(draft.llmStreamIdleTimeout).toBe("45");
    const document = defaultsFromDraft(
      { ...draft, llmPerAttemptTimeout: "", llmStreamIdleTimeout: " 45 " },
      saved,
      null,
    );
    expect(document.llmTimeouts).toEqual({
      perAttemptSeconds: 300,
      streamIdleSeconds: 45,
    });
  });
});

describe("modelDefaultsKind", () => {
  it("is the active provider, the ToolHive fallback, and never the offline mode", () => {
    expect(modelDefaultsKind(controlStatus())).toBe("openrouter");
    expect(
      modelDefaultsKind(
        controlStatus({
          selectedProvider: null,
          toolhiveGateway: { available: true, active: true },
        }),
      ),
    ).toBe("toolhive");
    expect(
      modelDefaultsKind(controlStatus({ selectedProvider: "mock" })),
    ).toBeNull();
    expect(
      modelDefaultsKind(
        controlStatus({ selectedProvider: null, toolhiveGateway: null }),
      ),
    ).toBeNull();
    expect(modelDefaultsKind(null)).toBeNull();
  });
});
