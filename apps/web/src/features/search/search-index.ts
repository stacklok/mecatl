// SPDX-License-Identifier: Apache-2.0

interface SearchSession {
  id: string;
  modelId: string;
  state: string;
  title: string;
}

/**
 * Only a schedule's identifying metadata is searchable. The prompt is a
 * body, not a title or description, so it is deliberately absent here and
 * never reaches the index even when the caller passes a full schedule.
 */
interface SearchSchedule {
  modelId: string;
  name: string;
  owner: string;
  status: string;
}

interface SearchConfiguredSkill {
  activeVersion: string;
  description: string;
  name: string;
  ownerAgent: string;
}

interface SearchLearnedSkill {
  description: string;
  id: string;
  name: string;
  ownerAgent: string;
  state: string;
  version: string;
}

interface SearchMemoryEntry {
  description: string;
  key: string;
}

export interface GlobalSearchSource {
  configuredSkills: readonly SearchConfiguredSkill[];
  learnedSkills: readonly SearchLearnedSkill[];
  memory: readonly SearchMemoryEntry[];
  schedules: readonly SearchSchedule[];
  sessions: readonly SearchSession[];
}

export type GlobalSearchTarget =
  | { kind: "chat"; sessionId: string }
  | { kind: "schedule"; scheduleName: string }
  | { item: string; kind: "skill"; view: "configured" | "learned" }
  | { item: string; kind: "memory" }
  | { kind: "page"; to: "/workspace/shortcuts" }
  | { kind: "settingsSection"; section: "about" };

export type GlobalSearchSection = "Chats" | "Memory" | "Pages" | "Schedules" | "Skills";

export interface GlobalSearchItem {
  description: string;
  id: string;
  keywords: string;
  section: GlobalSearchSection;
  target: GlobalSearchTarget;
  title: string;
}

/**
 * Static pages, always searchable — merged into the index regardless of
 * loaded inventories. Deliberately lean, matching Studio's own
 * `workspace-pages.ts`: only surfaces with no visible entry point beyond a
 * keystroke belong here. Settings itself has a persistent top-nav icon at
 * every viewport width, so it is NOT listed — About, buried inside the
 * Settings sidebar with no nav icon of its own, is.
 */
export const globalSearchPages: readonly GlobalSearchItem[] = [
  {
    description: "This deployment's status, agent version, and how to report a problem.",
    id: "page:about",
    keywords: "help about version status support report problem",
    section: "Pages",
    target: { kind: "settingsSection", section: "about" },
    title: "About",
  },
  {
    description: "Every keyboard shortcut in the app.",
    id: "page:shortcuts",
    keywords: "keybindings hotkeys help",
    section: "Pages",
    target: { kind: "page", to: "/workspace/shortcuts" },
    title: "Keyboard shortcuts",
  },
];

/** The section order results are grouped in — Pages last, matching Studio's static-help-at-the-end convention. */
export const globalSearchSectionOrder: readonly GlobalSearchSection[] = [
  "Chats",
  "Schedules",
  "Skills",
  "Memory",
  "Pages",
];

export function buildGlobalSearchIndex(source: GlobalSearchSource): GlobalSearchItem[] {
  return [
    ...globalSearchPages,
    ...source.sessions.map((session) => ({
      description: session.modelId || session.state,
      id: `chat:${session.id}`,
      keywords: session.state,
      section: "Chats" as const,
      target: { kind: "chat" as const, sessionId: session.id },
      title: session.title,
    })),
    ...source.schedules.map((schedule) => ({
      description: schedule.modelId || schedule.status,
      id: `schedule:${schedule.name}`,
      keywords: `${schedule.status} ${schedule.owner} ${schedule.modelId}`,
      section: "Schedules" as const,
      target: { kind: "schedule" as const, scheduleName: schedule.name },
      title: schedule.name,
    })),
    ...source.configuredSkills.map((skill) => ({
      description: skill.description,
      id: `configured-skill:${skill.name}`,
      keywords: `${skill.ownerAgent} ${skill.activeVersion}`,
      section: "Skills" as const,
      target: { item: skill.name, kind: "skill" as const, view: "configured" as const },
      title: skill.name,
    })),
    ...source.learnedSkills.map((skill) => ({
      description: skill.description,
      id: `learned-skill:${skill.id}:${skill.version}`,
      keywords: `${skill.ownerAgent} ${skill.state} ${skill.version}`,
      section: "Skills" as const,
      target: { item: skill.id, kind: "skill" as const, view: "learned" as const },
      title: skill.name,
    })),
    ...source.memory.map((entry) => ({
      description: entry.description,
      id: `memory:${entry.key}`,
      keywords: "remembered fact user memory",
      section: "Memory" as const,
      target: { item: entry.key, kind: "memory" as const },
      title: entry.key,
    })),
  ];
}

export function searchGlobalIndex(
  items: readonly GlobalSearchItem[],
  query: string,
  limit = 40,
): GlobalSearchItem[] {
  const normalizedQuery = normalize(query.trim());
  if (!normalizedQuery) return [];

  const tokens = normalizedQuery.split(/\s+/);
  return items
    .map((item, order) => {
      const title = normalize(item.title);
      const description = normalize(item.description);
      const haystack = `${title} ${description} ${normalize(item.keywords)}`;
      if (!tokens.every((token) => haystack.includes(token))) return undefined;

      let relevance = 30;
      if (title === normalizedQuery) relevance = 0;
      else if (title.startsWith(normalizedQuery)) relevance = 10;
      else if (title.split(/[^\p{L}\p{N}]+/u).some((word) => word.startsWith(normalizedQuery))) {
        relevance = 20;
      }
      relevance += Math.max(0, haystack.indexOf(tokens[0] ?? "")) / 1_000;

      return { item, order, relevance };
    })
    .filter((match): match is NonNullable<typeof match> => match !== undefined)
    .sort((left, right) => left.relevance - right.relevance || left.order - right.order)
    .slice(0, limit)
    .map((match) => match.item);
}

/**
 * Groups an already relevance-sorted result list into per-section buckets,
 * in `globalSearchSectionOrder`, preserving each section's relative order —
 * matching Studio's category-grouped result list instead of one flat list
 * with an inline per-row section label.
 */
export function groupSearchResults(
  results: readonly GlobalSearchItem[],
): Array<{ items: GlobalSearchItem[]; section: GlobalSearchSection }> {
  const bySection = new Map<GlobalSearchSection, GlobalSearchItem[]>();
  for (const item of results) {
    bySection.set(item.section, [...(bySection.get(item.section) ?? []), item]);
  }
  return globalSearchSectionOrder
    .map((section) => ({ items: bySection.get(section) ?? [], section }))
    .filter((group) => group.items.length > 0);
}

function normalize(value: string): string {
  return value
    .normalize("NFKD")
    .replace(/\p{Diacritic}/gu, "")
    .toLocaleLowerCase();
}
