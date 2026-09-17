"use client";

import { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { PermissionsSection } from "../_components/permissions-section";
import { RuntimeStatusLine } from "../_components/runtime-status-line";

export default function PermissionsSettingsPage() {
  const runtime = useHarnessRuntime();
  return (
    <>
      <RuntimeStatusLine runtime={runtime} />
      <PermissionsSection runtime={runtime} />
    </>
  );
}
