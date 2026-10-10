// SPDX-License-Identifier: Apache-2.0

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import {
  getAuthSessionOptions,
  getRuntimeOptions,
  getRuntimeSettingsOptions,
  getStorageHealthOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { PageShell } from "../../components/shell/page-shell";
import { Badge } from "../../components/ui/badge";
import { Input } from "../../components/ui/input";
import { Switch } from "../../components/ui/switch";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { pageTitleClass } from "../../lib/typography";
import { cn } from "../../lib/utils";
import { LearningSettingsPage } from "../learning/learning-settings-page";
import { MemorySettingsPage } from "../memory/memory-settings-page";
import { AboutDaemonCard, AboutStudioCard, DaemonFacts, SignInCard } from "./about-cards";
import { AgentSettings } from "./agent-settings";
import { IdentitySettings } from "./identity-settings";
import { InterfaceSettings } from "./interface-settings";
import { managementNotes } from "./management-notes";
import {
  FactList,
  FactRow,
  Note,
  RuntimeStatusLine,
  SettingsCard,
  SettingsRow,
  type SettingsState,
} from "./settings-card";
import {
  connectionMessage,
  freshDeploymentQuery,
  useBrowserOnline,
  useRefreshOnEntry,
} from "./settings-connection";
import { type SettingsSection, settingsGroups, settingsItem } from "./settings-sections";
import { StorageSettings } from "./storage-settings";

type Model = GetRuntimeSettingsResponse["models"][number];

/** The card a section's loading, offline, or failure line sits in. */
const stateCardTitles: Record<SettingsSection, string> = {
  about: "About",
  agent: "Agent behavior",
  appearance: "Appearance",
  diagnostics: "Diagnostics",
  labs: "Labs",
  learning: "Learning",
  "mcp-tools": "MCP tools",
  memory: "Memory",
  models: "Models",
  permissions: "Permissions",
  profile: "You",
  providers: "Providers",
  storage: "Storage",
};

function connectionState(connection: GetRuntimeResponse["connection"]): SettingsState | null {
  const text = connectionMessage(connection);
  if (text === null) return null;
  if (connection === "connecting" || connection === "reconnecting")
    return { kind: "loading", text };
  if (connection === "incompatible") return { kind: "error", text };
  return { kind: "notice", text };
}

export function SettingsWorkspace({
  onSectionChange,
  section = "profile",
}: {
  onSectionChange?: (section: SettingsSection) => void;
  section?: SettingsSection;
}) {
  const browserOnline = useBrowserOnline();
  const deploymentSection = section !== "profile" && section !== "appearance";
  const inventorySection =
    section === "providers" ||
    section === "models" ||
    section === "about" ||
    section === "diagnostics";
  const runtime = useQuery({
    ...getRuntimeOptions(),
    ...freshDeploymentQuery,
    enabled: browserOnline && deploymentSection,
  });
  const settings = useQuery({
    ...getRuntimeSettingsOptions(),
    ...freshDeploymentQuery,
    enabled: browserOnline && inventorySection,
  });
  const runtimeValidating = useRefreshOnEntry(
    `settings:${section}:runtime`,
    browserOnline && deploymentSection,
    runtime.data !== undefined,
    runtime.refetch,
  );
  const settingsValidating = useRefreshOnEntry(
    `settings:${section}:inventory`,
    browserOnline && inventorySection,
    settings.data !== undefined,
    settings.refetch,
  );
  const modelPreferences = useDisabledModels();
  const runtimeState: SettingsState | null = !browserOnline
    ? {
        kind: "notice",
        text: "Offline. Connect to the agent to read current deployment settings.",
      }
    : runtimeValidating || runtime.isFetching || runtime.isPending
      ? { kind: "loading", text: "Loading current runtime settings…" }
      : runtime.isError
        ? {
            kind: "error",
            text: "Current runtime settings could not be loaded. Check the connection and try again.",
          }
        : connectionState(runtime.data.connection);
  const inventoryState: SettingsState | null =
    runtimeState ??
    (settingsValidating || settings.isFetching || settings.isPending
      ? { kind: "loading", text: "Loading settings…" }
      : settings.isError
        ? {
            kind: "error",
            text: "Current settings could not be loaded. Check the connection and try again.",
          }
        : null);
  const stateCard = (state: SettingsState) => (
    <SettingsCard title={stateCardTitles[section]}>
      <RuntimeStatusLine state={state} />
    </SettingsCard>
  );
  const CurrentIcon = settingsItem(section).icon;

  return (
    <PageShell>
      <div className="space-y-6">
        <h1 className={pageTitleClass()}>Settings</h1>

        <div className="min-[500px]:hidden">
          <label
            className="block px-4 pb-2 text-[13px] font-medium text-muted-foreground"
            htmlFor="settings-section"
          >
            Settings section
          </label>
          {/* The prototype's drill-down row look, kept on the native picker that
              Studio's one-route-per-section navigation relies on. */}
          <div className="relative">
            <CurrentIcon
              aria-hidden="true"
              className="pointer-events-none absolute top-1/2 left-4 size-[18px] -translate-y-1/2 text-muted-foreground"
            />
            <select
              className="min-h-11 w-full appearance-none rounded-2xl border-0 bg-muted/50 py-2 pr-10 pl-11 text-[15px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              id="settings-section"
              onChange={(event) => onSectionChange?.(event.target.value as SettingsSection)}
              value={section}
            >
              {settingsGroups.map((group) => (
                <optgroup key={group.title} label={group.title}>
                  {group.items.map((item) => (
                    <option key={item.value} value={item.value}>
                      {item.label}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
            <ChevronDown
              aria-hidden="true"
              className="pointer-events-none absolute top-1/2 right-4 size-4 -translate-y-1/2 text-muted-foreground"
            />
          </div>
        </div>

        <div className="flex flex-col gap-6 min-[500px]:flex-row min-[500px]:items-start min-[500px]:gap-10">
          <nav
            aria-label="Settings sections"
            className="hidden w-44 shrink-0 flex-col gap-5 min-[500px]:flex"
          >
            {settingsGroups.map((group) => (
              <div className="space-y-1" key={group.title}>
                <p className="px-2 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
                  {group.title}
                </p>
                <ul className="flex flex-col gap-1">
                  {group.items.map((item) => (
                    <li key={item.value}>
                      <button
                        aria-current={section === item.value ? "page" : undefined}
                        className={cn(
                          "flex min-h-11 w-full items-center rounded-lg px-2 text-left text-sm whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-brand",
                          section === item.value
                            ? "bg-muted font-medium text-foreground"
                            : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
                        )}
                        onClick={() => onSectionChange?.(item.value)}
                        type="button"
                      >
                        {item.label}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </nav>

          <div className="min-w-0 max-w-3xl flex-1 space-y-5">
            {section === "profile" && (
              <>
                <IdentitySettings />
                <ProfileSession />
              </>
            )}
            {section === "agent" && (
              <>
                <AgentSettings />
                <SettingsCard title="Agent behavior">
                  {runtimeState ? (
                    <RuntimeStatusLine state={runtimeState} />
                  ) : (
                    <div className="flex flex-col gap-3">
                      <div className="divide-y divide-border/60">
                        <SettingsRow
                          description="Whether a message sent while the agent works can redirect it."
                          label="Steering during a run"
                        >
                          <Badge variant={runtime.data?.capabilities.steer ? "success" : "muted"}>
                            {runtime.data?.capabilities.steer ? "Available" : "Not enabled"}
                          </Badge>
                        </SettingsRow>
                      </div>
                      <Note>Agent behavior is managed by this deployment.</Note>
                    </div>
                  )}
                </SettingsCard>
              </>
            )}
            {section === "appearance" && <InterfaceSettings />}
            {section === "providers" &&
              (inventoryState
                ? stateCard(inventoryState)
                : settings.data && <ProviderInventory settings={settings.data} />)}
            {section === "models" &&
              (inventoryState
                ? stateCard(inventoryState)
                : settings.data && (
                    <ModelInventory modelPreferences={modelPreferences} settings={settings.data} />
                  ))}
            {section === "about" &&
              (inventoryState
                ? stateCard(inventoryState)
                : runtime.data &&
                  settings.data && (
                    <>
                      <AboutStudioCard runtime={runtime.data} />
                      <AboutDaemonCard runtime={runtime.data} settings={settings.data} />
                      <SignInCard />
                    </>
                  ))}
            {section === "memory" &&
              (runtimeState ? (
                stateCard(runtimeState)
              ) : (
                <MemorySettingsPage capabilities={runtime.data?.capabilities} />
              ))}
            {section === "learning" &&
              (runtimeState ? stateCard(runtimeState) : <LearningSettingsPage />)}
            {section === "storage" &&
              (runtimeState ? stateCard(runtimeState) : <StorageSettings />)}
            {section === "permissions" && (
              <SettingsCard title="Permissions">
                {runtimeState ? (
                  <RuntimeStatusLine state={runtimeState} />
                ) : (
                  <div className="flex flex-col gap-3">
                    <div className="divide-y divide-border/60">
                      <SettingsRow
                        description="What the agent is running at right now."
                        label="Safety level"
                      >
                        <PostureBadge posture={runtime.data?.capabilities.posture} />
                      </SettingsRow>
                    </div>
                    <Note>Permission posture is managed by this deployment.</Note>
                  </div>
                )}
              </SettingsCard>
            )}
            {section === "mcp-tools" && (
              <SettingsCard title="MCP tools">
                {runtimeState ? (
                  <RuntimeStatusLine state={runtimeState} />
                ) : (
                  <div className="flex flex-col gap-3">
                    <FactList>
                      <FactRow label="MCP support">
                        {runtime.data?.capabilities.mcp ? "Available" : "Not enabled"}
                      </FactRow>
                      <FactRow label="Connector status">
                        {runtime.data?.capabilities.mcpConnectorStatus
                          ? "Available"
                          : "Not enabled"}
                      </FactRow>
                    </FactList>
                    <Note>
                      MCP setup is managed by this deployment. Studio does not yet show the tool
                      inventory.
                    </Note>
                  </div>
                )}
              </SettingsCard>
            )}
            {section === "diagnostics" &&
              (inventoryState
                ? stateCard(inventoryState)
                : runtime.data &&
                  settings.data && (
                    <DiagnosticsSettings runtime={runtime.data} settings={settings.data} />
                  ))}
            {section === "labs" && (
              <SettingsCard title="Labs">
                {runtimeState ? (
                  <RuntimeStatusLine state={runtimeState} />
                ) : (
                  <Note>
                    No Labs features are available in Studio yet. This runtime is{" "}
                    {runtime.data?.mock ? "a local mock" : "a connected agent"}.
                  </Note>
                )}
              </SettingsCard>
            )}
          </div>
        </div>
      </div>
    </PageShell>
  );
}

/** The user-facing word for each posture tier the SDK reports. */
const postureLabels: Record<string, string> = {
  auto: "Auto",
  strict: "Strict",
  trusted: "Trusted",
  yolo: "Yolo",
};

function PostureBadge({ posture }: { posture: string | undefined }) {
  const tier = posture?.trim();
  if (!tier) return <Badge variant="muted">Not reported</Badge>;
  const normalized = tier.toLowerCase();
  const variant =
    normalized === "yolo"
      ? "destructive"
      : normalized === "auto"
        ? "warning"
        : normalized === "trusted"
          ? "info"
          : "outline";
  return <Badge variant={variant}>{postureLabels[normalized] ?? tier}</Badge>;
}

function ProfileSession() {
  const browserOnline = useBrowserOnline();
  const session = useQuery({
    ...getAuthSessionOptions(),
    enabled: browserOnline,
    refetchOnMount: false,
    retry: false,
    staleTime: 0,
  });
  const sessionValidating = useRefreshOnEntry(
    "profile:session",
    browserOnline,
    session.data !== undefined,
    session.refetch,
  );
  const sessionState: SettingsState | null = !browserOnline
    ? { kind: "notice", text: "Offline. Sign-in details are unavailable." }
    : sessionValidating || session.isFetching || session.isPending
      ? { kind: "loading", text: "Checking sign-in details…" }
      : session.isError
        ? { kind: "error", text: "Sign-in details could not be loaded." }
        : null;
  const account =
    session.data?.mode === "oidc" && session.data.status === "authenticated"
      ? session.data.account?.trim()
      : undefined;

  return (
    <SettingsCard title="Sign-in session">
      {sessionState ? (
        <RuntimeStatusLine state={sessionState} />
      ) : (
        <FactList>
          <FactRow label="Session status">
            {session.data?.status === "authenticated" ? "Signed in" : "Sign-in not required"}
          </FactRow>
          <FactRow label="Authentication mode">
            {session.data?.mode === "oidc"
              ? "Interactive sign-in"
              : session.data?.mode === "static"
                ? "Shared static identity"
                : "No authentication"}
          </FactRow>
          {account && <FactRow label="Account reference">{account}</FactRow>}
        </FactList>
      )}
    </SettingsCard>
  );
}

function ProviderInventory({ settings }: { settings: GetRuntimeSettingsResponse }) {
  return (
    <SettingsCard title="Providers">
      <div className="flex flex-col gap-3">
        <ul className="space-y-1 text-sm text-muted-foreground">
          {managementNotes(settings.management).map((note) => (
            <li key={note}>{note}</li>
          ))}
          <li>Credentials are never shown here.</li>
        </ul>
        {!settings.modelsSupported ? (
          <Note role="status">
            {settings.modelsReason || "Model providers are not available on this deployment."}
          </Note>
        ) : settings.providers.length === 0 ? (
          <Note role="status">No providers are reported by this deployment yet.</Note>
        ) : (
          <ul className="divide-y divide-border/60 overflow-hidden rounded-lg border">
            {settings.providers.map((provider) => (
              <ProviderRow key={provider.id} provider={provider} />
            ))}
          </ul>
        )}
      </div>
    </SettingsCard>
  );
}

/** A read-only provider row in the prototype's list grammar, linked to its detail page. */
function ProviderRow({ provider }: { provider: GetRuntimeSettingsResponse["providers"][number] }) {
  const state = providerState(provider.state);
  return (
    <li>
      <Link
        className="flex min-h-11 items-center gap-3 px-4 py-2 hover:bg-muted/40 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
        search={{ providerId: provider.id }}
        to="/workspace/provider"
      >
        <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", state.dot)} />
        <span className="min-w-0 flex-1">
          <span className="block text-sm font-medium">{humanize(provider.id)}</span>
          <span className="block truncate text-xs text-muted-foreground tabular-nums">
            {state.label} · {provider.modelCount} model{provider.modelCount === 1 ? "" : "s"}
          </span>
          {!state.ready && provider.hint && (
            <span className="block text-xs text-muted-foreground">{provider.hint}</span>
          )}
          <span className="sr-only">View provider details</span>
        </span>
      </Link>
    </li>
  );
}

function ModelInventory({
  modelPreferences,
  settings,
}: {
  modelPreferences: ReturnType<typeof useDisabledModels>;
  settings: GetRuntimeSettingsResponse;
}) {
  const [search, setSearch] = useState("");
  const models = useMemo(() => {
    const query = search.trim().toLowerCase();
    return query
      ? settings.models.filter((model) =>
          `${model.providerId} ${model.id} ${model.displayName}`.toLowerCase().includes(query),
        )
      : settings.models;
  }, [search, settings.models]);

  return (
    <SettingsCard title="Models">
      <div className="flex flex-col gap-4">
        <Note>
          Visible is a personal preference stored in this browser. Default model and routing are
          managed by the deployment.
        </Note>
        <FactList>
          <FactRow label="Default model">Managed by deployment; not reported to Studio.</FactRow>
          <FactRow label="Routing">
            {settings.management.routingConfigurationReason || "Managed by deployment."}
          </FactRow>
        </FactList>
        {settings.modelsSupported && settings.models.length > 0 && (
          <div className="relative max-w-sm">
            <Search className="pointer-events-none absolute left-3 top-3.5 size-4 text-muted-foreground" />
            <Input
              aria-label="Filter models"
              className="min-h-11 pl-9"
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Find a model"
              value={search}
            />
          </div>
        )}
        {!settings.modelsSupported ? (
          <Note role="status">
            {settings.modelsReason || "Model selection is not available on this deployment."}
          </Note>
        ) : settings.models.length === 0 ? (
          <Note role="status">No models are available yet.</Note>
        ) : models.length === 0 ? (
          <Note role="status">No models match your search.</Note>
        ) : (
          <div className="grid gap-3 min-[500px]:grid-cols-2 lg:grid-cols-3">
            {models.map((model) => (
              <ModelCard
                enabled={!modelPreferences.disabled.has(modelPreferenceId(model))}
                key={`${model.providerId}-${model.id}`}
                model={model}
                onEnabledChange={(enabled) =>
                  modelPreferences.setModelEnabled(modelPreferenceId(model), enabled)
                }
              />
            ))}
          </div>
        )}
      </div>
    </SettingsCard>
  );
}

function ModelCard({
  enabled,
  model,
  onEnabledChange,
}: {
  enabled: boolean;
  model: Model;
  onEnabledChange: (enabled: boolean) => void;
}) {
  return (
    <article className="rounded-lg border bg-background p-4" title={model.id}>
      <div className="flex items-start justify-between gap-3">
        <h3 className="text-sm font-medium">{model.displayName}</h3>
        <label
          className="flex min-h-11 items-center gap-2 text-xs text-muted-foreground"
          htmlFor={`visible-model-${model.providerId}-${model.id}`}
        >
          Visible
          <Switch
            aria-label={`Show ${model.displayName} in model pickers`}
            checked={enabled}
            id={`visible-model-${model.providerId}-${model.id}`}
            onCheckedChange={(checked) => onEnabledChange(checked === true)}
          />
        </label>
      </div>
      <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">ID</dt>
        <dd className="min-w-0 break-all font-mono">{model.id}</dd>
        <dt className="text-muted-foreground">Provider</dt>
        <dd className="min-w-0 break-all">{model.providerId}</dd>
        <dt className="text-muted-foreground">Context limit</dt>
        <dd>{formatContextLimit(model.contextLimit)}</dd>
        <dt className="text-muted-foreground">Images</dt>
        <dd>{model.image ? "Yes" : "No"}</dd>
        <dt className="text-muted-foreground">Reasoning</dt>
        <dd>{model.reasoning ? "Yes" : "No"}</dd>
      </dl>
    </article>
  );
}

function DiagnosticsSettings({
  runtime,
  settings,
}: {
  runtime: GetRuntimeResponse;
  settings: GetRuntimeSettingsResponse;
}) {
  const storage = useQuery(getStorageHealthOptions());
  return (
    <SettingsCard title="Diagnostics">
      <div className="flex flex-col gap-3">
        <DaemonFacts runtime={runtime} settings={settings} />
        {storage.isPending ? (
          <RuntimeStatusLine state={{ kind: "loading", text: "Loading storage diagnostics…" }} />
        ) : storage.isError ? (
          <RuntimeStatusLine
            state={{ kind: "error", text: "Storage diagnostics could not be loaded." }}
          />
        ) : !storage.data.supported ? (
          <Note role="status">Storage diagnostics are not supported by this deployment.</Note>
        ) : (
          <Note>Storage health: {storage.data.available ? "Available" : "Unavailable"}.</Note>
        )}
        <Note>Logs and usage are managed by this deployment and are not available here.</Note>
      </div>
    </SettingsCard>
  );
}

function providerState(state: string): { dot: string; label: string; ready: boolean } {
  const normalized = state.toLowerCase();
  if (normalized === "ok" || normalized === "available") {
    return { dot: "bg-success", label: "Ready", ready: true };
  }
  if (normalized === "unauthorized") {
    return { dot: "bg-warning", label: "Sign-in needed", ready: false };
  }
  if (normalized === "unreachable") {
    return { dot: "bg-destructive", label: "Unavailable", ready: false };
  }
  return { dot: "bg-muted-foreground", label: "Not ready", ready: false };
}

function humanize(value: string): string {
  return value
    .split(/[-_]+/u)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function formatContextLimit(value: string): string {
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 0 ? number.toLocaleString() : value;
}
