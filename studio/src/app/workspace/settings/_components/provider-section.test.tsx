import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import type { useProviderManagement } from "@/features/agent/hooks/use-provider-management";
import type { useProviderStatus } from "@/features/agent/hooks/use-provider-status";
import type { HarnessProviderInfo } from "@/lib/harness/client";
import { ProviderSection } from "./provider-section";

type Runtime = ReturnType<typeof useHarnessRuntime>;
type Management = ReturnType<typeof useProviderManagement>;

/**
 * Pins the provider surface's rules with teeth:
 * 1. NO key-paste UI anywhere (Studio rule 3) — with the Add dialog OPEN,
 *    the whole surface renders zero text inputs/textareas: adding a provider
 *    is a copyable snippet, never a form field a key could be typed into.
 * 2. Removal confirms with the restart sentence before any write, and
 *    external mode renders the managed note with no management controls.
 * 3. Rows read in plain words: the provider's name (not its id), one status
 *    line, an Active badge — no file names, no class or next-step lines.
 */

const status = {
  mode: "managed" as const,
  provider: "OpenRouter",
  running: true,
  gateway: null,
  modelRouter: null,
  operatorSettings: false,
  skillsDir: "",
  memoryDir: "",
  isMock: false,
  toolhiveGateway: null,
  configuredProviders: ["openrouter", "anthropic"],
  selectedProvider: "openrouter",
  authFile: "/home/op/.config/mecatl/auth.yaml",
  workspace: "",
  permissions: null,
  storage: null,
  retention: null,
};

function fakeRuntime(overrides: Partial<Runtime> = {}): Runtime {
  return {
    live: true,
    mode: "managed",
    status,
    models: [
      {
        id: "anthropic/claude",
        providerId: "openrouter",
        displayName: "Claude",
        contextLimit: 200_000,
        image: true,
        reasoning: true,
      },
    ],
    isLoading: false,
    busy: "",
    error: null,
    notice: null,
    refresh: vi.fn(async () => {}),
    connectGateway: vi.fn(async () => {}),
    connectGatewayOAuth: vi.fn(async () => {}),
    permissions: null,
    savePermissions: vi.fn(async () => {}),
    saveStorage: vi.fn(async () => {}),
    saveRetention: vi.fn(async () => {}),
    trustProject: vi.fn(async () => {}),
    trustProjectOnce: vi.fn(async () => {}),
    ...overrides,
  };
}

const removeProvider = vi.fn(async () => {});
const testKey = vi.fn(async () => {});

function fakeManagement(overrides: Partial<Management> = {}): Management {
  return {
    setActiveProvider: vi.fn(async () => {}),
    live: true,
    manageable: true,
    providers: [
      {
        name: "openrouter",
        configured: true,
        keyPresent: true,
        source: "auth.yaml",
        testable: true,
      },
      {
        name: "openai-codex",
        configured: true,
        keyPresent: false,
        source: "auth.yaml",
        testable: false,
      },
    ],
    known: [
      {
        name: "anthropic",
        label: "Anthropic",
        testable: true,
        snippet: "providers:\n  anthropic:\n    api_key: <YOUR_KEY>\n",
        note: "An Anthropic API key.",
      },
    ],
    health: {},
    isLoading: false,
    busy: "",
    error: null,
    notice: null,
    reload: vi.fn(async () => []),
    testKey,
    addCustomProvider: vi.fn(async () => ({ ok: true, restarted: false })),
    removeProvider,
    restartDaemon: vi.fn(async () => {}),
    startToolhive: vi.fn(async () => {}),
    ...overrides,
  };
}

type ProviderStatus = ReturnType<typeof useProviderStatus>;
type StatusRow = ProviderStatus["rows"][number];

/** The agent's provider_status hook, faked from a fixed row set. */
function fakeProviderStatus(rows: StatusRow[]): ProviderStatus {
  return {
    live: true,
    rows,
    isLoading: false,
    error: null,
    refresh: vi.fn(async () => {}),
    forProvider: (id: string) =>
      rows.find((row) => row.providerId === id) ?? null,
  };
}

/** A server row with the inventory parity fields filled in. */
function parityRow(
  overrides: Partial<HarnessProviderInfo> & { name: string },
): HarnessProviderInfo {
  return {
    configured: true,
    keyPresent: true,
    source: "auth.yaml",
    testable: true,
    class: "built-in",
    authMethod: "api_key",
    envShadowed: false,
    authState: "configured",
    defaultModel: "",
    nextStep: "set as active to use it",
    active: false,
    ...overrides,
  };
}

