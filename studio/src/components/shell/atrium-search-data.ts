import {
  Brain,
  CalendarClock,
  GitBranch,
  MessageSquare,
  Sparkles,
} from "lucide-react";
import type {
  AgentSession,
  CronJob,
  MemoryEntry,
} from "@/features/agent/types";
import type { HarnessSkillInfo } from "@/lib/harness/client";
import {
  describeRelationship,
  inspectRowTitle,
  relationshipTerms,
  runInspectHref,
  sessionKindLabel,
} from "@/lib/session-kinds";
import { WORKSPACE_PAGES } from "@/lib/workspace-pages";
import type { SearchEntry, SearchGroup } from "./search-types";

/** Every metadata term a session row matches on besides its title. */
function sessionKeywords(session: AgentSession): string[] {
  return [
    session.id,
    session.model,
    session.placementLabel ?? "",
    session.placementBranch ?? "",
    session.kind ?? "",
    ...relationshipTerms(session.relationship),
  ].filter(Boolean);
}

/**
 * Global-search index for the workspace surfaces, built from live daemon
 * data on every change (Studio is daemon-only — there is nothing to index at
 * module load). Transcripts are not indexed, so chat entries carry no body:
 * matches land on titles and metadata — id, model, placement label/branch,
 * kind, and the relationship's ids and names (the TUI's /sessions filter
 * surface). `runs` (the inspect-only inventory) index under their own
 * heading and deep-link to the parent chat with `?inspect=`, which opens the
 * read-only transcript.
 */
export function buildAtriumSearchEntries(input: {
  sessions: readonly AgentSession[];
  runs?: readonly AgentSession[];
  jobs: readonly CronJob[];
  skills: readonly HarnessSkillInfo[];
  memories: readonly MemoryEntry[];
}): SearchEntry[] {
  const entries: SearchEntry[] = [];

  for (const session of input.sessions) {
    entries.push({
      id: `chat-${session.id}`,
      source: "atrium",
      category: "chat",
      title: session.title,
      subtitle:
        session.placementLabel || session.workspace || session.model || "Chat",
      href: `/workspace/chat/${encodeURIComponent(session.id)}`,
      icon: MessageSquare,
      keywords: sessionKeywords(session),
    });
  }

  for (const run of input.runs ?? []) {
    const relationship = describeRelationship(run.relationship);
    entries.push({
      id: `run-${run.id}`,
      source: "atrium",
      category: "run",
      title: inspectRowTitle(run),
      subtitle:
        run.title && relationship
          ? `${sessionKindLabel(run.kind)} · ${relationship}`
          : sessionKindLabel(run.kind),
      href: runInspectHref(run),
      icon: GitBranch,
      keywords: sessionKeywords(run),
    });
  }

  for (const job of input.jobs) {
    entries.push({
      id: `schedule-${job.name}`,
      source: "atrium",
      category: "schedule",
      title: job.name,
      subtitle: job.schedule,
      href: `/workspace/schedules/${encodeURIComponent(job.name)}`,
      icon: CalendarClock,
      keywords: [job.instruction],
    });
  }

  for (const skill of input.skills) {
    entries.push({
      id: `skill-${skill.name}`,
      source: "atrium",
      category: "skill",
      title: skill.name,
      subtitle: skill.description,
      href: `/workspace/skills/${encodeURIComponent(skill.name)}`,
      icon: Sparkles,
    });
  }

  for (const entry of input.memories) {
    entries.push({
      id: `memory-${entry.id}`,
      source: "atrium",
      category: "memory",
      title: entry.title,
      subtitle: entry.content,
      href: `/workspace/settings/memory/${encodeURIComponent(entry.id)}`,
      icon: Brain,
    });
  }

  return entries;
}

export const ATRIUM_SEARCH_GROUPS: readonly SearchGroup[] = [
  { category: "chat", heading: "Chats", source: "atrium" },
  { category: "run", heading: "Runs", source: "atrium" },
  { category: "memory", heading: "Memory", source: "atrium" },
  { category: "skill", heading: "Skills", source: "atrium" },
  { category: "schedule", heading: "Scheduled", source: "atrium" },
  { category: "page", heading: "Pages", source: "atrium" },
];

/**
 * The static help pages (`WORKSPACE_PAGES`) as search entries — the one
 * part of the index that is NOT daemon data, so it is a constant rather
 * than an input of `buildAtriumSearchEntries`; the palette appends it to
 * the live entries. ⌘K "help" finds Help & about and Keyboard shortcuts.
 */
export const WORKSPACE_PAGE_ENTRIES: readonly SearchEntry[] =
  WORKSPACE_PAGES.map((page) => ({
    id: `page-${page.href}`,
    source: "atrium",
    category: "page",
    title: page.label,
    subtitle: page.description,
    href: page.href,
    icon: page.icon,
    keywords: page.keywords,
  }));
