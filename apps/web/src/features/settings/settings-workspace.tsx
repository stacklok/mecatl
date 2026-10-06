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
import {
  BookOpen,
  BrainCircuit,
  Cloud,
  Copy,
  ExternalLink,
  Keyboard,
  Laptop,
  LifeBuoy,
  Search,
  Server,
} from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { AuthControl } from "../../components/shell/auth-control";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import { Switch } from "../../components/ui/switch";
import { modelPreferenceId, useDisabledModels } from "../../lib/model-preferences";
import { pageTitleClass } from "../../lib/typography";
import { LearningReview } from "../knowledge/learning-review";
import { AgentSettings } from "./agent-settings";
import { IdentitySettings } from "./identity-settings";
import { InterfaceSettings } from "./interface-settings";
import { managementNotes } from "./management-notes";
import { MemorySettings } from "./memory-settings";
import {
  connectionMessage,
  freshDeploymentQuery,
  useBrowserOnline,
  useRefreshOnEntry,
} from "./settings-connection";
import { type SettingsSection, settingsGroups } from "./settings-sections";
import { StorageSettings } from "./storage-settings";

type Model = GetRuntimeSettingsResponse["models"][number];

/** Where a problem with this app (not the connected agent) is reported. */
const supportUrl = "https://github.com/stacklok/mecatl/issues";

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
  const runtimeState = !browserOnline
    ? "Offline. Connect to the agent to read current deployment settings."
    : runtimeValidating || runtime.isFetching || runtime.isPending
      ? "Loading current runtime settings…"
      : runtime.isError
        ? "Current runtime settings could not be loaded. Check the connection and try again."
        : connectionMessage(runtime.data.connection);
  const inventoryState =
    runtimeState ??
    (settingsValidating || settings.isFetching || settings.isPending
      ? "Loading settings…"
      : settings.isError
        ? "Current settings could not be loaded. Check the connection and try again."
        : null);

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl px-4 py-7 sm:px-8 sm:py-10">
        <Link
          className="inline-flex min-h-11 items-center rounded-lg text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-brand"
          search={{ sessionId: undefined }}
          to="/workspace/chat"
        >
          ← Chats
        </Link>
        <h1 className={pageTitleClass()}>Settings</h1>
        <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
          Review your preferences and the settings managed by this deployment.
        </p>

        <div className="mt-7 sm:hidden">
          <label className="text-xs font-medium text-muted-foreground" htmlFor="settings-section">
            Settings section
          </label>
          <select
            className="mt-2 min-h-11 w-full rounded-lg border border-control-border bg-card px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
        </div>

        <div className="mt-7 flex items-start gap-8">
          <nav aria-label="Settings sections" className="hidden w-40 shrink-0 sm:block">
            <ul className="space-y-4">
              {settingsGroups.map((group) => (
                <li key={group.title}>
                  <p className="px-3 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                    {group.title}
                  </p>
                  <ul className="space-y-1">
                    {group.items.map((item) => (
                      <li key={item.value}>
                        <button
                          aria-current={section === item.value ? "page" : undefined}
                          className={`min-h-11 w-full rounded-lg px-3 py-2 text-left text-sm focus-visible:outline-2 focus-visible:outline-brand ${section === item.value ? "bg-muted font-medium" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"}`}
                          onClick={() => onSectionChange?.(item.value)}
                          type="button"
                        >
                          {item.label}
                        </button>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </nav>

          <div className="min-w-0 flex-1 space-y-6">
            {section === "profile" && (
              <>
                <IdentitySettings />
                <ProfileSession />
              </>
            )}
            {section === "agent" && (
              <>
                <AgentSettings />
                <Section icon={Server} title="Agent behavior">
                  <SourceNote source="authenticated BFF runtime" owner="deployment" />
                  {runtimeState ? (
                    <StateCard text={runtimeState} />
                  ) : (
                    <p className="mt-3 text-sm text-muted-foreground">
                      Steering during a run is{" "}
                      {runtime.data?.capabilities.steer ? "available" : "not enabled"}. Agent
                      behavior is managed by this deployment.
                    </p>
                  )}
                </Section>
              </>
            )}
            {section === "appearance" && <InterfaceSettings />}
            {section === "providers" &&
              (inventoryState ? (
                <StateCard text={inventoryState} />
              ) : (
                settings.data && <ProviderInventory settings={settings.data} />
              ))}
            {section === "models" &&
              (inventoryState ? (
                <StateCard text={inventoryState} />
              ) : (
                settings.data && (
                  <ModelInventory modelPreferences={modelPreferences} settings={settings.data} />
                )
              ))}
            {section === "about" &&
              (inventoryState ? (
                <StateCard text={inventoryState} />
              ) : (
                runtime.data &&
                settings.data && <AboutAgent runtime={runtime.data} settings={settings.data} />
              ))}
            {section === "memory" &&
              (runtimeState ? (
                <StateCard text={runtimeState} />
              ) : (
                <>
                  <Section icon={BrainCircuit} title="Memory">
                    <SourceNote
                      source="authenticated user-memory BFF reads"
                      owner="personal facts"
                    />
                    <p className="mt-2 text-sm text-muted-foreground">
                      Memory store configuration is managed by this deployment. Approved
                      consolidation plans can be generated and applied below.
                    </p>
                  </Section>
                  <MemorySettings />
                </>
              ))}
            {section === "learning" &&
              (runtimeState ? (
                <StateCard text={runtimeState} />
              ) : (
                <>
                  <Section icon={BrainCircuit} title="Learning settings">
                    <SourceNote
                      source="authenticated learning-proposal and reflection BFF reads"
                      owner="personal decisions"
                    />
                    <p className="mt-2 text-sm text-muted-foreground">
                      You can review proposals below. Learning configuration is managed by this
                      deployment and is read-only here.
                    </p>
                  </Section>
                  <LearningReview />
                </>
              ))}
            {section === "storage" &&
              (runtimeState ? (
                <StateCard text={runtimeState} />
              ) : (
                <>
                  <SourceNote source="authenticated BFF storage health" owner="deployment" />
                  <StorageSettings />
                </>
              ))}
            {section === "permissions" && (
              <Section icon={Server} title="Permissions">
                <SourceNote source="authenticated BFF runtime capability" owner="deployment" />
                {runtimeState ? (
                  <StateCard text={runtimeState} />
                ) : (
                  <dl className="mt-4">
                    <Fact label="Permission posture">
                      {runtime.data?.capabilities.posture || "Not reported"}
                    </Fact>
                  </dl>
                )}
                <p className="mt-3 text-sm text-muted-foreground">
                  Permission posture is managed by this deployment.
                </p>
              </Section>
            )}
            {section === "mcp-tools" && (
              <Section icon={Server} title="MCP tools">
                <SourceNote source="authenticated BFF runtime capability" owner="deployment" />
                {runtimeState ? (
                  <StateCard text={runtimeState} />
                ) : (
                  <dl className="mt-4 grid gap-3 sm:grid-cols-2">
                    <Fact label="MCP support">
                      {runtime.data?.capabilities.mcp ? "Available" : "Not enabled"}
                    </Fact>
                    <Fact label="Connector status">
                      {runtime.data?.capabilities.mcpConnectorStatus ? "Available" : "Not enabled"}
                    </Fact>
                  </dl>
                )}
                <p className="mt-3 text-sm text-muted-foreground">
                  MCP setup is managed by this deployment. Studio does not yet show the tool
                  inventory.
                </p>
              </Section>
            )}
            {section === "diagnostics" &&
              (inventoryState ? (
                <StateCard text={inventoryState} />
              ) : (
                runtime.data &&
                settings.data && (
                  <DiagnosticsSettings runtime={runtime.data} settings={settings.data} />
                )
              ))}
            {section === "labs" && (
              <Section icon={Server} title="Labs">
                <SourceNote
                  source="Studio availability and authenticated BFF runtime"
                  owner="deployment"
                />
                {runtimeState ? (
                  <StateCard text={runtimeState} />
                ) : (
                  <p className="mt-3 text-sm text-muted-foreground">
                    No Labs features are available in Studio yet. This runtime is{" "}
                    {runtime.data?.mock ? "a local mock" : "a connected agent"}.
                  </p>
                )}
              </Section>
            )}
          </div>
        </div>
      </div>
    </div>
  );
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
  const sessionState = !browserOnline
    ? "Offline. Sign-in details are unavailable."
    : sessionValidating || session.isFetching || session.isPending
      ? "Checking sign-in details…"
      : session.isError
        ? "Sign-in details could not be loaded."
        : null;
  const account =
    session.data?.mode === "oidc" && session.data.status === "authenticated"
      ? session.data.account?.trim()
      : undefined;

  return (
    <Section icon={Server} title="Sign-in session">
      <SourceNote source="BFF auth session" owner="read-only account identity" />
      {sessionState ? (
        <StateCard text={sessionState} />
      ) : (
        <dl className="mt-4 grid gap-3 sm:grid-cols-2">
          <Fact label="Session status">
            {session.data?.status === "authenticated" ? "Signed in" : "Sign-in not required"}
          </Fact>
          <Fact label="Authentication mode">
            {session.data?.mode === "oidc"
              ? "Interactive sign-in"
              : session.data?.mode === "static"
                ? "Shared static identity"
                : "No authentication"}
          </Fact>
          {account && <Fact label="Account reference">{account}</Fact>}
        </dl>
      )}
    </Section>
  );
}

function ProviderInventory({ settings }: { settings: GetRuntimeSettingsResponse }) {
  return (
    <Section icon={Server} title="Providers">
      <SourceNote source="authenticated BFF model inventory" owner="deployment" />
      <ul className="space-y-1 text-sm text-muted-foreground">
        {managementNotes(settings.management).map((note) => (
          <li key={note}>{note}</li>
        ))}
        <li>Credentials are never shown here.</li>
      </ul>
      {!settings.modelsSupported ? (
        <StateCard
          text={settings.modelsReason || "Model providers are not available on this deployment."}
        />
      ) : settings.providers.length === 0 ? (
        <StateCard text="No providers are reported by this deployment yet." />
      ) : (
        <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {settings.providers.map((provider) => {
            return (
              <article className="rounded-xl border bg-background p-4" key={provider.id}>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <h3 className="font-medium">{humanize(provider.id)}</h3>
                  <ProviderState state={provider.state} />
                </div>
                <p className="mt-2 text-sm text-muted-foreground">
                  {provider.modelCount} model{provider.modelCount === 1 ? "" : "s"} reported
                </p>
                <Link
                  className="mt-3 inline-flex min-h-11 items-center text-sm font-medium text-brand underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-brand"
                  search={{ providerId: provider.id }}
                  to="/workspace/provider"
                >
                  View provider details
                </Link>
              </article>
            );
          })}
        </div>
      )}
    </Section>
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
    <Section icon={BrainCircuit} title="Models">
      <SourceNote source="authenticated BFF model inventory" owner="deployment" />
      <p className="mt-2 text-sm text-muted-foreground">
        Visible is a personal preference stored in this browser. Default model and routing are
        managed by the deployment.
      </p>
      <dl className="mt-4 grid gap-3 sm:grid-cols-2">
        <Fact label="Default model">Managed by deployment; not reported to Studio.</Fact>
        <Fact label="Routing">
          {settings.management.routingConfigurationReason || "Managed by deployment."}
        </Fact>
      </dl>
      {settings.modelsSupported && settings.models.length > 0 && (
        <div className="relative mt-4 max-w-sm">
          <Search className="pointer-events-none absolute left-3 top-2.5 size-4 text-muted-foreground" />
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
        <StateCard
          text={settings.modelsReason || "Model selection is not available on this deployment."}
        />
      ) : settings.models.length === 0 ? (
        <StateCard text="No models are available yet." />
      ) : models.length === 0 ? (
        <StateCard text="No models match your search." />
      ) : (
        <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
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
    </Section>
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
    <article className="rounded-xl border bg-background p-4" title={model.id}>
      <div className="flex items-start justify-between gap-3">
        <h3 className="font-medium">{model.displayName}</h3>
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

function reported(value: string | undefined): string {
  return value?.trim() || "Not reported";
}

/** Only the BFF's safe build and runtime projections go into copied diagnostics. */
function supportSummary(runtime: GetRuntimeResponse, settings: GetRuntimeSettingsResponse): string {
  return [
    `Studio build: ${reported(runtime.studioBuildId)}`,
    `SDK version: ${reported(runtime.sdkVersion)}`,
    `Daemon build: ${reported(settings.buildId)}`,
    `Daemon implementation: ${reported(settings.serverImplementation)}`,
    `Runtime source: ${runtime.source}`,
    `Connection: ${runtime.connection}`,
    `Deployment: ${reported(runtime.deployment)}`,
  ].join("\n");
}

function AboutAgent({
  runtime,
  settings,
}: {
  runtime: GetRuntimeResponse;
  settings: GetRuntimeSettingsResponse;
}) {
  const [copyStatus, setCopyStatus] = useState("");
  return (
    <Section icon={runtime.source === "local" ? Laptop : Cloud} title="About">
      <SourceNote
        source="authenticated BFF runtime and settings inventory"
        owner="deployment and Studio build"
      />
      <p className="mt-3 text-sm text-muted-foreground">
        Studio is the browser client. Its build and installed SDK are reported separately from the
        connected daemon.
      </p>
      <dl className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Fact label="Studio build">{reported(runtime.studioBuildId)}</Fact>
        <Fact label="SDK version">{reported(runtime.sdkVersion)}</Fact>
        <Fact label="Daemon build">{reported(settings.buildId)}</Fact>
        <Fact label="Daemon implementation">{reported(settings.serverImplementation)}</Fact>
        <Fact label="Runtime source">{runtime.source}</Fact>
        <Fact label="Connection">{runtime.connection}</Fact>
        <Fact label="Deployment">{reported(runtime.deployment)}</Fact>
      </dl>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button
          className="min-h-11"
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(supportSummary(runtime, settings));
              setCopyStatus("Support summary copied.");
            } catch {
              setCopyStatus("Could not copy the support summary.");
            }
          }}
          size="sm"
          type="button"
          variant="outline"
        >
          <Copy aria-hidden="true" />
          Copy support summary
        </Button>
        <Button asChild className="min-h-11" size="sm" variant="outline">
          <a
            href="https://mecatl.dev/docs/building/deployment/studio"
            rel="noreferrer"
            target="_blank"
          >
            <BookOpen aria-hidden="true" />
            Documentation
            <ExternalLink aria-hidden="true" className="size-3 text-muted-foreground" />
          </a>
        </Button>
        <Button asChild className="min-h-11" size="sm" variant="outline">
          <a href={supportUrl} rel="noreferrer" target="_blank">
            <LifeBuoy aria-hidden="true" />
            Report a problem
            <ExternalLink aria-hidden="true" className="size-3 text-muted-foreground" />
          </a>
        </Button>
        <Button asChild className="min-h-11" size="sm" variant="outline">
          <Link to="/workspace/shortcuts">
            <Keyboard aria-hidden="true" />
            Keyboard shortcuts
          </Link>
        </Button>
      </div>
      {copyStatus && (
        <p className="mt-2 text-sm" role="status">
          {copyStatus}
        </p>
      )}
      <div className="mt-2 divide-y border-t">
        <AuthControl />
      </div>
    </Section>
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
    <Section icon={Server} title="Diagnostics">
      <SourceNote
        source="authenticated BFF runtime, settings inventory, and storage health"
        owner="deployment"
      />
      <dl className="mt-4 grid gap-3 sm:grid-cols-2">
        <Fact label="Runtime connection">{runtime.connection}</Fact>
        <Fact label="Runtime source">{runtime.source}</Fact>
        <Fact label="Daemon implementation">{settings.serverImplementation || "Not reported"}</Fact>
        <Fact label="Daemon build">{settings.buildId || "Not reported"}</Fact>
      </dl>
      {storage.isPending ? (
        <StateCard text="Loading storage diagnostics…" />
      ) : storage.isError ? (
        <StateCard text="Storage diagnostics could not be loaded." />
      ) : !storage.data.supported ? (
        <StateCard text="Storage diagnostics are not supported by this deployment." />
      ) : (
        <p className="mt-4 text-sm text-muted-foreground">
          Storage health: {storage.data.available ? "Available" : "Unavailable"}.
        </p>
      )}
      <p className="mt-3 text-sm text-muted-foreground">
        Logs and usage are managed by this deployment and are not available here.
      </p>
    </Section>
  );
}

function Section({
  children,
  icon: Icon,
  title,
}: {
  children: ReactNode;
  icon: typeof Server;
  title: string;
}) {
  return (
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <div className="flex items-center gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-brand/10 text-brand-ink">
          <Icon className="size-5" />
        </span>
        <h2 className="text-lg font-semibold">{title}</h2>
      </div>
      <div className="mt-5">{children}</div>
    </section>
  );
}

function Fact({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="rounded-lg border bg-background p-3">
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="mt-2 break-words text-sm">{children}</dd>
    </div>
  );
}

function SourceNote({ source, owner }: { source: string; owner: string }) {
  return (
    <p className="text-xs text-muted-foreground">
      Source: {source}. Owner: {owner}.
    </p>
  );
}

function ProviderState({ state }: { state: string }) {
  const normalized = state.toLowerCase();
  if (normalized === "ok" || normalized === "available") {
    return <Badge variant="success">Ready</Badge>;
  }
  if (normalized === "unauthorized") {
    return <Badge variant="warning">Sign-in needed</Badge>;
  }
  if (normalized === "unreachable") {
    return <Badge variant="destructive">Unavailable</Badge>;
  }
  return <Badge variant="muted">Not ready</Badge>;
}

function StateCard({ text }: { text: string }) {
  return (
    <div className="mt-4 rounded-xl border border-dashed p-8 text-center text-sm text-muted-foreground">
      {text}
    </div>
  );
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
