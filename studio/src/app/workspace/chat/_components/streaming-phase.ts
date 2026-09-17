import { sanitizeLine } from "@/features/agent/stop-reason";
import type { AgentMessage } from "@/features/agent/types";

/** Rune cap for one tool name on the live activity line. */
const MAX_TOOL_NAME_RUNES = 40;

/** How many distinct running tool names the activity line spells out. */
const MAX_NAMED_TOOLS = 3;

/**
 * The phase label for the bottom-of-transcript activity line while a turn
 * runs: the NAME(s) of the tool(s) currently running ("Running Read",
 * "Running Read, Grep"), else "Writing" once text has streamed, else
 * "Thinking". Mirrors the TUI footer, which names the running tool rather
 * than saying "running tools" — the name is what tells the user whether the
 * agent is reading, editing, or shelling out.
 *
 * Names are daemon-influenced (an MCP server names its own tools), so each
 * is clamped to one bounded line; a nameless running call falls back to the
 * generic label.
 */
export function streamingPhaseLabel(message?: AgentMessage): string {
  const running = (message?.toolCalls ?? []).filter(
    (call) => call.status === "running",
  );
  if (running.length > 0) {
    const names: string[] = [];
    for (const call of running) {
      const name = sanitizeLine(call.name ?? "", MAX_TOOL_NAME_RUNES);
      if (name && !names.includes(name)) names.push(name);
    }
    if (names.length === 0) return "Running tools";
    const shown = names.slice(0, MAX_NAMED_TOOLS).join(", ");
    const rest = names.length - MAX_NAMED_TOOLS;
    return rest > 0 ? `Running ${shown} +${rest}` : `Running ${shown}`;
  }
  // Reasoning deltas have arrived but no text yet: the model is reasoning,
  // not merely "thinking" in the abstract (the bubble shows the live line).
  if (message?.reasoning?.trim() && !message.content) return "Reasoning";
  return message?.content ? "Writing" : "Thinking";
}