const toolhiveRow = (
  overrides: Partial<HarnessProviderInfo> = {},
): HarnessProviderInfo =>
  parityRow({
    name: "toolhive",
    configured: false,
    source: "thv llm proxy",
    testable: false,
    class: "external",
    authMethod: "external",
    authState: "gateway not reachable",
    nextStep: "start the gateway (thv llm proxy start)",
    baseURL: "http://127.0.0.1:14000/v1",
    reachable: false,
    thvOnPath: true,
    ...overrides,
  });

beforeEach(() => {
  removeProvider.mockClear();
  testKey.mockClear();
});

describe("provider list", () => {
  it("renders rows by plain name with one status line, an Active badge and never a key value", () => {
    render(
      <ProviderSection runtime={fakeRuntime()} management={fakeManagement()} />,
    );
    expect(screen.getByText("OpenRouter")).toBeInTheDocument();
    expect(screen.getByText("OpenAI Codex subscription")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText(/Key added · 1 model/)).toBeInTheDocument();
    expect(screen.getByText(/No key added · 0 models/)).toBeInTheDocument();
    // No file names, source labels or class/next-step lines on a row.
    expect(screen.queryByText(/auth\.yaml/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^Next:/)).not.toBeInTheDocument();
    expect(screen.queryByText("built-in")).not.toBeInTheDocument();
    expect(screen.queryByText(/running|stopped/)).not.toBeInTheDocument();
  });

  it("reads the key check verdict back in plain words", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          health: {
            openrouter: { state: "rejected", detail: "401 from provider" },
          },
        })}
      />,
    );
    expect(screen.getByText(/Key rejected · 1 model/)).toBeInTheDocument();
    // The raw detail stays out of the row.
    expect(screen.queryByText(/401/)).not.toBeInTheDocument();
  });

  it("offers NO key input anywhere, even with the Add dialog open", async () => {
    const user = userEvent.setup();
    render(
      <ProviderSection runtime={fakeRuntime()} management={fakeManagement()} />,
    );
    await user.click(screen.getByRole("button", { name: /Add provider/ }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText(/Studio never sees your key/)).toBeTruthy();
    // Rule 3's UI half: the entire surface — rows, menu, open dialog —
    // contains no element a credential could be typed or pasted into.
    expect(document.querySelectorAll("input, textarea")).toHaveLength(0);
  });

  it("hides the offline-mode switch and the not-yet-added kinds list", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          providers: [
            parityRow({ name: "openrouter" }),
            parityRow({
              name: "anthropic",
              configured: false,
              keyPresent: false,
              source: "",
              testable: false,
              authState: "not configured",
              nextStep: "add an API key to auth.yaml",
            }),
          ],
        })}
      />,
    );
    expect(
      screen.queryByRole("button", { name: /offline mock/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("available-kinds")).not.toBeInTheDocument();
    expect(screen.queryByText("Anthropic")).not.toBeInTheDocument();
    // Only the one Add entry point remains.
    expect(screen.getByRole("button", { name: /Add provider/ })).toBeVisible();
  });

  it("shows the empty state in plain words when nothing is configured", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({ providers: [toolhiveRow()] })}
      />,
    );
    expect(
      screen.getByText("No provider yet. Add one to start chatting."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/offline mock/)).not.toBeInTheDocument();
    expect(screen.queryByText(/ToolHive/)).not.toBeInTheDocument();
  });
});

