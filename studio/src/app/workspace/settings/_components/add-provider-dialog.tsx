"use client";

import { Check, Copy, Plus } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type {
  HarnessProviderInfo,
  KnownHarnessProvider,
} from "@/lib/harness/client";
import {
  CUSTOM_PROVIDER_API_FLAVORS,
  customProviderAuthSnippet,
  customProviderSettingsSnippet,
  providerOverrideSnippet,
  validCustomProviderBaseURL,
  validCustomProviderId,
} from "@/lib/provider-auth.mjs";

/** The synthetic Select value for the custom-gateway flow — double
 *  underscores keep it outside the daemon's provider-id grammar, so it can
 *  never collide with a real kind. */
const CUSTOM_KIND = "__custom__";

/** Human labels for the daemon's closed api_flavor enum (ADR 0238). */
const FLAVOR_LABELS: Record<string, string> = {
  "openai-responses": "OpenAI Responses",
  "openai-chat-completions": "OpenAI Chat Completions",
  "anthropic-messages": "Anthropic Messages",
};

/**
 * Guided provider add, with deliberately NO key input anywhere (Studio rule
 * 3: credentials never cross the browser/controller boundary): pick a kind,
 * copy the exact snippet — `<YOUR_KEY>` placeholder and all — into the file
 * on the daemon's machine, then Re-check reads the inventory back through
 * the controller and offers the restart that makes mecated see it.
 *
 * Two flows share that shape:
 * - a BUILT-IN kind copies one auth.yaml block;
 * - "Custom gateway" (ADR 0238: an operator-defined provider) collects the
 *   NON-secret definition — id, API flavor, base URL, default model, auth
 *   method — and emits the settings `providers:` block plus, for api_key
 *   auth, the auth.yaml key block. The id/URL/model fields hold identifiers,
 *   never credentials; the key still travels only by hand into auth.yaml.
 */
