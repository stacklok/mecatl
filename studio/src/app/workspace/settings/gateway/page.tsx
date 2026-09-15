"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { GatewaySection } from "../_components/gateway-section";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

export default function GatewaySettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <GatewaySection runtime={runtime} />
    </>
  );
}
