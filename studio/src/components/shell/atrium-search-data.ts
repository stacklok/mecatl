import { Brain, CalendarClock, MessageSquare, Sparkles } from "lucide-react";
import type {
  AgentSession,
  CronJob,
  MemoryEntry,
} from "@/features/agent/types";
import type { HarnessSkillInfo } from "@/lib/harness/client";
import type { SearchEntry, SearchGroup } from "./search-types";

/**
 * Global-search index for the workspace surfaces, built from live daemon
 * data on every change (Studio is daemon-only — there is nothing to index at
 * module load). Transcripts are not indexed, so chat entries carry no body:
 * matches land on titles and metadata only.
 */
export function buildAtriumSearchEntries(input: {
  sessions: readonly AgentSession[];
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
      subtitle: session.workspace || session.model || "Chat",
      href: `/workspace/chat/${encodeURIComponent(session.id)}`,
      icon: MessageSquare,
      keywords: [session.id],
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
  { category: "memory", heading: "Memory", source: "atrium" },
  { category: "skill", heading: "Skills", source: "atrium" },
  { category: "schedule", heading: "Scheduled", source: "atrium" },
];
