// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";
import { connectionBannerMessages, connectionBannerState } from "./connection-status-banner-state";

/**
 * A thin banner shown above the top nav only when the Mecatl connection has
 * a problem — never a persistent status chip in the nav itself (Studio's
 * "the top nav carries no status chips" rule). Renders nothing while the
 * connection is healthy or still resolving.
 */
export function ConnectionStatusBanner() {
  const runtime = useQuery(getRuntimeOptions());
  const state = connectionBannerState(runtime);

  if (state === "hidden") return null;

  return (
    <div
      className="flex shrink-0 items-center gap-2 bg-warning/90 px-4 py-1.5 text-xs font-medium text-black"
      role="status"
    >
      <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" />
      <span>{connectionBannerMessages[state.kind]}</span>
    </div>
  );
}
