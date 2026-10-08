// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";

/**
 * Shown when the daemon reports `skills: false`: the inventory below is
 * metadata only, and no skill reaches the agent until the Skill tool is on.
 * Renders nothing while the runtime is loading or reports the tool enabled.
 */
export function SkillToolDisabledBanner() {
  const runtime = useQuery(getRuntimeOptions());
  if (runtime.data?.capabilities.skills !== false) return null;
  return (
    <div
      className="flex items-start gap-3 rounded-xl border border-warning/40 bg-warning/10 p-4"
      role="status"
    >
      <Sparkles className="mt-0.5 size-4 shrink-0 text-warning" />
      <p className="text-sm">
        The Skill tool is disabled on this daemon, so skills are not loaded. Start the daemon with a
        skills directory to enable it.
      </p>
    </div>
  );
}
