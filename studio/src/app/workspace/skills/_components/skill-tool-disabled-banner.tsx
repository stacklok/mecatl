"use client";

import { Sparkles } from "lucide-react";
import Link from "next/link";
import { useRuntimeStatus } from "@/features/agent/runtime-status";

/** The daemon-reported fact, worded once; Settings → Tools shows the same fact as its Skill tool status row. */
export const SKILL_TOOL_DISABLED_TEXT =
  "The Skill tool is disabled on this daemon — skills are not loaded.";

/**
 * Skills page banner for a daemon whose capability document says
 * `skills: false` (a managed daemon spawned without `--skills-dir`, or an
 * external deployment that turned the tool off): the inventory below is
 * then the controller's parked list at most, and a skill created here will
 * not reach the agent until the tool is back on. Renders nothing while the
 * daemon reports the tool registered or reports nothing at all.
 */
export function SkillToolDisabledBanner() {
  const { serverCapabilities, mode } = useRuntimeStatus();
  if (serverCapabilities.skills !== false) return null;
  return (
    <div
      role="status"
      data-testid="skill-tool-disabled"
      className="flex items-start gap-3 rounded-xl border border-warning/40 bg-warning/10 p-4"
    >
      <Sparkles className="mt-0.5 size-4 shrink-0 text-warning" />
      <p className="text-sm">
        {SKILL_TOOL_DISABLED_TEXT}{" "}
        {mode === "managed" ? (
          <>
            Turn it on under{" "}
            <Link
              href="/workspace/settings/tools"
              className="underline underline-offset-2"
            >
              Settings → Tools
            </Link>
            .
          </>
        ) : (
          <>
            The external deployment starts mecated without a skills directory.
          </>
        )}
      </p>
    </div>
  );
}