describe("row actions", () => {
  it("confirms Remove provider with the restart sentence before calling the server", async () => {
    const user = userEvent.setup();
    render(
      <ProviderSection runtime={fakeRuntime()} management={fakeManagement()} />,
    );
    await user.click(
      screen.getByRole("button", { name: "Actions for OpenRouter" }),
    );
    // One removal action, whatever the provider's kind.
    expect(
      screen.queryByRole("menuitem", { name: /Remove key/ }),
    ).not.toBeInTheDocument();
    await user.click(
      await screen.findByRole("menuitem", { name: "Remove provider" }),
    );
    expect(removeProvider).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Remove OpenRouter?");
    expect(dialog).toHaveTextContent(
      "The agent forgets this provider and its key. Changes restart the agent. Anything running will stop.",
    );
    // The active provider gets the extra sentence, in plain words.
    expect(dialog).toHaveTextContent(/It is the active provider/);
    expect(dialog).not.toHaveTextContent(/MECATL|auth\.yaml|daemon/);
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(removeProvider).toHaveBeenCalledWith("openrouter", "all");
  });

  it("says the agent goes offline when the only provider is removed", async () => {
    const user = userEvent.setup();
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          providers: [parityRow({ name: "openrouter" })],
        })}
      />,
    );
    await user.click(
      screen.getByRole("button", { name: "Actions for OpenRouter" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "Remove provider" }),
    );
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      /It is the only provider, so the agent will be offline until you add one\./,
    );
  });

  it("Check key runs the server-side check; the item is absent for a kind that cannot be checked", async () => {
    const user = userEvent.setup();
    render(
      <ProviderSection runtime={fakeRuntime()} management={fakeManagement()} />,
    );
    await user.click(
      screen.getByRole("button", { name: "Actions for OpenRouter" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "Check key" }),
    );
    expect(testKey).toHaveBeenCalledWith("openrouter");

    await user.keyboard("{Escape}");
    await user.click(
      screen.getByRole("button", {
        name: "Actions for OpenAI Codex subscription",
      }),
    );
    await screen.findByRole("menuitem", { name: "Set as active" });
    expect(
      screen.queryByRole("menuitem", { name: /Check key/ }),
    ).not.toBeInTheDocument();
  });

  it("Set as active calls the server and refreshes the status", async () => {
    const user = userEvent.setup();
    const setActiveProvider = vi.fn(async () => {});
    const refresh = vi.fn(async () => {});
    render(
      <ProviderSection
        runtime={fakeRuntime({ refresh })}
        management={fakeManagement({
          providers: [
            parityRow({ name: "openrouter" }),
            parityRow({ name: "anthropic" }),
          ],
          setActiveProvider,
        })}
      />,
    );
    await user.click(
      screen.getByRole("button", { name: "Actions for Anthropic" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "Set as active" }),
    );
    expect(setActiveProvider).toHaveBeenCalledWith("anthropic");
    expect(refresh).toHaveBeenCalled();
  });

  it("a custom provider removes as one action too, disabled while its settings are managed outside Studio", async () => {
    const user = userEvent.setup();
    const custom = parityRow({
      name: "my-gw",
      class: "custom",
      authMethod: "api_key",
      keyPresent: true,
      source: "settings.yaml + auth.yaml",
      testable: true,
    });
    const { rerender } = render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({ providers: [custom] })}
      />,
    );
    expect(screen.getByText("my-gw")).toBeInTheDocument();
    expect(screen.queryByText(/settings\.yaml/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Actions for my-gw" }));
    expect(
      screen.queryByRole("menuitem", { name: /keep provider/ }),
    ).not.toBeInTheDocument();
    await user.click(
      await screen.findByRole("menuitem", { name: "Remove provider" }),
    );
    await user.click(await screen.findByRole("button", { name: "Remove" }));
    expect(removeProvider).toHaveBeenCalledWith("my-gw", "all");

    rerender(
      <ProviderSection
        runtime={fakeRuntime({ status: { ...status, operatorSettings: true } })}
        management={fakeManagement({ providers: [custom] })}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Actions for my-gw" }));
    const remove = await screen.findByRole("menuitem", {
      name: "Remove provider",
    });
    expect(remove).toHaveAttribute("aria-disabled", "true");
    expect(remove).toHaveAttribute(
      "title",
      "This provider is set where the agent runs and can't be removed here.",
    );
  });

  it("lists a keyless custom provider as a normal row that needs no key", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          providers: [
            parityRow({
              name: "open-gw",
              class: "custom",
              authMethod: "none",
              keyPresent: true,
              source: "settings.yaml",
              testable: false,
              authState: "not required",
            }),
          ],
        })}
      />,
    );
    expect(screen.getByText("open-gw")).toBeInTheDocument();
    expect(screen.getByText(/No key needed/)).toBeInTheDocument();
  });

  it("shows the agent's own problem with a provider as one plain sentence, and nothing when it is fine", () => {
    const rows = [parityRow({ name: "openrouter", defaultModel: "a/b" })];
    const { rerender } = render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({ providers: rows })}
        providerStatus={fakeProviderStatus([
          {
            providerId: "openrouter",
            state: "unreachable",
            hint: "check the network",
            defaultModelAutoSelected: false,
            modelCount: 0,
            availableNotDefault: false,
          },
        ])}
      />,
    );
    expect(
      screen.getByText("The provider could not be reached."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/daemon:/)).not.toBeInTheDocument();

    rerender(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({ providers: rows })}
        providerStatus={fakeProviderStatus([
          {
            providerId: "openrouter",
            state: "ok",
            hint: "",
            defaultModelAutoSelected: true,
            modelCount: 3,
            availableNotDefault: true,
          },
        ])}
      />,
    );
    expect(screen.queryByText(/could not be reached/)).not.toBeInTheDocument();
    expect(screen.queryByText(/auto-selected/)).not.toBeInTheDocument();
    expect(screen.queryByText(/default: a\/b/)).not.toBeInTheDocument();
  });
});

