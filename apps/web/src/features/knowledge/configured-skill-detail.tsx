// SPDX-License-Identifier: Apache-2.0

import { listConfiguredSkillsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PageShell } from "../../components/shell/page-shell";
import { Button } from "../../components/ui/button";
import { pageTitleClass } from "../../lib/typography";
import { cn } from "../../lib/utils";
import { NO_DESCRIPTION } from "./format";
import { humanizeSkillName } from "./humanize-skill-name";

/**
 * TERM: skill detail sections — Summary (what the daemon reports), Manage (lifecycle
 * actions), Files (the skill's content). Sections of one page, not tabs. Avoid: "tabs".
 *
 * DECISION: the layout follows the Studio design baseline (#1779): back pill, serif title,
 * pill row, then Summary. Rejected: tabs, which the baseline does not have.
 *
 * SPEC: a configured skill missing from the inventory renders "not found" with a link back,
 * never a blank page; an unsupported inventory shows the daemon's reason.
 */
export function ConfiguredSkillDetail({ name }: { name: string }) {
  const query = useQuery(listConfiguredSkillsOptions());

  if (query.isPending) return <DetailState text="Loading skill…" />;
  if (query.isError) return <DetailState error text="The skill inventory could not be loaded." />;
  if (!query.data.supported)
    return <DetailState text={query.data.reason} title="Skills are unavailable" />;
  const skill = query.data.items.find((item) => item.name === name);
  if (!skill)
    return (
      <DetailState text="This skill is not in the current inventory." title="Skill not found" />
    );

  return (
    <PageShell className="space-y-5">
      <BackLink />
      <div className="space-y-3">
        <h1
          className={pageTitleClass("break-words text-[44px] leading-[1.05] max-[500px]:text-3xl")}
        >
          {humanizeSkillName(skill.name)}
        </h1>
        <div className="flex flex-wrap items-center gap-2">
          <MetaPill className="font-mono">{skill.name}</MetaPill>
          {skill.agentOwned && skill.ownerAgent && <MetaPill>by {skill.ownerAgent}</MetaPill>}
          {skill.agentOwned && skill.activeVersion && (
            <MetaPill className="font-mono">{skill.activeVersion}</MetaPill>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-10 lg:flex-row lg:items-start">
        <aside className="flex w-full max-w-[465px] flex-col gap-6">
          <div className="space-y-3">
            <h2 className="text-base font-semibold">Summary</h2>
            <p className="text-base leading-relaxed text-muted-foreground">
              {skill.description || NO_DESCRIPTION}
            </p>
          </div>
        </aside>
      </div>
    </PageShell>
  );
}

function BackLink() {
  return (
    <Button asChild className="h-9 w-fit gap-1 rounded-full px-4" size="sm" variant="outline">
      <Link search={{ item: undefined, view: "configured" }} to="/workspace/skills">
        <span aria-hidden="true">‹</span>
        Back
      </Link>
    </Button>
  );
}

function MetaPill({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border bg-background px-3 py-1 text-xs text-muted-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}

function DetailState({ error, text, title }: { error?: boolean; text: string; title?: string }) {
  return (
    <div className="flex h-full items-center justify-center p-6 text-center">
      <div
        className={`max-w-md rounded-xl border border-dashed p-8 ${error ? "border-destructive/40" : ""}`}
        role={error ? "alert" : "status"}
      >
        {title && <h1 className="font-semibold">{title}</h1>}
        <p className={`mt-2 text-sm ${error ? "text-destructive" : "text-muted-foreground"}`}>
          {text}
        </p>
        <Button asChild className="mt-5" size="sm" variant="outline">
          <Link search={{ item: undefined, view: "configured" }} to="/workspace/skills">
            Back to skills
          </Link>
        </Button>
      </div>
    </div>
  );
}
