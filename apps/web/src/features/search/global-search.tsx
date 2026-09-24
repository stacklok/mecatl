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
  ArrowRight,
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
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { Dialog, DialogClose, DialogContent, DialogTitle } from "../../components/ui/dialog";
import { Kbd } from "../../components/ui/kbd";
import { cn } from "../../lib/utils";
import { useThreadSessionIds } from "../chat/thread-map";
import { useShortcut, useShortcutSuppression } from "../shortcuts/shortcut-provider";
import { keycaps, type ShortcutId } from "../shortcuts/shortcut-registry";
import {
  buildGlobalSearchIndex,
  type GlobalSearchItem,
  type GlobalSearchTarget,
  globalSearchPages,
  groupSearchResults,
  searchGlobalIndex,
} from "./search-index";
import {
  clampActiveIndex,
  createSearchCompositionGuard,
  moveActiveIndex,
  shouldActivateResult,
} from "./search-keyboard";

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
  if (session?.status === "disabled" && session.mode !== "oidc") {
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
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const invokingElement = useRef<HTMLElement | null>(null);
  const navigating = useRef(false);
  const navigationDone = useRef(false);
  const dialogClosed = useRef(false);
  const opening = useRef(false);
  const mounted = useRef(true);
  const composition = useRef(createSearchCompositionGuard());
  const scrollActiveIntoView = useRef(false);
  const [visualViewport, setVisualViewport] = useState<{ height: number; top: number } | null>(
    null,
  );
  const idPrefix = useId();
  const listboxId = `${idPrefix}-results`;
  const optionId = (index: number) => `${idPrefix}-option-${index}`;
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
  const results = useMemo(() => searchGlobalIndex(index, query), [index, query]);
  const groups = useMemo(() => groupSearchResults(results), [results]);
  const flatResults = useMemo(() => groups.flatMap((group) => group.items), [groups]);
  // Inventories load while the user types, so the stored index can outrun the list.
  const highlighted = clampActiveIndex(activeIndex, flatResults.length);
  const activeResult = flatResults[highlighted];
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
    setActiveIndex(0);
    void queryClient.invalidateQueries({ queryKey: getAuthSessionOptions().queryKey });
  }, [accessExpired, queryClient]);

  async function openSearch(invoker: HTMLElement | null) {
    if (accessExpired || opening.current) return;
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
    setActiveIndex(0);
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

  useEffect(() => {
    if (!scrollActiveIntoView.current) return;
    scrollActiveIntoView.current = false;
    document.getElementById(`${idPrefix}-option-${highlighted}`)?.scrollIntoView({
      block: "nearest",
    });
  }, [highlighted, idPrefix]);

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
    // Keys an input method editor consumes (candidate navigation, commit) are its own.
    if (composition.current.ownsKeyDown(keyState)) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const next = moveActiveIndex(
        highlighted,
        event.key === "ArrowDown" ? 1 : -1,
        flatResults.length,
      );
      scrollActiveIntoView.current = next !== highlighted;
      setActiveIndex(next);
    } else if (activeResult && shouldActivateResult(keyState, true)) {
      event.preventDefault();
      void choose(activeResult.target);
    }
  }

  let resultIndex = -1;

  return (
    <>
      <button
        aria-keyshortcuts="Control+K Meta+K"
        aria-label="Search"
        disabled={accessExpired || checkingAuth}
        className="flex size-11 items-center justify-center gap-2 rounded-full px-2.5 text-[#a5b8b4] transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60 min-[500px]:w-full min-[900px]:justify-start min-[900px]:px-3"
        onClick={(event) => void openSearch(event.currentTarget)}
        type="button"
      >
        <Search aria-hidden="true" className="size-[17px]" />
        <span className="hidden text-xs min-[900px]:inline">Search</span>
        <span className="hidden items-center gap-0.5 min-[1100px]:flex">
          {keycaps("mod+k", mac).map((keycap) => (
            <Kbd key={keycap} size="sm">
              {keycap}
            </Kbd>
          ))}
        </span>
      </button>

      <Dialog
        onOpenChange={(nextOpen) => {
          if (!nextOpen) closeSearch();
        }}
        open={paletteOpen}
      >
        <DialogContent
          aria-describedby={undefined}
          className="top-[max(4rem,12dvh)] flex max-h-[min(36rem,calc(100dvh-4rem))] max-w-[min(42rem,calc(100%-1.5rem))] translate-y-0 flex-col gap-0 overflow-hidden rounded-2xl bg-popover p-0 text-popover-foreground shadow-2xl max-[499px]:top-[var(--search-viewport-top,0px)] max-[499px]:left-0 max-[499px]:h-[var(--search-viewport-height,100dvh)] max-[499px]:max-h-none max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:rounded-none max-[499px]:border-0 max-[499px]:pt-[env(safe-area-inset-top)] max-[499px]:pb-[env(safe-area-inset-bottom)] sm:max-w-[min(42rem,calc(100%-1.5rem))]"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            inputRef.current?.focus();
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (navigating.current) {
              dialogClosed.current = true;
              focusNavigatedRoute();
            } else if (invokingElement.current?.isConnected) {
              invokingElement.current.focus({ preventScroll: true });
            }
          }}
          showCloseButton={false}
          style={viewportStyle}
        >
          <DialogTitle className="sr-only">Search Mecatl</DialogTitle>
          <div className="flex items-center gap-3 border-b px-4">
            {loading ? (
              <LoaderCircle
                aria-label="Loading searchable items"
                className="size-5 shrink-0 animate-spin text-muted-foreground"
              />
            ) : (
              <Search aria-hidden="true" className="size-5 shrink-0 text-muted-foreground" />
            )}
            <input
              aria-activedescendant={activeResult ? optionId(highlighted) : undefined}
              aria-autocomplete="list"
              aria-controls={listboxId}
              aria-expanded={true}
              aria-label={
                canSearchInventory
                  ? "Search chats, schedules, skills, and memory"
                  : "Search help and pages"
              }
              autoComplete="off"
              className="h-14 min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-muted-foreground"
              onChange={(event) => {
                setQuery(event.target.value);
                setActiveIndex(0);
              }}
              onKeyDown={onInputKeyDown}
              onKeyUp={(event) => composition.current.keyUp(event)}
              onCompositionStart={() => composition.current.start()}
              onCompositionEnd={() => composition.current.end()}
              onPointerDownCapture={() => composition.current.pointerChoice()}
              onTouchStartCapture={() => composition.current.pointerChoice()}
              placeholder={
                canSearchInventory
                  ? "Search chats, schedules, skills, and memory…"
                  : "Search help and pages…"
              }
              ref={inputRef}
              role="combobox"
              spellCheck={false}
              type="text"
              value={query}
            />
            <DialogClose
              aria-label="Close search"
              className="flex size-11 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X aria-hidden="true" className="size-4" />
            </DialogClose>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-2 max-[499px]:max-h-none">
            {!query.trim() ? (
              <SearchPrompt
                inventoryAuthorized={canSearchInventory}
                sessionCheckFailed={staticHelpOnly}
              />
            ) : flatResults.length === 0 && !loading ? (
              <p className="px-4 py-12 text-center text-sm text-muted-foreground">
                No results for “{query.trim()}”
              </p>
            ) : null}
            <div aria-label="Search results" id={listboxId} role="listbox">
              {groups.map((group) => {
                const headingId = `${idPrefix}-group-${group.section}`;
                return (
                  // biome-ignore lint/a11y/useSemanticElements: a listbox groups its options with role="group"; <fieldset> is a form control grouping and not a valid listbox child
                  <div
                    aria-labelledby={headingId}
                    className="mb-1 last:mb-0"
                    key={group.section}
                    role="group"
                  >
                    <p
                      aria-hidden="true"
                      className="px-3 pt-2 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground"
                      id={headingId}
                    >
                      {group.section}
                    </p>
                    {group.items.map((result) => {
                      resultIndex += 1;
                      const optionIndex = resultIndex;
                      return (
                        <SearchResult
                          active={optionIndex === highlighted}
                          id={optionId(optionIndex)}
                          item={result}
                          key={result.id}
                          onChoose={() => void choose(result.target)}
                          onHover={() => setActiveIndex(optionIndex)}
                        />
                      );
                    })}
                  </div>
                );
              })}
            </div>
          </div>

          <div className="flex min-h-9 items-center justify-between gap-3 border-t px-4 py-2 text-[11px] text-muted-foreground">
            <span aria-live="polite">
              {!canSearchInventory
                ? "Workspace inventories unavailable; search help and pages"
                : partialError
                  ? "Some inventories could not be searched"
                  : loading
                    ? "Loading searchable inventories"
                    : query.trim() && !loading
                      ? `${flatResults.length} result${flatResults.length === 1 ? "" : "s"}`
                      : "Results stay in this browser"}
            </span>
            <span className="hidden sm:inline">↑↓ select · Enter open · Esc close</span>
          </div>
        </DialogContent>
      </Dialog>
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
    <div className="px-4 py-10 text-center">
      <p className="text-sm font-medium">
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

