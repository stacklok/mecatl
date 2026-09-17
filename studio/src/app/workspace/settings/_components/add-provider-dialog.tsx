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
import { RESTART_SENTENCE } from "./settings-card";

/**
 * Guided provider add, with deliberately NO key input anywhere (Studio rule
 * 3: credentials never cross the browser/controller boundary): pick a
 * provider, copy the exact snippet — `<YOUR_KEY>` placeholder and all —
 * into the agent's own key file, then Re-check reads the inventory back
 * through the server and offers the restart that makes the agent see it.
 */
export function AddProviderDialog({
  known,
  configured,
  authFile,
  reload,
  restartDaemon,
  restarting,
}: {
  known: KnownHarnessProvider[];
  configured: string[];
  /** The key file's path on the agent's machine (from /status). */
  authFile: string;
  /** Re-reads the inventory; resolves to the fresh rows. */
  reload: () => Promise<HarnessProviderInfo[]>;
  /** Restarts the agent so the new key takes effect. */
  restartDaemon: () => Promise<void>;
  restarting: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState("");
  const [copied, setCopied] = useState(false);
  const [checking, setChecking] = useState(false);
  const [checked, setChecked] = useState<"appeared" | "missing" | null>(null);

  const selected = known.find((provider) => provider.name === kind) ?? null;
  const alreadyConfigured = new Set(configured);

  function handleOpenChange(next: boolean) {
    setOpen(next);
    if (next) {
      setKind("");
      setCopied(false);
      setChecked(null);
    }
  }

  async function copySnippet(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 2_000);
    } catch {
      // Clipboard unavailable — the block is selectable text either way.
    }
  }

  async function recheck() {
    if (!selected) return;
    setChecking(true);
    try {
      const rows = await reload();
      setChecked(
        rows.some((row) => row.name === selected.name && row.keyPresent)
          ? "appeared"
          : "missing",
      );
    } finally {
      setChecking(false);
    }
  }

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
              Studio never sees your key. You add it to the agent&rsquo;s own
              key file in three quick steps.
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
                  setCopied(false);
                }}
              >
                <SelectTrigger id="add-provider-kind" className="w-full">
                  <SelectValue placeholder="Choose a provider…" />
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
                        " (already added)"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {selected && (
              <div className="flex flex-col gap-3">
                <p className="text-sm font-medium">
                  2. Add this to the agent&rsquo;s key file
                  <span className="block text-xs font-normal text-muted-foreground">
                    {selected.note} Paste the snippet into{" "}
                    {authFile ? (
                      <code className="font-mono">{authFile}</code>
                    ) : (
                      "the agent’s key file (the person who set up the agent knows where it is)"
                    )}{" "}
                    under <code className="font-mono">providers:</code>, and
                    replace <code className="font-mono">&lt;YOUR_KEY&gt;</code>{" "}
                    with your key.
                  </span>
                </p>
                <CopyableSnippet
                  text={selected.snippet}
                  copied={copied}
                  onCopy={() => void copySnippet(selected.snippet)}
                />
              </div>
            )}

            {selected &&
              (checked === "appeared" ? (
                <p className="text-sm">
                  <span className="font-medium">{selected.label}</span> found.{" "}
                  {RESTART_SENTENCE}
                </p>
              ) : (
                <p className="text-sm font-medium">
                  3. Save the file, then re-check
                  {checked === "missing" && (
                    <span className="block text-xs font-normal text-muted-foreground">
                      Not found yet. Save the file on the agent&rsquo;s
                      computer, then try again.
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
            {selected && checked !== "appeared" && (
              <Button
                variant="action"
                className="rounded-full"
                disabled={checking}
                onClick={() => void recheck()}
              >
                {checking ? "Checking…" : "Re-check"}
              </Button>
            )}
            {selected && checked === "appeared" && (
              <Button
                variant="action"
                disabled={restarting}
                onClick={async () => {
                  await restartDaemon();
                  setOpen(false);
                }}
              >
                {restarting ? "Restarting…" : "Save and restart"}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** One copyable, read-only snippet block. */
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
