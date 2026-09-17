"use client";

import type { ConnectionStatus } from "@stacklok-oss/mecatl-sdk";
import { useSyncExternalStore } from "react";
import { getHarnessClient, onHarnessClientReset } from "@/lib/harness/sdk";

/**
 * The SDK client's connection status, as React state.
 *
 * The SDK publishes one multicast store (`client.status`): `online` while
 * requests succeed, `reconnecting` while a durable watch is re-attaching
 * from its last cursor after a dropped stream, `unauthorized` after a 401,
 * `incompatible` when the daemon's API major is not one this SDK speaks,
 * `offline` / `connecting` for the heartbeat's own view. Nothing in Studio
 * read it before this hook — the reconnect was silent.
 *
 * The subscription follows the client: `resetHarnessClient()` (tests, between
 * cases) forgets the client, so a store captured once would go quiet on a
 * replacement client. The reset listener re-attaches to whichever client is
 * current. On the server there is no client, so the snapshot is "connecting".
 */

const SERVER_SNAPSHOT: ConnectionStatus = "connecting";

function subscribe(onChange: () => void): () => void {
  let release = getHarnessClient().status.subscribe(() => onChange());
  const offReset = onHarnessClientReset(() => {
    release();
    release = getHarnessClient().status.subscribe(() => onChange());
    onChange();
  });
  return () => {
    release();
    offReset();
  };
}

function getSnapshot(): ConnectionStatus {
  return getHarnessClient().status.getSnapshot();
}

export function useConnectionStatus(): ConnectionStatus {
  return useSyncExternalStore(subscribe, getSnapshot, () => SERVER_SNAPSHOT);
}
