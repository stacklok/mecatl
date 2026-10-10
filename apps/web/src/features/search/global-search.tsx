// SPDX-License-Identifier: Apache-2.0

import { type GetAuthSessionResponse, getAuthSession } from "@mecatl-studio/contracts/generated";
import {
  getAuthSessionOptions,
  listConfiguredSkillsOptions,
  listLearnedSkillsOptions,
  listSchedulesOptions,
  listSessionsOptions,
  listUserMemoryOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  Brain,
  CalendarClock,
  Compass,
  GraduationCap,
  LoaderCircle,
  MessageCircle,
  Search,
  X,
} from "lucide-react";
import {
  type CSSProperties,
  type KeyboardEvent,
  type MouseEvent,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { DialogClose } from "@/components/ui/dialog";
import { useThreadSessionIds } from "@/features/chat/thread-map";
import { useShortcut, useShortcutSuppression } from "@/features/shortcuts/shortcut-provider";
import { keycaps, type ShortcutId } from "@/features/shortcuts/shortcut-registry";
import { cn } from "@/lib/utils";
import {
  buildGlobalSearchIndex,
  createGlobalSearchProvider,
  type GlobalSearchItem,
  type GlobalSearchTarget,
  globalSearchPages,
  groupSearchResults,
} from "./search-index";
import { createSearchCompositionGuard } from "./search-keyboard";

/**
 * The only app-wide shortcuts that stay live while the palette is open: its
 * own toggle, and the two navigation shortcuts, whose handlers below close the
 * palette before navigating. Everything else (Escape, arrow keys, chat
 * bindings) is suppressed so it cannot act on the page behind the dialog.
 */
const paletteShortcuts: readonly ShortcutId[] = [
  "search.open",
  "settings.open",
  "shortcuts.open",
  "shortcuts.open.mod",
];

/**
 * The keys cmdk's list handler acts on (its vim bindings are off). An input
 * method editor keeps the ones it owns, and Home and End stay caret keys in
 * the text field, so those never reach the list.
 */
const listKeys: ReadonlySet<string> = new Set(["ArrowDown", "ArrowUp", "End", "Enter", "Home"]);

// Generated query functions read only queryKey[0]. React Query may use a
// trailing account key for cache partitioning without changing the request.
function accountQueryKey<T extends readonly [unknown]>(queryKey: T, scope: string): T {
  return [...queryKey, scope] as unknown as T;
}

function isUnauthorized(error: unknown): boolean {
  if (!error || typeof error !== "object") return false;
  if ("status" in error && error.status === 401) return true;
  if ("response" in error && error.response && typeof error.response === "object") {
    return "status" in error.response && error.response.status === 401;
  }
  return false;
}

function searchScope(session: GetAuthSessionResponse | undefined): string | undefined {
  if (session?.status === "authenticated") {
    return session.account ? `account:${session.account}` : "static-help:account-unavailable";
  }
  if (session?.status === "disabled") {
    return `shared:${session.mode}`;
  }
  return undefined;
}

export function GlobalSearch() {
  const auth = useQuery(getAuthSessionOptions());
  const staticOnly = auth.data?.status === "authenticated" && !auth.data.account;
  const scope = searchScope(auth.data);

  // A changed account remounts the palette closed. Its inventory queries get
  // separate keys, so React Query never offers the prior account's cache.
  if (!scope) return null;
  return <SearchPalette inventoryAuthorized={!staticOnly} key={scope} scope={scope} />;
}

function SearchPalette({
  inventoryAuthorized,
  scope,
}: {
  inventoryAuthorized: boolean;
  scope: string;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [checkingAuth, setCheckingAuth] = useState(false);
  const [staticHelpOnly, setStaticHelpOnly] = useState(false);
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const invokingElement = useRef<HTMLElement | null>(null);
  const navigating = useRef(false);
  const navigationDone = useRef(false);
  const dialogClosed = useRef(false);
  const opening = useRef(false);
  const mounted = useRef(true);
  const composition = useRef(createSearchCompositionGuard());
  const [visualViewport, setVisualViewport] = useState<{ height: number; top: number } | null>(
    null,
  );
  // Search is revalidated on every open, including at the HTTP cache layer.
  const sessionsOptions = listSessionsOptions({ cache: "no-store" });
  const schedulesOptions = listSchedulesOptions({ cache: "no-store" });
  const configuredSkillsOptions = listConfiguredSkillsOptions({ cache: "no-store" });
  const learnedSkillsOptions = listLearnedSkillsOptions({ cache: "no-store" });
  const memoryOptions = listUserMemoryOptions({ cache: "no-store" });
  const sessionsKey = accountQueryKey(sessionsOptions.queryKey, scope);
  const schedulesKey = accountQueryKey(schedulesOptions.queryKey, scope);
  const configuredSkillsKey = accountQueryKey(configuredSkillsOptions.queryKey, scope);
  const learnedSkillsKey = accountQueryKey(learnedSkillsOptions.queryKey, scope);
  const memoryKey = accountQueryKey(memoryOptions.queryKey, scope);
  const canSearchInventory = inventoryAuthorized && !staticHelpOnly;
  const accountKeys = useRef([
    sessionsKey,
    schedulesKey,
    configuredSkillsKey,
    learnedSkillsKey,
    memoryKey,
  ]);
  const sessions = useQuery({
    ...sessionsOptions,
    enabled: open && canSearchInventory,
    queryKey: sessionsKey,
    staleTime: 0,
  });
  const schedules = useQuery({
    ...schedulesOptions,
    enabled: open && canSearchInventory,
    queryKey: schedulesKey,
    staleTime: 0,
  });
  const configuredSkills = useQuery({
    ...configuredSkillsOptions,
    enabled: open && canSearchInventory,
    queryKey: configuredSkillsKey,
    staleTime: 0,
  });
  const learnedSkills = useQuery({
    ...learnedSkillsOptions,
    enabled: open && canSearchInventory,
    queryKey: learnedSkillsKey,
    staleTime: 0,
  });
  const memory = useQuery({
    ...memoryOptions,
    enabled: open && canSearchInventory,
    queryKey: memoryKey,
    staleTime: 0,
  });
  const threadSessionIds = useThreadSessionIds();
  const inventories = [sessions, schedules, configuredSkills, learnedSkills, memory];
  const accessExpired =
    inventoryAuthorized && inventories.some((inventory) => isUnauthorized(inventory.error));
  const paletteOpen = open && !accessExpired;

  // React Query retains same-account data across closes. An inventory is
  // searchable only after this open's request has finished successfully.
  const index = useMemo(
    () =>
      canSearchInventory
        ? buildGlobalSearchIndex({
            configuredSkills:
              !configuredSkills.isError &&
              !configuredSkills.isFetching &&
              configuredSkills.data?.supported
                ? configuredSkills.data.items
                : [],
            learnedSkills:
              !learnedSkills.isError && !learnedSkills.isFetching && learnedSkills.data?.supported
                ? learnedSkills.data.items
                : [],
            memory:
              !memory.isError && !memory.isFetching && memory.data?.supported
                ? memory.data.items
                : [],
            schedules:
              !schedules.isError && !schedules.isFetching && schedules.data?.supported
                ? schedules.data.items
                : [],
            sessions: (sessions.isError || sessions.isFetching
              ? []
              : (sessions.data?.items ?? [])
            ).filter((session) => !threadSessionIds.has(session.id)),
          })
        : globalSearchPages,
    [
      canSearchInventory,
      configuredSkills.data,
      configuredSkills.isError,
      configuredSkills.isFetching,
      learnedSkills.data,
      learnedSkills.isError,
      learnedSkills.isFetching,
      memory.data,
      memory.isError,
      memory.isFetching,
      schedules.data,
      schedules.isError,
      schedules.isFetching,
      sessions.data,
      sessions.isError,
      sessions.isFetching,
      threadSessionIds,
    ],
  );
  const provider = useMemo(() => createGlobalSearchProvider(index), [index]);
  const results = useMemo(
    () => provider.query(query).map((result) => result.entry),
    [provider, query],
  );
  const groups = useMemo(() => groupSearchResults(results), [results]);
  const resultCount = results.length;
  const loading =
    canSearchInventory &&
    inventories.some((inventory) => inventory.isPending || inventory.isFetching);
  const partialError = canSearchInventory && inventories.some((inventory) => inventory.isError);
  const mac = navigator.platform.includes("Mac");

  useEffect(() => {
    if (!open || !window.visualViewport) return;
    const viewport = window.visualViewport;
    const update = () => setVisualViewport({ height: viewport.height, top: viewport.offsetTop });
    update();
    viewport.addEventListener("resize", update);
    viewport.addEventListener("scroll", update);
    return () => {
      viewport.removeEventListener("resize", update);
      viewport.removeEventListener("scroll", update);
    };
  }, [open]);
  const viewportStyle = visualViewport
    ? ({
        "--search-viewport-height": `${visualViewport.height}px`,
        "--search-viewport-top": `${visualViewport.top}px`,
      } as CSSProperties)
    : undefined;

  // Queries outlive a dialog close for quick reopening by the same account,
  // but never outlive this keyed palette's account scope.
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      for (const queryKey of accountKeys.current)
        queryClient.removeQueries({ exact: true, queryKey });
    };
  }, [queryClient]);

  useEffect(() => {
    if (!accessExpired) return;
    setOpen(false);
    setQuery("");
    // A 401 may arrive after other inventories succeeded. Drop every scoped
    // result before a fresh identity check can reopen this palette.
    for (const queryKey of accountKeys.current)
      queryClient.removeQueries({ exact: true, queryKey });
    void queryClient.invalidateQueries({ queryKey: getAuthSessionOptions().queryKey });
  }, [accessExpired, queryClient]);

  async function openSearch(invoker: HTMLElement | null) {
    if (opening.current) return;
    opening.current = true;
    setCheckingAuth(true);
    const authKey = getAuthSessionOptions().queryKey;
    const showSearch = () => {
      invokingElement.current = invoker;
      navigating.current = false;
      navigationDone.current = false;
      dialogClosed.current = false;
      setOpen(true);
    };
    try {
      // This is a new BFF request, independent of React Query's cached or
      // in-flight auth query. A different tab may have changed the shared
      // cookie since this tab last rendered its search trigger.
      const { data } = await getAuthSession({ cache: "no-store", throwOnError: true });
      if (!mounted.current) return;
      if (searchScope(queryClient.getQueryData<GetAuthSessionResponse>(authKey)) !== scope) return;
      queryClient.setQueryData(authKey, data);
      if (searchScope(data) !== scope) return;
      setStaticHelpOnly(false);
      showSearch();
    } catch (error) {
      // A lost BFF connection still permits local help search. A rejected
      // session does not: leave sign-in recovery to the auth gate.
      if (!mounted.current) return;
      if (searchScope(queryClient.getQueryData<GetAuthSessionResponse>(authKey)) !== scope) return;
      if (isUnauthorized(error)) {
        void queryClient.invalidateQueries({ exact: true, queryKey: authKey });
        return;
      }
      setStaticHelpOnly(true);
      showSearch();
    } finally {
      opening.current = false;
      if (mounted.current) setCheckingAuth(false);
    }
  }

  function closeSearch() {
    setOpen(false);
    setQuery("");
  }

  function focusNavigatedRoute() {
    if (!navigating.current || !navigationDone.current || !dialogClosed.current) return;
    requestAnimationFrame(() => {
      const main = document.querySelector<HTMLElement>("main");
      const target = main?.querySelector<HTMLElement>("h1") ?? main;
      if (target) {
        target.tabIndex = -1;
        target.focus({ preventScroll: true });
      }
      navigating.current = false;
    });
  }

  async function navigateFromSearch(destination: () => Promise<unknown>) {
    if (navigating.current) return;
    navigating.current = true;
    navigationDone.current = false;
    dialogClosed.current = false;
    closeSearch();
    await destination();
    navigationDone.current = true;
    focusNavigatedRoute();
  }

  useShortcutSuppression(paletteOpen, paletteShortcuts);
  useShortcut("search.open", () => {
    if (open) closeSearch();
    else void openSearch(document.activeElement as HTMLElement | null);
  });
  useShortcut("settings.open", () => {
    void navigateFromSearch(() => navigate({ to: "/workspace/settings" }));
  });
  useShortcut("shortcuts.open", () => {
    void navigateFromSearch(() => navigate({ to: "/workspace/shortcuts" }));
  });
  useShortcut("shortcuts.open.mod", () => {
    void navigateFromSearch(() => navigate({ to: "/workspace/shortcuts" }));
  });

  async function choose(target: GlobalSearchTarget) {
    await navigateFromSearch(async () => {
      if (target.kind === "chat") {
        await navigate({ search: { sessionId: target.sessionId }, to: "/workspace/chat" });
      } else if (target.kind === "schedule") {
        await navigate({
          params: { scheduleName: target.scheduleName },
          to: "/workspace/schedules/$scheduleName",
        });
      } else if (target.kind === "page") {
        await navigate({ to: target.to });
      } else if (target.kind === "settingsSection") {
        await navigate({
          params: { section: target.section },
          search: { item: undefined },
          to: "/workspace/settings/$section",
        });
      } else if (target.kind === "memory") {
        await navigate({
          params: { section: "memory" },
          search: { item: target.item },
          to: "/workspace/settings/$section",
        });
      } else if (target.item) {
        await navigate({
          params: { item: target.item, view: target.view },
          to: "/workspace/skills/$view/$item",
        });
      } else {
        await navigate({ search: { item: undefined, view: target.view }, to: "/workspace/skills" });
      }
    });
  }

  function onInputKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    const keyState = {
      isComposing: event.nativeEvent.isComposing,
      key: event.key,
      keyCode: event.keyCode,
    };
    // The guard tracks every keydown. Keys an input method editor consumes
    // (candidate navigation, commit) are its own, so they stop here, before
    // cmdk's list handler on the palette root. Home and End move the caret.
    const imeOwned = composition.current.ownsKeyDown(keyState);
    if ((imeOwned || event.key === "Home" || event.key === "End") && listKeys.has(event.key)) {
      event.stopPropagation();
    }
  }

  const inputLabel = canSearchInventory
    ? "Search chats, schedules, skills, and memory"
    : "Search help and pages";

  return (
    <>
      <button
        aria-keyshortcuts="Control+K Meta+K"
        aria-label="Search"
        disabled={checkingAuth}
        className={cn(
          "@container flex size-11 shrink-0 items-center justify-center gap-2 rounded-md border border-border bg-background px-0 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          // Icon-only on narrow screens; a search field from 500px up. The
          // label and keycap appear once the field is wide enough for them.
          "min-[500px]:w-full min-[500px]:justify-start min-[500px]:px-3",
        )}
        onClick={(event) => void openSearch(event.currentTarget)}
        type="button"
      >
        <Search aria-hidden="true" className="size-4 shrink-0" />
        <span className="hidden min-w-0 flex-1 truncate text-left text-sm @min-[6.5rem]:inline">
          Search…
        </span>
        <kbd className="pointer-events-none hidden shrink-0 select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[0.65rem] font-medium text-muted-foreground @min-[10.5rem]:inline-flex">
          {keycaps("mod+k", mac).map((keycap) => (
            <span key={keycap}>{keycap}</span>
          ))}
        </kbd>
      </button>

      <CommandDialog
        className="top-[max(4rem,12dvh)] max-h-[min(36rem,calc(100dvh-4rem))] translate-y-0 bg-popover text-popover-foreground max-[499px]:top-[var(--search-viewport-top,0px)] max-[499px]:left-0 max-[499px]:h-[var(--search-viewport-height,100dvh)] max-[499px]:max-h-none max-[499px]:w-full max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:rounded-none max-[499px]:border-0 max-[499px]:pt-[env(safe-area-inset-top)] max-[499px]:pb-[env(safe-area-inset-bottom)] max-[499px]:[&>[data-slot=command]]:h-full"
        commandProps={{ label: inputLabel, vimBindings: false }}
        contentProps={{
          onCloseAutoFocus: (event) => {
            event.preventDefault();
            if (navigating.current) {
              dialogClosed.current = true;
              focusNavigatedRoute();
            } else if (invokingElement.current?.isConnected) {
              invokingElement.current.focus({ preventScroll: true });
            }
          },
          onOpenAutoFocus: (event) => {
            event.preventDefault();
            inputRef.current?.focus();
          },
          style: viewportStyle,
        }}
        description={null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) closeSearch();
        }}
        open={paletteOpen}
        // Studio's ranked index already filters; cmdk's fuzzy filter is off.
        shouldFilter={false}
        showCloseButton={false}
        title="Search Mecatl"
      >
        <CommandInput
          aria-label={inputLabel}
          className="text-base min-[500px]:text-sm"
          icon={
            loading ? (
              <>
                <LoaderCircle
                  aria-hidden="true"
                  className="size-4 shrink-0 animate-spin opacity-50"
                />
                <span className="sr-only">Loading searchable items</span>
              </>
            ) : undefined
          }
          onCompositionEnd={() => composition.current.end()}
          onCompositionStart={() => composition.current.start()}
          onKeyDown={onInputKeyDown}
          onKeyUp={(event) => composition.current.keyUp(event)}
          onPointerDownCapture={() => composition.current.pointerChoice()}
          onTouchStartCapture={() => composition.current.pointerChoice()}
          onValueChange={setQuery}
          placeholder={
            canSearchInventory
              ? "Search chats, schedules, skills, and memory…"
              : "Search help and pages…"
          }
          ref={inputRef}
          trailing={
            <DialogClose
              aria-label="Close search"
              className="-mr-2 flex size-11 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <X aria-hidden="true" className="size-4" />
            </DialogClose>
          }
          value={query}
        />

        <CommandList
          className="max-[499px]:max-h-none max-[499px]:min-h-0 max-[499px]:flex-1"
          label="Search results"
        >
          {!query.trim() ? (
            <CommandEmpty>
              <SearchPrompt
                inventoryAuthorized={canSearchInventory}
                sessionCheckFailed={staticHelpOnly}
              />
            </CommandEmpty>
          ) : resultCount === 0 && !loading ? (
            <CommandEmpty>
              {/* CommandEmpty owns the slot's spacing (its className is not merged). */}
              <span className="text-muted-foreground">No results for “{query.trim()}”</span>
            </CommandEmpty>
          ) : null}
          {groups.map((group) => (
            <CommandGroup heading={group.section} key={group.section}>
              {group.items.map((result) => (
                <SearchResultItem
                  item={result}
                  key={result.id}
                  onChoose={() => void choose(result.target)}
                />
              ))}
            </CommandGroup>
          ))}
        </CommandList>

        <div className="flex min-h-9 shrink-0 items-center justify-between gap-3 border-t px-3 py-2 text-xs text-muted-foreground">
          <span aria-live="polite">
            {!canSearchInventory
              ? "Workspace inventories unavailable; search help and pages"
              : partialError
                ? "Some inventories could not be searched"
                : loading
                  ? "Loading searchable inventories"
                  : query.trim() && !loading
                    ? `${resultCount} result${resultCount === 1 ? "" : "s"}`
                    : "Results stay in this browser"}
          </span>
          <span className="hidden min-[500px]:inline">↑↓ select · Enter open · Esc close</span>
        </div>
      </CommandDialog>
    </>
  );
}

