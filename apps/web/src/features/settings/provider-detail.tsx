// SPDX-License-Identifier: Apache-2.0

import type { GetProviderSettingsResponse } from "@mecatl-studio/contracts/generated";
import { getProviderSettingsOptions, getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Badge } from "../../components/ui/badge";
import {
  connectionMessage,
  freshDeploymentQuery,
  useBrowserOnline,
  useRefreshOnEntry,
} from "./settings-connection";

/** Direct, reloadable detail for one exact caller-visible provider ID. */
export function ProviderDetail({ providerId }: { providerId: string }) {
  const browserOnline = useBrowserOnline();
  const runtime = useQuery({
    ...getRuntimeOptions(),
    ...freshDeploymentQuery,
    enabled: browserOnline,
  });
  const runtimeValidating = useRefreshOnEntry(
    `provider:${providerId}:runtime`,
    browserOnline,
    runtime.data !== undefined,
    runtime.refetch,
  );
  const detail = useQuery({
    ...getProviderSettingsOptions({ query: { providerId } }),
    ...freshDeploymentQuery,
    enabled:
      browserOnline &&
      !runtimeValidating &&
      !runtime.isFetching &&
      !runtime.isError &&
      runtime.data?.connection === "online",
  });
  const detailValidating = useRefreshOnEntry(
    `provider:${providerId}:detail`,
    browserOnline &&
      !runtimeValidating &&
      !runtime.isFetching &&
      runtime.data?.connection === "online",
    detail.data !== undefined,
    detail.refetch,
  );
  const connection =
    !runtimeValidating &&
    !runtime.isFetching &&
    runtime.data &&
    connectionMessage(runtime.data.connection);
  let content: ReactNode;
  if (!browserOnline) {
    content = <State text="Offline. Provider details are unavailable." />;
  } else if (runtimeValidating || runtime.isFetching || runtime.isPending) {
    content = <State text="Loading provider details…" />;
  } else if (connection) {
    content = <State text={connection} />;
  } else if (runtime.isError) {
    content = (
      <State text="Provider details could not be loaded. Check the connection and try again." />
    );
  } else if (detailValidating || detail.isFetching || detail.isPending) {
    content = <State text="Loading provider details…" />;
  } else if (detail.isError) {
    content = isProviderNotFound(detail.error) ? (
      <State text="Provider not found in the caller-visible inventory." />
    ) : isModelsUnsupported(detail.error) ? (
      <State text="Model providers are not available on this deployment." />
    ) : (
      <State text="Provider details could not be loaded. Check the connection and try again." />
    );
  } else {
    content = <ProviderFacts detail={detail.data} />;
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-4xl px-4 py-7 sm:px-8 sm:py-10">
        <Link
          className="inline-flex min-h-11 items-center rounded-lg text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-brand"
          params={{ section: "providers" }}
          search={{ item: undefined }}
          to="/workspace/settings/$section"
        >
          ← Providers
        </Link>
        <h1 className="mt-5 break-all text-3xl font-semibold tracking-tight">{providerId}</h1>
        <p className="mt-2 text-sm text-muted-foreground">
          Source: authenticated BFF provider detail. Configuration is managed by the deployment.
        </p>
        <div className="mt-7">{content}</div>
      </div>
    </div>
  );
}

function ProviderFacts({ detail }: { detail: GetProviderSettingsResponse }) {
  const { provider } = detail;
  return (
    <section className="rounded-2xl border bg-card p-5 sm:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold">Provider status</h2>
        <ProviderState state={provider.state} />
      </div>
      <dl className="mt-5 grid gap-3 sm:grid-cols-2">
        <Fact label="Provider ID">{provider.id}</Fact>
        <Fact label="Available models">{provider.modelCount} models</Fact>
        <Fact label="Status hint">{provider.hint || "Not available"}</Fact>
        <Fact label="Display endpoint">{detail.displayEndpoint || "Not available"}</Fact>
      </dl>
      <h3 className="mt-6 text-sm font-semibold">Models from this provider</h3>
      {detail.models.length === 0 ? (
        <p className="mt-2 text-sm text-muted-foreground">
          No models are available from this provider.
        </p>
      ) : (
        <ul className="mt-3 grid gap-3 sm:grid-cols-2">
          {detail.models.map((model) => (
            <li className="rounded-lg border bg-background p-3" key={model.id}>
              <p className="font-medium">{model.displayName}</p>
              <p className="break-all text-xs text-muted-foreground">{model.id}</p>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-5 text-xs text-muted-foreground">
        The endpoint is a sanitized display value from the daemon, not a connection setting.
      </p>
    </section>
  );
}

function Fact({ children, label }: { children: React.ReactNode; label: string }) {
  return (
    <div className="rounded-lg border bg-background p-3">
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="mt-2 break-words text-sm">{children}</dd>
    </div>
  );
}

function ProviderState({ state }: { state: string }) {
  const normalized = state.toLowerCase();
  if (normalized === "ok" || normalized === "available")
    return <Badge variant="success">Ready</Badge>;
  if (normalized === "unauthorized") return <Badge variant="warning">Sign-in needed</Badge>;
  if (normalized === "unreachable") return <Badge variant="destructive">Unavailable</Badge>;
  return <Badge variant="muted">Not ready</Badge>;
}

function State({ text }: { text: string }) {
  return (
    <div className="rounded-2xl border border-dashed p-8 text-center text-sm text-muted-foreground">
      {text}
    </div>
  );
}

function isProviderNotFound(error: unknown) {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === "provider_not_found"
  );
}

function isModelsUnsupported(error: unknown) {
  return (
    typeof error === "object" &&
    error !== null &&
    "code" in error &&
    error.code === "models_unsupported"
  );
}
