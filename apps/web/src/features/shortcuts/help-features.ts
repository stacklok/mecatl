// SPDX-License-Identifier: Apache-2.0

import type { ServerCapabilitiesResponse } from "@mecatl-studio/contracts";

export interface HelpFeatureRow {
  enabled: boolean;
  hint: string;
  id: string;
  label: string;
}

interface HelpFeatureSpec {
  enabled: (caps: ServerCapabilitiesResponse) => boolean;
  hint: string;
  id: string;
  label: string;
}

/**
 * Only capabilities with a genuine, reachable spot in this app's UI —
 * unlike Studio's fuller list (agents/@-mentions, slash commands, teams,
 * media attachments, AI-debug, worktrees, MCP), which this repo has no
 * surface for yet. Documenting a row with nowhere for it to show up would
 * be a fake affordance, not a feature list.
 */
const HELP_FEATURES: readonly HelpFeatureSpec[] = [
  {
    enabled: (caps) => caps.skills,
    hint: "Browse and manage skills under Skills",
    id: "skills",
    label: "Skills",
  },
  {
    enabled: (caps) => caps.learnedSkills,
    hint: "Skills the agent drafted from its own runs, under Skills → Learned",
    id: "learned_skills",
    label: "Learned skills",
  },
  {
    // Settings → Memory is the user-model surface, so the row follows the
    // user-model capability rather than the agent's generic memory tools.
    enabled: (caps) => caps.userModel,
    hint: "Cross-session memory carries context between runs (Settings → Memory)",
    id: "memory",
    label: "Memory",
  },
  {
    // Mirrors the exact gate `memory-consolidation.tsx` uses to render its
    // "Consolidate" button — only the user-model target is wired here.
    enabled: (caps) => caps.manualDream?.userModel?.generate === true,
    hint: "Consolidate memory on demand under Settings → Memory",
    id: "manual_dream",
    label: "Memory consolidation",
  },
  {
    enabled: (caps) => caps.reflection,
    hint: "Ask the agent to reflect on a finished chat, under Settings → Learning",
    id: "reflection",
    label: "Session reflection",
  },
  {
    enabled: (caps) => caps.steer,
    hint: "Enter or Shift+Enter can steer a running turn instead of queueing",
    id: "steer",
    label: "Steer",
  },
  {
    enabled: (caps) => caps.manualCompaction,
    hint: "Compact conversation in the chat ··· menu",
    id: "manual_compaction",
    label: "Compaction",
  },
  {
    enabled: (caps) => caps.scheduling,
    hint: "Scheduled runs live under the Scheduled tab",
    id: "scheduling",
    label: "Scheduling",
  },
  {
    enabled: (caps) => caps.modelSelection,
    hint: "Choose a model for the chat from the composer's Model picker",
    id: "model_selection",
    label: "Model selection",
  },
];

/**
 * Derives the help rows for one connected deployment. Every check reads an
 * explicit boolean off the runtime's own capability document — an absent or
 * falsy value is "not enabled", never assumed on.
 */
export function deriveHelpFeatures(caps: ServerCapabilitiesResponse): HelpFeatureRow[] {
  return HELP_FEATURES.map((spec) => ({
    enabled: spec.enabled(caps),
    hint: spec.hint,
    id: spec.id,
    label: spec.label,
  }));
}
