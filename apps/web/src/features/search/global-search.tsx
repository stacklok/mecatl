// SPDX-License-Identifier: Apache-2.0

import {
  listConfiguredSkillsOptions,
  listLearnedSkillsOptions,
  listSchedulesOptions,
  listSessionsOptions,
  listUserMemoryOptions,
} from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
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
import { type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
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
  groupSearchResults,
  searchGlobalIndex,
} from "./search-index";
import {
  clampActiveIndex,
  isImeComposing,
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
  "shortcuts.open.mod",
];

export function GlobalSearch() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const scrollActiveIntoView = useRef(false);
  const idPrefix = useId();
  const listboxId = `${idPrefix}-results`;
  const optionId = (index: number) => `${idPrefix}-option-${index}`;
  const sessions = useQuery({ ...listSessionsOptions(), enabled: open });
  const schedules = useQuery({ ...listSchedulesOptions(), enabled: open });
  const configuredSkills = useQuery({ ...listConfiguredSkillsOptions(), enabled: open });
  const learnedSkills = useQuery({ ...listLearnedSkillsOptions(), enabled: open });
  const memory = useQuery({ ...listUserMemoryOptions(), enabled: open });
  const threadSessionIds = useThreadSessionIds();
  const inventories = [sessions, schedules, configuredSkills, learnedSkills, memory];

  const index = useMemo(
    () =>
      buildGlobalSearchIndex({
        configuredSkills: configuredSkills.data?.supported ? configuredSkills.data.items : [],
        learnedSkills: learnedSkills.data?.supported ? learnedSkills.data.items : [],
        memory: memory.data?.supported ? memory.data.items : [],
        schedules: schedules.data?.supported ? schedules.data.items : [],
        sessions: (sessions.data?.items ?? []).filter(
          (session) => !threadSessionIds.has(session.id),
        ),
      }),
    [
      configuredSkills.data,
      learnedSkills.data,
      memory.data,
      schedules.data,
      sessions.data,
      threadSessionIds,
    ],
  );
  const results = useMemo(() => searchGlobalIndex(index, query), [index, query]);
  const groups = useMemo(() => groupSearchResults(results), [results]);
  const flatResults = useMemo(() => groups.flatMap((group) => group.items), [groups]);
  // Inventories load while the user types, so the stored index can outrun the list.
  const highlighted = clampActiveIndex(activeIndex, flatResults.length);
  const activeResult = flatResults[highlighted];
  const expanded = flatResults.length > 0;
  const loading = inventories.some((inventory) => inventory.isPending);
  const partialError = inventories.some((inventory) => inventory.isError);
  const mac = navigator.platform.includes("Mac");

  useShortcutSuppression(open, paletteShortcuts);
  useShortcut("search.open", () => setOpen((current) => !current));
  useShortcut("settings.open", () => {
    setOpen(false);
    void navigate({ to: "/workspace/settings" });
  });
  useShortcut("shortcuts.open", () => {
    setOpen(false);
    void navigate({ to: "/workspace/shortcuts" });
  });
  useShortcut("shortcuts.open.mod", () => {
    setOpen(false);
    void navigate({ to: "/workspace/shortcuts" });
  });

  useEffect(() => {
    if (!scrollActiveIntoView.current) return;
    scrollActiveIntoView.current = false;
    document.getElementById(`${idPrefix}-option-${highlighted}`)?.scrollIntoView({
      block: "nearest",
    });
  }, [highlighted, idPrefix]);

  async function choose(target: GlobalSearchTarget) {
    setOpen(false);
    setQuery("");
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
  }

  function onInputKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    const composition = { isComposing: event.nativeEvent.isComposing, keyCode: event.keyCode };
    // Keys an input method editor consumes (candidate navigation, commit) are its own.
    if (isImeComposing(composition)) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const next = moveActiveIndex(
        highlighted,
        event.key === "ArrowDown" ? 1 : -1,
        flatResults.length,
      );
      scrollActiveIntoView.current = next !== highlighted;
      setActiveIndex(next);
    } else if (activeResult && shouldActivateResult({ ...composition, key: event.key }, true)) {
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
        className="flex h-9 items-center gap-2 rounded-full px-2.5 text-[#a5b8b4] transition-colors hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60 min-[900px]:px-3"
        onClick={() => setOpen(true)}
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

      <Dialog onOpenChange={setOpen} open={open}>
        <DialogContent
          aria-describedby={undefined}
          className="top-[max(4rem,12dvh)] block max-w-[min(42rem,calc(100%-1.5rem))] translate-y-0 gap-0 overflow-hidden rounded-2xl bg-popover p-0 text-popover-foreground shadow-2xl sm:max-w-[min(42rem,calc(100%-1.5rem))]"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            inputRef.current?.focus();
          }}
          showCloseButton={false}
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
              aria-expanded={expanded}
              aria-label="Search chats, schedules, skills, and memory"
              autoComplete="off"
              className="h-14 min-w-0 flex-1 bg-transparent text-base outline-none placeholder:text-muted-foreground"
              onChange={(event) => {
                setQuery(event.target.value);
                setActiveIndex(0);
              }}
              onKeyDown={onInputKeyDown}
              placeholder="Search chats, schedules, skills, and memory…"
              ref={inputRef}
              role="combobox"
              spellCheck={false}
              type="text"
              value={query}
            />
            <DialogClose
              aria-label="Close search"
              className="rounded-full p-2 text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <X aria-hidden="true" className="size-4" />
            </DialogClose>
          </div>

          <div className="max-h-[min(28rem,65dvh)] overflow-y-auto p-2">
            {!query.trim() ? (
              <SearchPrompt />
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
              {partialError
                ? "Some inventories could not be searched"
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

function SearchPrompt() {
  return (
    <div className="px-4 py-10 text-center">
      <p className="text-sm font-medium">Find anything in your workspace</p>
      <p className="mt-1 text-xs leading-5 text-muted-foreground">
        Search titles, names, descriptions, owners, models, and statuses.
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
      onClick={onChoose}
      onMouseDown={(event) => event.preventDefault()}
      onMouseMove={active ? undefined : onHover}
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
