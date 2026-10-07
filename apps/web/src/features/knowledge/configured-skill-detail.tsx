// SPDX-License-Identifier: Apache-2.0

import { listConfiguredSkillsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/badge";
import { Button } from "../../components/ui/button";
import { pageTitleClass } from "../../lib/typography";

/**
 * TERM: skill detail views — Summary (what the daemon reports), Manage (which
 * lifecycle actions are permitted), Files (the skill's content).
 *
 * DECISION: configured-skill Manage is a read-only statement, not a disabled form.
 * Reason: the issue holds create/edit/delete/upload read-only until a write
 * contract is approved; disabled controls would imply a contract that doesn't exist.
 * Rejected: hiding the tab — users then can't tell "no actions" from "not built".
 *
 * ASSUMPTION: Files for a configured skill shows an explanatory empty state, because
 * ListSkills returns only name/description/owner/version and the daemon exposes no
 * file-listing or body RPC for configured skills.
 *
 * ASSUMPTION: the active tab is component state, not a URL search param.
 *
 * SPEC: a configured skill missing from the inventory renders "not found" with a
 * link back, never a blank page; unsupported inventory shows the daemon's reason.
 */
type Tab = "summary" | "manage" | "files";

const TABS: ReadonlyArray<readonly [Tab, string]> = [
  ["summary", "Summary"],
  ["manage", "Manage"],
  ["files", "Files"],
];

export function ConfiguredSkillDetail({ name }: { name: string }) {
  const query = useQuery(listConfiguredSkillsOptions());
  const [tab, setTab] = useState<Tab>("summary");

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
      <div className="mx-auto w-full max-w-5xl px-4 py-7 sm:px-8 sm:py-10">
        <BackLink />
        <div className="mt-7 flex flex-wrap items-center gap-2">
          <h1 className={pageTitleClass("break-words")}>{skill.name}</h1>
          <Badge variant="outline">configured</Badge>
          {skill.agentOwned && <Badge variant="info">agent-owned</Badge>}
        </div>
        <div
          className="mt-6 inline-flex max-w-full overflow-x-auto rounded-full bg-muted p-1"
          role="tablist"
        >
          {TABS.map(([value, label]) => (
            <button
              aria-selected={tab === value}
              className={`h-8 rounded-full px-4 text-sm ${tab === value ? "bg-background font-medium shadow-sm" : "text-muted-foreground"}`}
              key={value}
              onClick={() => setTab(value)}
              role="tab"
              type="button"
            >
              {label}
            </button>
          ))}
        </div>
        <div className="mt-6" role="tabpanel">
          {tab === "summary" && (
            <>
              <p className="max-w-3xl text-sm leading-6 text-muted-foreground">
                {skill.description || "No description recorded."}
              </p>
              <dl className="mt-6 grid gap-3 rounded-xl border bg-card p-4 text-sm sm:grid-cols-3">
                <Fact label="Version" value={skill.activeVersion || "Unversioned"} />
                <Fact label="Owner" value={skill.ownerAgent || "Deployment"} />
                <Fact label="Source" value="Configured" />
              </dl>
            </>
          )}
          {tab === "manage" && (
            <Panel
              text="This skill is managed by the Mecatl deployment. Studio can't edit, delete, or upload configured skills."
              title="Read-only"
            />
          )}
          {tab === "files" && (
            <Panel
              text="The daemon doesn't report file contents for configured skills yet."
              title="No files to show"
            />
          )}
        </div>
      </div>
    </div>
  );
}

function BackLink() {
  return (
    <Button asChild size="sm" variant="outline">
      <Link search={{ item: undefined, view: "configured" }} to="/workspace/skills">
        <ArrowLeft />
        Back to skills
      </Link>
    </Button>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 break-words">{value}</dd>
    </div>
  );
}

function Panel({ text, title }: { text: string; title: string }) {
  return (
    <div className="rounded-xl border border-dashed p-6">
      <h2 className="font-semibold">{title}</h2>
      <p className="mt-2 max-w-xl text-sm text-muted-foreground">{text}</p>
    </div>
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
