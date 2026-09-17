"use client";

import { Loader2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { isNoMcpProvider, NO_MCP_PROVIDER_TEXT } from "@/lib/harness/mcp";
import { isUnsupportedByDaemon } from "@/lib/harness/sdk";

/**
 * What the composer's two MCP pickers (prompts, resources) share: the
 * on-open listing lifecycle with its abort, the daemon-error wording, the
 * server filter and the type-to-filter input. Both pickers are admitted by
 * `capabilities.mcp` only — the caller gates; nothing here fetches unless
 * opened.
 */

/** The server filter's "every server" value (Radix Select refuses ""). */
export const ALL_SERVERS = "__all__";

/** The label every picker's insert action carries (the TUI's Choose). */
export const INSERT_INTO_MESSAGE = "Insert into message";

/**
 * The user-facing sentence for a failed MCP read: the daemon's
 * `no_mcp_provider` refusal is a plain notice, an older daemon that lacks
 * the route reads `unsupportedText`, anything else is its own message.
 */
export function mcpPickerErrorText(
  error: unknown,
  unsupportedText: string,
): string {
  if (isNoMcpProvider(error)) return NO_MCP_PROVIDER_TEXT;
  if (isUnsupportedByDaemon(error)) return unsupportedText;
  return error instanceof Error ? error.message : String(error);
}

export type PickerListing<T> =
  | { status: "loading" }
  | { status: "ready"; items: T[] }
  | { status: "error"; message: string };

/**
 * Loads a listing every time the picker OPENS (a re-open re-reads, since
 * servers may have changed), aborts a read left behind by a close or unmount,
 * and words a failure through `mcpPickerErrorText`.
 */
export function useMcpPickerListing<T>(
  open: boolean,
  load: (signal: AbortSignal) => Promise<T[]>,
  unsupportedText: string,
): PickerListing<T> {
  const [listing, setListing] = useState<PickerListing<T>>({
    status: "loading",
  });
  // The loader is read through a ref so an inline lambda never re-triggers
  // the read; only the open flip does.
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setListing({ status: "loading" });
    loadRef
      .current(controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setListing({ status: "ready", items });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setListing({
          status: "error",
          message: mcpPickerErrorText(error, unsupportedText),
        });
      });
    return () => controller.abort();
  }, [open, unsupportedText]);

  return listing;
}

/** The distinct, non-empty server names of a listing, in first-seen order. */
export function distinctServers(
  items: readonly { server: string }[],
): string[] {
  const out: string[] = [];
  for (const item of items) {
    if (item.server && !out.includes(item.server)) out.push(item.server);
  }
  return out;
}

/** Case-insensitive substring match of `query` against any of `fields`. */
export function matchesQuery(
  query: string,
  fields: readonly string[],
): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  return fields.some((field) => field.toLowerCase().includes(needle));
}

/**
 * The filter row above a picker's list: type-to-filter, and — only when the
 * listing spans more than one server — a server Select.
 */
export function PickerFilters({
  idPrefix,
  query,
  onQueryChange,
  queryLabel,
  servers,
  server,
  onServerChange,
}: {
  idPrefix: string;
  query: string;
  onQueryChange: (query: string) => void;
  queryLabel: string;
  servers: string[];
  server: string;
  onServerChange: (server: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2 sm:flex-row">
      <div className="flex-1">
        <Label htmlFor={`${idPrefix}-filter`} className="sr-only">
          {queryLabel}
        </Label>
        <Input
          id={`${idPrefix}-filter`}
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder={queryLabel}
          autoComplete="off"
        />
      </div>
      {servers.length > 1 && (
        <div className="sm:w-56">
          <Label htmlFor={`${idPrefix}-server`} className="sr-only">
            Server
          </Label>
          <Select value={server} onValueChange={onServerChange}>
            <SelectTrigger id={`${idPrefix}-server`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_SERVERS}>All servers</SelectItem>
              {servers.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
    </div>
  );
}

/** The picker's "reading…" line. */
export function PickerBusy({ children }: { children: React.ReactNode }) {
  return (
    <p
      className="flex items-center gap-2 text-sm text-muted-foreground"
      role="status"
    >
      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
      {children}
    </p>
  );
}