/**
 * One listbox option. Focus stays in the combobox input (the
 * `aria-activedescendant` pattern), so options are not tab stops; a pointer
 * press is kept from stealing focus from the input.
 */
function SearchResult({
  active,
  id,
  item,
  onChoose,
  onHover,
}: {
  active: boolean;
  id: string;
  item: GlobalSearchItem;
  onChoose: () => void;
  onHover: () => void;
}) {
  const touchStart = useRef<{ moved: boolean; x: number; y: number } | null>(null);
  const Icon =
    item.target.kind === "chat"
      ? MessageCircle
      : item.target.kind === "schedule"
        ? CalendarClock
        : item.target.kind === "memory"
          ? Brain
          : item.target.kind === "page" || item.target.kind === "settingsSection"
            ? Compass
            : GraduationCap;
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard activation belongs to the combobox input (aria-activedescendant); options never hold focus
    <div
      aria-selected={active}
      className={cn(
        "group flex w-full cursor-pointer items-center gap-3 rounded-xl px-3 py-3 text-left",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/60",
      )}
      id={id}
      onClick={(event) => {
        if (touchStart.current?.moved) {
          event.preventDefault();
          touchStart.current = null;
          return;
        }
        touchStart.current = null;
        onChoose();
      }}
      onMouseDown={(event) => event.preventDefault()}
      onMouseMove={active ? undefined : onHover}
      onPointerDown={(event) => {
        // A touch scroll may suppress its click, leaving the movement flag
        // behind. A new mouse/pen press is a separate choice, not that scroll.
        if (event.pointerType !== "touch") touchStart.current = null;
      }}
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
      role="option"
      tabIndex={-1}
    >
      <span className="flex size-9 shrink-0 items-center justify-center rounded-lg border bg-background text-muted-foreground">
        <Icon aria-hidden="true" className="size-4" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium">{item.title}</span>
        {item.description && (
          <span className="mt-0.5 block truncate text-xs text-muted-foreground">
            {item.description}
          </span>
        )}
      </span>
      <ArrowRight
        aria-hidden="true"
        className={cn(
          "size-4 shrink-0 text-muted-foreground transition-opacity",
          active ? "opacity-100" : "opacity-0 group-hover:opacity-100",
        )}
      />
    </div>
  );
}
