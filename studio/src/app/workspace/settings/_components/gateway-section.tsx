"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  RESTART_SENTENCE,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;

/** A suggestion only — the field is fully editable. */
const SUGGESTED_GATEWAY_URL = "https://connector-gateway.stacklok.dev/gw/mcp";

/**
 * Connects an MCP gateway by name and address through its sign-in flow.
 * The address is validated by the controller (HTTPS only, no credentials in
 * the URL), and its errors are surfaced verbatim through the shared runtime
 * error. Connecting restarts the daemon; a failed handshake rolls the
 * previous gateway back.
 */
export function GatewaySection({ runtime }: { runtime: Runtime }) {
  const [name, setName] = useState("");
  const [url, setUrl] = useState(SUGGESTED_GATEWAY_URL);

  const gateway = runtime.status?.gateway ?? null;
  const busy = runtime.busy === "gateway";
  const ready = Boolean(name.trim() && url.trim());

  const startOAuth = () => {
    if (!ready || busy) return;
    // Opened synchronously: a popup created after an await is blocked.
    const popup = window.open(
      "about:blank",
      "mecatl-gateway-oauth",
      "width=520,height=680",
    );
    if (!popup) return;
    void runtime.connectGatewayOAuth(name.trim(), url.trim(), {
      setUrl: (target) => {
        popup.location.href = target;
      },
      isClosed: () => popup.closed,
    });
  };

  const connectedRow = gateway ? (
    <SettingsRow label="Connected to">
      <div className="min-w-0 text-right">
        <p className="text-sm font-medium">{gateway.name}</p>
        <p className="max-w-72 break-all text-xs text-muted-foreground">
          {gateway.url}
        </p>
      </div>
    </SettingsRow>
  ) : null;

  return (
    <SettingsCard
      title="MCP gateway"
      description="Sign in to a gateway to give the agent its tools."
    >
      {!runtime.live ? (
        <OfflineNote />
      ) : runtime.mode === "external" ? (
        <div className="flex flex-col gap-4">
          {connectedRow && (
            <div className="divide-y divide-border/60">{connectedRow}</div>
          )}
          <ExternalManagedNote />
        </div>
      ) : (
        // A plain stacked form (labels above full-width fields, no dividers)
        // — this card is one configure-then-connect action, not a row list.
        <div className="flex flex-col gap-4">
          {connectedRow}
          <div className="flex flex-col gap-3">
            <Label htmlFor="gw-name">Gateway name</Label>
            <Input
              id="gw-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="connector-gateway"
            />
          </div>
          <div className="flex flex-col gap-3">
            <Label htmlFor="gw-url">Gateway address</Label>
            <Input
              id="gw-url"
              value={url}
              onChange={(event) => setUrl(event.target.value)}
              placeholder={SUGGESTED_GATEWAY_URL}
            />
          </div>
          <Note>{RESTART_SENTENCE}</Note>
          <Button
            type="button"
            variant="action"
            className="self-start rounded-full"
            disabled={!ready || busy}
            onClick={startOAuth}
          >
            {busy ? "Waiting for sign-in…" : "Sign in to gateway"}
          </Button>
        </div>
      )}
    </SettingsCard>
  );
}
