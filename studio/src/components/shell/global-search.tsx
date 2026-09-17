"use client";

import { Search } from "lucide-react";
import { useRouter } from "next/navigation";
import { useCallback, useMemo, useState } from "react";
import {
  ATRIUM_SEARCH_GROUPS,
  buildAtriumSearchEntries,
  WORKSPACE_PAGE_ENTRIES,
} from "@/components/shell/atrium-search-data";
import { createStaticSearchProvider } from "@/components/shell/search-static";
import type { SearchEntry } from "@/components/shell/search-types";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  useAgentCron,
  useAgentMemory,
  useAgentSessions,
} from "@/features/agent";
import { useAgentSkills } from "@/features/agent/hooks/use-agent-skills";
import { useShortcutBindings } from "@/lib/shortcuts/keymap";
import { keycaps } from "@/lib/shortcuts/registry";
import { useShortcut } from "@/lib/shortcuts/use-shortcuts";
import { useThreadSessionIds } from "@/lib/thread-map";
import { cn } from "@/lib/utils";

/** Atrium workspace results. */
const ALL_GROUPS = [...ATRIUM_SEARCH_GROUPS];

/**
 * The global nav search in the shell topbar. Opens a ⌘/Ctrl-K command palette
 * (built on cmdk) whose filtering is delegated to a `SearchProvider` — an
 * in-memory index over the live daemon data (sessions, schedules, skills,
 * memory keys), rebuilt whenever that data changes — so cmdk's own fuzzy
 * filter is turned off (`shouldFilter={false}`). Transcripts are not indexed:
 * only titles and metadata match. Selecting a result routes to it.
 */
export function GlobalSearch() {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");

  const { sessions, runs } = useAgentSessions();
  const { jobs } = useAgentCron();
  const { skills } = useAgentSkills();
  const { entries: memories } = useAgentMemory();
  // Thread-backing sessions stay out of search, mirroring the chat sidebar:
  // a thread's only entry point is the reply indicator on its parent message.
  const threadSessionIds = useThreadSessionIds();

  const provider = useMemo(
    () =>
      createStaticSearchProvider([
        ...buildAtriumSearchEntries({
          sessions: sessions.filter((s) => !threadSessionIds.has(s.id)),
          runs,
          jobs,
          skills,
          memories,
        }),
        // The static help pages (Help & about, the shortcuts reference).
        ...WORKSPACE_PAGE_ENTRIES,
      ]),
    [sessions, runs, jobs, skills, memories, threadSessionIds],
  );

  // App-wide shortcuts, wired through the central dispatcher (this component
  // is mounted in the shell topbar, so they live on every workspace page):
  // ⌘K toggles the palette; `?` and ⌘/ open the keyboard-shortcuts reference
  // (⌘/ also works while typing); ⌘, opens settings.
  useShortcut("search.open", () => setOpen((prev) => !prev));
  useShortcut("shortcuts.open", () => router.push("/workspace/shortcuts"));
  useShortcut("shortcuts.open.mod", () => router.push("/workspace/shortcuts"));
  useShortcut("settings.open", () => router.push("/workspace/settings"));
  // The trigger's hint shows the EFFECTIVE search chord, so a remapped ⌘K
  // (Settings → Keyboard) never advertises a key that no longer opens it.
  const { bindings } = useShortcutBindings();
  const searchHint = keycaps(
    bindings.find((b) => b.id === "search.open")?.effectiveCombo ?? "mod+k",
  );

  const results = useMemo(() => provider.query(query), [provider, query]);
  const byCategory = useMemo(() => {
    const map = new Map<string, { entry: SearchEntry; snippet?: string }[]>();
    for (const r of results) {
      const list = map.get(r.entry.category) ?? [];
      list.push(r);
      map.set(r.entry.category, list);
    }
    return map;
  }, [results]);

  const handleSelect = useCallback(
    (entry: SearchEntry) => {
      setOpen(false);
      router.push(entry.href);
    },
    [router],
  );

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label="Search"
        className={cn(
          "flex h-9 items-center gap-2 rounded-md border border-border bg-background text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          // Icon-only on narrow screens; a full search field from `sm` up.
          "size-9 justify-center px-0 min-[500px]:w-64 min-[500px]:justify-start min-[500px]:px-3",
        )}
      >
        <Search className="size-4 shrink-0" />
        <span className="hidden flex-1 text-left text-sm min-[500px]:inline">
          Search…
        </span>
        <kbd className="pointer-events-none hidden select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[0.65rem] font-medium text-muted-foreground min-[500px]:inline-flex">
          {searchHint.map((cap) =>
            cap.length === 1 && "⌘⇧⌥".includes(cap) ? (
              <span key={cap} className="text-xs">
                {cap}
              </span>
            ) : (
              <span key={cap}>{cap}</span>
            ),
          )}
        </kbd>
      </button>

      <CommandDialog
        open={open}
        onOpenChange={setOpen}
        title="Global search"
        description="Search chats, memory, skills, connectors, and more"
        shouldFilter={false}
        // Below the mobile breakpoint the palette takes the whole screen:
        // the on-screen keyboard eats half the viewport, so a floating
        // dialog leaves no room for results.
        className="max-[499px]:inset-0 max-[499px]:top-0 max-[499px]:left-0 max-[499px]:h-dvh max-[499px]:max-h-none max-[499px]:w-full max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:translate-y-0 max-[499px]:rounded-none max-[499px]:border-0 max-[499px]:[&>[data-slot=command]]:h-full"
      >
        <CommandInput
          placeholder="Search…"
          // pr-8 keeps typed text clear of the dialog's floating close button.
          className="pr-8"
          value={query}
          onValueChange={setQuery}
        />
        <CommandList className="max-[499px]:max-h-none max-[499px]:flex-1">
          <CommandEmpty>No results found.</CommandEmpty>
          {ALL_GROUPS.map(({ category, heading }) => {
            const hits = byCategory.get(category);
            if (!hits || hits.length === 0) return null;
            return (
              <CommandGroup key={category} heading={heading}>
                {hits.map(({ entry, snippet }) => {
                  const Icon = entry.icon;
                  return (
                    <CommandItem
                      key={entry.id}
                      value={entry.id}
                      onSelect={() => handleSelect(entry)}
                    >
                      <Icon className="size-4 shrink-0" />
                      <div className="flex min-w-0 flex-col">
                        <span className="truncate text-sm">{entry.title}</span>
                        <span className="truncate text-xs text-muted-foreground">
                          {snippet ?? entry.subtitle}
                        </span>
                      </div>
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            );
          })}
        </CommandList>
      </CommandDialog>
    </>
  );
}