function SearchPrompt({
  inventoryAuthorized,
  sessionCheckFailed,
}: {
  inventoryAuthorized: boolean;
  sessionCheckFailed: boolean;
}) {
  return (
    <div className="px-4">
      <p className="font-medium">
        {inventoryAuthorized ? "Find anything in your workspace" : "Search help and pages"}
      </p>
      <p className="mt-1 text-xs leading-5 text-muted-foreground">
        {inventoryAuthorized
          ? "Search titles, names, descriptions, owners, models, and statuses."
          : sessionCheckFailed
            ? "Studio could not check your session, so workspace items are unavailable."
            : "Workspace items are unavailable until your account is known."}
      </p>
    </div>
  );
}

const resultIcons = {
  chat: MessageCircle,
  memory: Brain,
  page: Compass,
  schedule: CalendarClock,
  settingsSection: Compass,
  skill: GraduationCap,
} as const satisfies Record<GlobalSearchTarget["kind"], unknown>;

/**
 * One result row. Focus stays in the combobox input (cmdk's
 * `aria-activedescendant` pattern), so a pointer press never takes it, and a
 * touch that scrolled the list is a scroll, not a choice.
 */
function SearchResultItem({ item, onChoose }: { item: GlobalSearchItem; onChoose: () => void }) {
  const touchStart = useRef<{ moved: boolean; x: number; y: number } | null>(null);
  const Icon = resultIcons[item.target.kind];
  return (
    <CommandItem
      onClickCapture={(event: MouseEvent<HTMLDivElement>) => {
        const scrolled = touchStart.current?.moved;
        touchStart.current = null;
        if (!scrolled) return;
        // Capture runs before cmdk's own click handler on this row.
        event.preventDefault();
        event.stopPropagation();
      }}
      onMouseDown={(event) => event.preventDefault()}
      onPointerDown={(event) => {
        // A touch scroll may suppress its click, leaving the movement flag
        // behind. A new mouse/pen press is a separate choice, not that scroll.
        if (event.pointerType !== "touch") touchStart.current = null;
      }}
      onSelect={onChoose}
      onTouchCancel={() => {
        touchStart.current = null;
      }}
      onTouchMove={(event) => {
        const touch = event.touches[0];
        const start = touchStart.current;
        if (!touch || !start) return;
        if (Math.hypot(touch.clientX - start.x, touch.clientY - start.y) > 8) {
          start.moved = true;
        }
      }}
      onTouchStart={(event) => {
        const touch = event.touches[0];
        if (touch) touchStart.current = { moved: false, x: touch.clientX, y: touch.clientY };
      }}
      value={item.id}
    >
      <Icon aria-hidden="true" className="size-4 shrink-0" />
      <div className="flex min-w-0 flex-col">
        <span className="truncate text-sm">{item.title}</span>
        {item.description && (
          <span className="truncate text-xs text-muted-foreground">{item.description}</span>
        )}
      </div>
    </CommandItem>
  );
}
