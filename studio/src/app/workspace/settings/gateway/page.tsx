"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { GatewaySection } from "../_components/gateway-section";
import { McpSourcesCard } from "../_components/mcp-sources-card";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

export default function GatewaySettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <McpSourcesCard />
      <GatewaySection runtime={runtime} />
    </>
  );
}