describe("the ToolHive gateway", () => {
  it("is not listed while it is unreachable — no start or re-check controls", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          providers: [parityRow({ name: "openrouter" }), toolhiveRow()],
        })}
      />,
    );
    expect(screen.queryByText(/ToolHive/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Start gateway/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Re-check/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/thv/)).not.toBeInTheDocument();
  });

  it("lists as a plain row once reachable and can be set as active; it has no key or removal actions", async () => {
    const user = userEvent.setup();
    const setActiveProvider = vi.fn(async () => {});
    render(
      <ProviderSection
        runtime={fakeRuntime()}
        management={fakeManagement({
          providers: [
            parityRow({ name: "openrouter" }),
            toolhiveRow({
              configured: true,
              reachable: true,
              authState: "gateway reachable",
              nextStep: "set as active to use it",
            }),
          ],
          setActiveProvider,
        })}
      />,
    );
    expect(screen.getByText("ToolHive gateway")).toBeInTheDocument();
    expect(screen.getByText(/Ready · 0 models/)).toBeInTheDocument();
    expect(screen.queryByText(/127\.0\.0\.1/)).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("button", { name: "Actions for ToolHive gateway" }),
    );
    expect(
      screen.queryByRole("menuitem", { name: /Check key/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: /Remove/ }),
    ).not.toBeInTheDocument();
    await user.click(
      await screen.findByRole("menuitem", { name: "Set as active" }),
    );
    expect(setActiveProvider).toHaveBeenCalledWith("toolhive");
  });

  it("shows the Active badge when the agent runs on it", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime({
          status: { ...status, selectedProvider: "toolhive" },
        })}
        management={fakeManagement({
          providers: [toolhiveRow({ configured: true, active: true })],
        })}
      />,
    );
    expect(screen.getByText("ToolHive gateway")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
  });
});

describe("external mode", () => {
  it("renders the managed note and no management controls", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime({
          mode: "external",
          status: { ...status, mode: "external" },
        })}
        management={fakeManagement({
          manageable: false,
          providers: [],
          known: [],
        })}
      />,
    );
    expect(
      screen.getByText(/The agent is run somewhere else/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Add provider/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/Check key/)).not.toBeInTheDocument();
    expect(document.querySelectorAll("input, textarea")).toHaveLength(0);
  });

  it("lists the agent's providers read-only, with a problem line only when one is not fine", () => {
    render(
      <ProviderSection
        runtime={fakeRuntime({
          mode: "external",
          status: { ...status, mode: "external" },
        })}
        management={fakeManagement({
          manageable: false,
          providers: [],
          known: [],
        })}
        providerStatus={fakeProviderStatus([
          {
            providerId: "fixture",
            state: "ok",
            hint: "",
            defaultModelAutoSelected: false,
            modelCount: 1,
            availableNotDefault: false,
          },
          {
            providerId: "toolhive",
            state: "unreachable",
            hint: "start it with `thv llm proxy start`",
            defaultModelAutoSelected: false,
            modelCount: 0,
            availableNotDefault: false,
          },
        ])}
      />,
    );
    const ok = document.querySelector("[data-provider-status='fixture']");
    expect(ok).toHaveTextContent("Ready · 1 model");
    expect(ok?.querySelector("[data-role='hint']")).toBeNull();
    const down = document.querySelector("[data-provider-status='toolhive']");
    expect(down).toHaveTextContent("ToolHive gateway");
    expect(down).toHaveTextContent("Not available · 0 models");
    expect(down?.querySelector("[data-role='hint']")).toHaveTextContent(
      "The provider could not be reached.",
    );
    // The agent's own hint wording stays out of the page.
    expect(screen.queryByText(/thv llm/)).not.toBeInTheDocument();
    expect(
      screen.queryByText(/Daemon provider status/),
    ).not.toBeInTheDocument();
    expect(document.querySelectorAll("button")).toHaveLength(0);
  });
});
