// SPDX-License-Identifier: Apache-2.0

import { listConfiguredSkillsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Ellipsis } from "lucide-react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { pageTitleClass } from "../../lib/typography";
import { cn } from "../../lib/utils";
import { humanizeSkillName } from "./humanize-skill-name";

/**
 * TERM: skill detail sections — Summary (what the daemon reports), Manage (lifecycle
 * actions), Files (the skill's content). Sections of one page, not tabs. Avoid: "tabs".
 *
 * DECISION: the layout follows the Studio design baseline, mecatl-prototypes PR #46
 * (skill-detail-page.tsx): back pill, serif title, pill row, Summary and Manage in the left
 * column, Files in the right. Rejected: tabs, which the baseline does not have.
 *
 * DECISION: Manage renders the baseline's Edit and Disable/Delete controls, permanently
 * disabled with an explanatory note. Reason: the issue holds configured-skill writes
 * read-only until a write contract is approved, and the baseline shows the controls inert
 * rather than absent. Rejected: omitting them — the baseline would then differ visibly.
 *
 * DECISION: Files is a note, not a listing. Reason: the daemon's ListSkills is metadata-only
 * (name, description, owner, version); it has no file-listing or body RPC for configured
 * skills. This is the intentional deviation to record in the owning issue.
 *
 * SPEC: a configured skill missing from the inventory renders "not found" with a link back,
 * never a blank page; an unsupported inventory shows the daemon's reason.
 */
const MANAGED_NOTE = "Managed by the Mecatl deployment";

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
    <div className="h-full overflow-y-auto">
      <div className="mx-auto w-full max-w-6xl space-y-5 px-4 py-7 sm:px-8 sm:py-10">
        <BackLink />
        <div className="space-y-3">
          <h1
            className={pageTitleClass(
              "break-words text-[44px] leading-[1.05] max-[500px]:text-3xl",
            )}
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
                {skill.description || "No description recorded."}
              </p>
            </div>
            <div className="space-y-3">
              <h2 className="text-base font-semibold">Manage</h2>
              <div className="flex items-center gap-2">
                <Button className="rounded-full" disabled title={MANAGED_NOTE} variant="outline">
                  Edit
                </Button>
                <DropdownMenu modal={false}>
                  <DropdownMenuTrigger asChild>
                    <Button
                      aria-label={`More actions for ${skill.name}`}
                      className="size-9 rounded-full"
                      disabled
                      size="icon"
                      title={MANAGED_NOTE}
                      variant="outline"
                    >
                      <Ellipsis className="size-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="start">
                    <DropdownMenuItem disabled>Disable</DropdownMenuItem>
                    <DropdownMenuItem disabled variant="destructive">
                      Delete
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <p className="text-xs text-muted-foreground">
                {MANAGED_NOTE}. Change the deployment&rsquo;s own skills directory instead.
              </p>
            </div>
          </aside>

          <section className="flex min-w-0 flex-1 flex-col gap-3">
            <h2 className="text-base font-semibold">Files</h2>
            <div className="rounded-lg border bg-background p-6">
              <p className="text-sm leading-relaxed text-muted-foreground">
                The daemon&rsquo;s inventory is metadata-only; the agent reads a skill&rsquo;s body
                only when it loads it.
              </p>
            </div>
          </section>
        </div>
      </div>
    </div>
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