export function AddProviderDialog({
  known,
  configured,
  authFile,
  operatorSettings,
  reload,
  restartDaemon,
  restarting,
}: {
  known: KnownHarnessProvider[];
  configured: string[];
  /** The auth.yaml path on the controller's machine (from /status). */
  authFile: string;
  /** True when an imported operator-settings.yaml drives the daemon — its
   *  providers: section, if any, wins over the user-global settings file. */
  operatorSettings: boolean;
  /** Re-reads the inventory; resolves to the fresh rows. */
  reload: () => Promise<HarnessProviderInfo[]>;
  /** Restarts the daemon so the new block takes effect. */
  restartDaemon: () => Promise<void>;
  restarting: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState("");
  const [copied, setCopied] = useState("");
  const [checking, setChecking] = useState(false);
  const [checked, setChecked] = useState<"appeared" | "missing" | null>(null);

  // The custom-gateway definition (non-secret by construction).
  const [customId, setCustomId] = useState("");
  const [customFlavor, setCustomFlavor] = useState(
    CUSTOM_PROVIDER_API_FLAVORS[0],
  );
  const [customBaseURL, setCustomBaseURL] = useState("");
  const [customModel, setCustomModel] = useState("");
  const [customAuth, setCustomAuth] = useState<"api_key" | "none">("api_key");
  // Optional base-URL override for a BUILT-IN provider (provider_overrides).
  const [overrideURL, setOverrideURL] = useState("");

  const selected = known.find((provider) => provider.name === kind) ?? null;
  const isCustom = kind === CUSTOM_KIND;
  const alreadyConfigured = new Set(configured);
  const path = authFile || "~/.config/mecatl/auth.yaml";
  // settings.yaml lives beside auth.yaml under the same XDG rule, so the
  // guided-copy destination is derivable without another status field.
  const settingsPath = path.endsWith("auth.yaml")
    ? `${path.slice(0, -"auth.yaml".length)}settings.yaml`
    : "~/.config/mecatl/settings.yaml";

  const customIdValid = validCustomProviderId(customId);
  const customURLValid = validCustomProviderBaseURL(customBaseURL);
  const customModelValid = customModel.trim() !== "";
  const customComplete = customIdValid && customURLValid && customModelValid;
  const customTaken = customIdValid && alreadyConfigured.has(customId);
  const targetName = isCustom ? customId : (selected?.name ?? "");
  const targetLabel = isCustom
    ? customId || "the custom provider"
    : (selected?.label ?? "");

  const settingsSnippet = customComplete
    ? customProviderSettingsSnippet({
        id: customId,
        baseURL: customBaseURL.trim(),
        defaultModel: customModel.trim(),
        apiFlavor: customFlavor,
        authMethod: customAuth,
      })
    : "";
  const authSnippet = customComplete ? customProviderAuthSnippet(customId) : "";
  const overrideSnippet = selected
    ? providerOverrideSnippet(selected.name, overrideURL)
    : "";

  function handleOpenChange(next: boolean) {
    setOpen(next);
    if (next) {
      setKind("");
      setCopied("");
      setChecked(null);
      setCustomId("");
      setCustomFlavor(CUSTOM_PROVIDER_API_FLAVORS[0]);
      setCustomBaseURL("");
      setCustomModel("");
      setCustomAuth("api_key");
    }
  }

  async function copyText(which: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(which);
      setTimeout(() => setCopied(""), 2_000);
    } catch {
      // Clipboard unavailable — the block is selectable text either way.
    }
  }

  async function recheck() {
    if (!targetName) return;
    setChecking(true);
    try {
      const rows = await reload();
      setChecked(
        rows.some((row) => row.name === targetName) ? "appeared" : "missing",
      );
    } finally {
      setChecking(false);
    }
  }

  const showSteps = selected !== null || (isCustom && customComplete);

  return (
    <>
      <Button
        size="sm"
        variant="outline"
        className="rounded-full"
        onClick={() => handleOpenChange(true)}
      >
        <Plus className="size-4" />
        Add provider
      </Button>
      <Dialog open={open} onOpenChange={handleOpenChange}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
          <DialogHeader className="text-left">
            <DialogTitle>Add a provider</DialogTitle>
            <DialogDescription>
              Studio never handles API keys — you add the key to the
              daemon&rsquo;s own config file in three quick steps.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-5">
            <div className="flex flex-col gap-3">
              <Label htmlFor="add-provider-kind">1. Choose the provider</Label>
              {/* Controlled for its whole lifetime ("" shows the
                  placeholder) — `kind || undefined` would flip it from
                  uncontrolled to controlled on first pick, which Radix
                  rightly warns about. */}
              <Select
                value={kind}
                onValueChange={(next) => {
                  setKind(next);
                  setChecked(null);
                  setCopied("");
                }}
              >
                <SelectTrigger id="add-provider-kind" className="w-full">
                  <SelectValue placeholder="Choose a provider kind…" />
                </SelectTrigger>
                <SelectContent>
                  {known.map((provider) => (
                    <SelectItem
                      key={provider.name}
                      value={provider.name}
                      disabled={alreadyConfigured.has(provider.name)}
                    >
                      {provider.label}
                      {alreadyConfigured.has(provider.name) &&
                        " (already configured)"}
                    </SelectItem>
                  ))}
                  <SelectItem value={CUSTOM_KIND}>
                    Custom gateway (OpenAI/Anthropic-compatible)
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>

            {isCustom && (
              <div className="flex flex-col gap-3">
                <p className="text-sm font-medium">
                  Describe the gateway
                  <span className="block text-xs font-normal text-muted-foreground">
                    The definition is not a secret — it names the endpoint and
                    wire protocol. Any API key is still added by hand, in the
                    next step.
                  </span>
                </p>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="custom-provider-id">Provider id</Label>
                  <Input
                    id="custom-provider-id"
                    placeholder="my-gateway"
                    autoComplete="off"
                    spellCheck={false}
                    value={customId}
                    onChange={(event) => {
                      setCustomId(event.target.value.trim());
                      setChecked(null);
                    }}
                  />
                  {customId !== "" && !customIdValid && (
                    <p className="text-xs text-destructive">
                      Lower-case letters, digits, and hyphens (start with a
                      letter, max 63 characters); built-in names like
                      &ldquo;openai&rdquo; are reserved.
                    </p>
                  )}
                  {customTaken && (
                    <p className="text-xs text-destructive">
                      A provider named &ldquo;{customId}&rdquo; is already
                      configured.
                    </p>
                  )}
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="custom-provider-flavor">API flavor</Label>
                  <Select
                    value={customFlavor}
                    onValueChange={(next) => {
                      setCustomFlavor(next);
                      setChecked(null);
                    }}
                  >
                    <SelectTrigger
                      id="custom-provider-flavor"
                      className="w-full"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {CUSTOM_PROVIDER_API_FLAVORS.map((flavor: string) => (
                        <SelectItem key={flavor} value={flavor}>
                          {FLAVOR_LABELS[flavor] ?? flavor}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="custom-provider-url">Base URL</Label>
                  <Input
                    id="custom-provider-url"
                    placeholder="https://gateway.example.com/v1"
                    autoComplete="off"
                    spellCheck={false}
                    value={customBaseURL}
                    onChange={(event) => {
                      setCustomBaseURL(event.target.value);
                      setChecked(null);
                    }}
                  />
                  {customBaseURL !== "" && !customURLValid && (
                    <p className="text-xs text-destructive">
                      An HTTPS URL without credentials, query, or fragment.
                    </p>
                  )}
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="custom-provider-model">Default model</Label>
                  <Input
                    id="custom-provider-model"
                    placeholder="the model id the gateway serves"
                    autoComplete="off"
                    spellCheck={false}
                    value={customModel}
                    onChange={(event) => {
                      setCustomModel(event.target.value);
                      setChecked(null);
                    }}
                  />
                  <p className="text-xs text-muted-foreground">
                    Required by the daemon — the model used when a session does
                    not pick one.
                  </p>
                </div>
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="custom-provider-auth">Authentication</Label>
                  <Select
                    value={customAuth}
                    onValueChange={(next) => {
                      setCustomAuth(next === "none" ? "none" : "api_key");
                      setChecked(null);
                    }}
                  >
                    <SelectTrigger id="custom-provider-auth" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="api_key">
                        API key (added to auth.yaml by hand)
                      </SelectItem>
                      <SelectItem value="none">
                        None (the gateway needs no key)
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            )}

            {isCustom && customComplete && !customTaken && (
              <>
                <div className="flex flex-col gap-3">
                  <p className="text-sm font-medium">
                    2. Add this to the operator settings
                    <span className="block text-xs font-normal text-muted-foreground">
                      Paste the block into{" "}
                      <code className="font-mono">{settingsPath}</code> (merge
                      it under an existing{" "}
                      <code className="font-mono">providers:</code> key if one
                      exists).
                      {operatorSettings &&
                        " You are running with an imported operator settings file — if it already defines a providers: section, add the entry THERE instead: that file wins the whole section."}
                    </span>
                  </p>
                  <CopyableSnippet
                    text={settingsSnippet}
                    copied={copied === "settings"}
                    onCopy={() => void copyText("settings", settingsSnippet)}
                  />
                </div>
                {customAuth === "api_key" && (
                  <div className="flex flex-col gap-3">
                    <p className="text-sm font-medium">
                      3. Add the key to the credentials file
                      <span className="block text-xs font-normal text-muted-foreground">
                        Paste into <code className="font-mono">{path}</code>{" "}
                        under its <code className="font-mono">providers:</code>{" "}
                        key, and swap{" "}
                        <code className="font-mono">&lt;YOUR_KEY&gt;</code> for
                        your real key.
                      </span>
                    </p>
                    <CopyableSnippet
                      text={authSnippet}
                      copied={copied === "auth"}
                      onCopy={() => void copyText("auth", authSnippet)}
                    />
                  </div>
                )}
              </>
            )}

            {selected && (
              <div className="flex flex-col gap-3">
                <p className="text-sm font-medium">
                  2. Add this to the config file
                  <span className="block text-xs font-normal text-muted-foreground">
                    {selected.note} Paste the snippet into{" "}
                    <code className="font-mono">{path}</code> under its{" "}
                    <code className="font-mono">providers:</code> key, and swap{" "}
                    <code className="font-mono">&lt;YOUR_KEY&gt;</code> for your
                    real key.
                  </span>
                </p>
                <CopyableSnippet
                  text={selected.snippet}
                  copied={copied === "known"}
                  onCopy={() => void copyText("known", selected.snippet)}
                />
                {/* provider_overrides (ADR 0238): route this built-in through
                    a gateway/proxy without redefining it — optional, and a
                    settings.yaml (operator-tier) block, unlike the key. */}
                <div className="flex flex-col gap-3 pt-1">
                  <Label htmlFor="add-provider-override">
                    Route through a gateway
                    <span className="block text-xs font-normal text-muted-foreground">
                      Optional. A base-URL override sends {selected.label}
                      &nbsp;traffic through your proxy or gateway.
                    </span>
                  </Label>
                  <Input
                    id="add-provider-override"
                    value={overrideURL}
                    onChange={(event) => setOverrideURL(event.target.value)}
                    placeholder="https://gateway.example/v1"
                    autoComplete="off"
                    spellCheck={false}
                  />
                  {overrideSnippet && (
                    <>
                      <p className="text-xs text-muted-foreground">
                        Add to <code className="font-mono">{settingsPath}</code>
                        {operatorSettings &&
                          " — or the imported operator settings file, which wins the whole section"}
                        :
                      </p>
                      <CopyableSnippet
                        text={overrideSnippet}
                        copied={copied === "override"}
                        onCopy={() =>
                          void copyText("override", overrideSnippet)
                        }
                      />
                    </>
                  )}
                </div>
              </div>
            )}

            {showSteps &&
              !customTaken &&
              (checked === "appeared" ? (
                <p className="text-sm">
                  <span className="font-medium">{targetLabel}</span> found ✓ —
                  restart the daemon to start using it. In-flight runs end with
                  the restart.
                </p>
              ) : (
                <p className="text-sm font-medium">
                  {isCustom && customAuth === "api_key" ? "4" : "3"}. Save the
                  file{isCustom && customAuth === "api_key" ? "s" : ""}, then
                  Re-check
                  {checked === "missing" && (
                    <span className="block text-xs font-normal text-muted-foreground">
                      Not found yet — make sure the file is saved on the
                      daemon&rsquo;s machine, then try again.
                    </span>
                  )}
                </p>
              ))}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              className="rounded-full"
              onClick={() => handleOpenChange(false)}
            >
              Done
            </Button>
            {showSteps && !customTaken && checked !== "appeared" && (
              <Button
                variant="action"
                className="rounded-full"
                disabled={checking}
                onClick={() => void recheck()}
              >
                {checking ? "Checking…" : "Re-check"}
              </Button>
            )}
            {showSteps && checked === "appeared" && (
              <Button
                variant="action"
                disabled={restarting}
                onClick={async () => {
                  await restartDaemon();
                  setOpen(false);
                }}
              >
                {restarting ? "Restarting…" : "Restart daemon to apply"}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** One copyable, read-only snippet block (shared by both flows). */
function CopyableSnippet({
  text,
  copied,
  onCopy,
}: {
  text: string;
  copied: boolean;
  onCopy: () => void;
}) {
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-lg border bg-muted/40 p-3 pr-10 font-mono text-xs leading-relaxed">
        {text}
      </pre>
      <Button
        size="icon"
        variant="ghost"
        className="absolute top-1.5 right-1.5 size-7 text-muted-foreground hover:text-foreground"
        aria-label={copied ? "Copied" : "Copy snippet"}
        title={copied ? "Copied" : "Copy snippet"}
        onClick={onCopy}
      >
        {copied ? (
          <Check className="size-3.5" />
        ) : (
          <Copy className="size-3.5" />
        )}
      </Button>
    </div>
  );
}
