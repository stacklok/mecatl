// SPDX-License-Identifier: Apache-2.0

import type {
  GetRuntimeResponse,
  GetRuntimeSettingsResponse,
} from "@mecatl-studio/contracts/generated";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BookOpen, Copy, ExternalLink, Keyboard, LifeBuoy } from "lucide-react";
import { type ReactNode, useState } from "react";
import { AuthControl } from "@/components/shell/auth-control";
import { Button } from "@/components/ui/button";
import { writeClipboardText } from "@/lib/clipboard";
import { FactList, FactRow, SettingsCard } from "./settings-card";

/** Studio's own documentation page. */
const docsUrl = "https://mecatl.dev/docs/building/deployment/studio";

/** Where a problem with this app (not the connected agent) is reported. */
const supportUrl = "https://github.com/stacklok/mecatl/issues";

const actionClass = "min-h-11 rounded-full";

export function reported(value: string | undefined): string {
  return value?.trim() || "Not reported";
}

/** Only the BFF's safe build and runtime projections go into copied diagnostics. */
export function supportSummary(
  runtime: GetRuntimeResponse,
  settings: GetRuntimeSettingsResponse,
): string {
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

/** Studio's own identity: its build, the SDK it was built with, and the help entry points. */
export function AboutStudioCard({ runtime }: { runtime: GetRuntimeResponse }) {
  return (
    <SettingsCard title="About Mecatl Studio">
      <div className="flex flex-col gap-3">
        <FactList>
          <FactRow label="Studio build">{reported(runtime.studioBuildId)}</FactRow>
          <FactRow label="SDK version">{reported(runtime.sdkVersion)}</FactRow>
        </FactList>
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild className={actionClass} size="sm" variant="outline">
            <a href={docsUrl} rel="noreferrer" target="_blank">
              <BookOpen aria-hidden="true" className="size-4" />
              Documentation
              <ExternalLink aria-hidden="true" className="size-3 text-muted-foreground" />
            </a>
          </Button>
          <Button asChild className={actionClass} size="sm" variant="outline">
            <a href={supportUrl} rel="noreferrer" target="_blank">
              <LifeBuoy aria-hidden="true" className="size-4" />
              Report a problem
              <ExternalLink aria-hidden="true" className="size-3 text-muted-foreground" />
            </a>
          </Button>
          <Button asChild className={actionClass} size="sm" variant="outline">
            <Link to="/workspace/shortcuts">
              <Keyboard aria-hidden="true" className="size-4" />
              Keyboard shortcuts
            </Link>
          </Button>
        </div>
      </div>
    </SettingsCard>
  );
}

/** The connected daemon's facts, shared by About and Diagnostics. */
export function DaemonFacts({
  children,
  runtime,
  settings,
}: {
  children?: ReactNode;
  runtime: GetRuntimeResponse;
  settings: GetRuntimeSettingsResponse;
}) {
  return (
    <FactList>
      <FactRow label="Daemon build">{reported(settings.buildId)}</FactRow>
      <FactRow label="Daemon implementation">{reported(settings.serverImplementation)}</FactRow>
      <FactRow label="Runtime source">{runtime.source}</FactRow>
      <FactRow label="Connection">{runtime.connection}</FactRow>
      {children}
    </FactList>
  );
}

/** The connected agent's identity, with one action that copies every safe fact for support. */
export function AboutDaemonCard({
  runtime,
  settings,
}: {
  runtime: GetRuntimeResponse;
  settings: GetRuntimeSettingsResponse;
}) {
  const [copyStatus, setCopyStatus] = useState("");
  return (
    <SettingsCard title="About the agent">
      <div className="flex flex-col gap-3">
        <DaemonFacts runtime={runtime} settings={settings}>
          <FactRow label="Deployment">{reported(runtime.deployment)}</FactRow>
        </DaemonFacts>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            className={actionClass}
            onClick={async () => {
              const outcome = await writeClipboardText(supportSummary(runtime, settings));
              setCopyStatus(
                outcome.ok ? "Support summary copied." : "Could not copy the support summary.",
              );
            }}
            size="sm"
            type="button"
            variant="outline"
          >
            <Copy aria-hidden="true" className="size-4" />
            Copy support summary
          </Button>
        </div>
        <p className="text-xs text-muted-foreground" role="status">
          {copyStatus || "Include these details when you report a problem."}
        </p>
      </div>
    </SettingsCard>
  );
}

/** Sign-in status and sign-out, as its own card once a person is signed in. */
export function SignInCard() {
  const session = useQuery(getAuthSessionOptions());
  if (session.data?.status !== "authenticated") return null;
  return (
    <SettingsCard title="Sign-in">
      <AuthControl />
    </SettingsCard>
  );
}
