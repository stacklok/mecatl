// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions, listConfiguredSkillsOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { SortableHead, useTableSort } from "@/components/sortable-head";
import { PageShell } from "../../components/shell/page-shell";
import { Input } from "../../components/ui/input";
import { Table, TableBody, TableCell, TableHeader, TableRow } from "../../components/ui/table";
import { ToggleGroup, ToggleGroupItem } from "../../components/ui/toggle-group";
import { errorMessage } from "../../lib/error-message";
import { pageTitleClass } from "../../lib/typography";
import { NO_DESCRIPTION } from "./format";
import { humanizeSkillName } from "./humanize-skill-name";
import { LearnedSkills } from "./learned-skills";
import { filterSkills, type SkillSortKey, sortSkills } from "./skill-inventory";
import { SkillToolDisabledBanner } from "./skill-tool-disabled-banner";
import { StateCard } from "./state-card";

export type KnowledgeView = "configured" | "learned";

/** The prototype's pill switch, drawn over the shadcn toggle item's defaults. */
const PILL_CLASS =
  "h-7 min-w-0 rounded-full px-3.5 font-normal text-muted-foreground hover:bg-transparent hover:text-foreground data-[state=on]:bg-background data-[state=on]:font-medium data-[state=on]:text-foreground data-[state=on]:shadow-sm";

export function KnowledgeWorkspace({
  item,
  onItemChange,
  onViewChange,
  view,
}: {
  item?: string;
  onItemChange: (item?: string) => void;
  onViewChange: (view: KnowledgeView) => void;
  view: KnowledgeView;
}) {
  const runtime = useQuery(getRuntimeOptions());
  // Hide the Learned pill once the daemon says it has no learned-skill inventory, unless the
  // URL already points there: that view then explains why it is unavailable.
  // Known gap (#2168): `learnedSkills` only says the lifecycle API exists. It stays true with
  // `learning.mode: off`, so this view can be empty forever with nothing saying why. Once the
  // daemon reports the effective learning status, use it here and in the empty state of
  // `learned-skills.tsx`.
  const learnedHidden = runtime.data?.capabilities.learnedSkills === false && view !== "learned";
  const activeView = view;
  return (
    <PageShell className="space-y-5">
      <SkillToolDisabledBanner />
      <h1 className={pageTitleClass()}>Skills</h1>
      <ToggleGroup
        aria-label="Skills view"
        className="inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-full bg-muted p-1"
        onValueChange={(next) => {
          // Choosing the selected pill again clears a single toggle group; the view stays.
          if (next) onViewChange(next as KnowledgeView);
        }}
        type="single"
        value={activeView}
      >
        {(
          [
            ["configured", "All"],
            ...(learnedHidden ? [] : [["learned", "Learned"] as const]),
          ] as const
        ).map(([value, label]) => (
          <ToggleGroupItem className={PILL_CLASS} key={value} value={value}>
            {label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
      {activeView === "configured" ? (
        <ConfiguredSkills />
      ) : (
        <LearnedSkills onSelect={onItemChange} selectedId={item} />
      )}
    </PageShell>
  );
}

function ConfiguredSkills() {
  const query = useQuery(listConfiguredSkillsOptions());
  const [filter, setFilter] = useState("");
  const sort = useTableSort<SkillSortKey>("name");
  if (query.isPending) return <StateCard text="Loading skills…" />;
  if (query.isError) return <StateCard error text={errorMessage(query.error)} />;
  if (!query.data.supported)
    return <StateCard text={query.data.reason} title="Skills are unavailable" />;
  if (query.data.items.length === 0) return <StateCard icon="skill" text="No skills here yet" />;
  const rows = sortSkills(filterSkills(query.data.items, filter), sort.key, sort.dir);
  return (
    <div className="space-y-3">
      <Input
        aria-label="Filter skills"
        onChange={(event) => setFilter(event.target.value)}
        placeholder="Filter by name, description, or owner"
        value={filter}
      />
      {rows.length === 0 ? (
        <StateCard text={`No skills match "${filter.trim()}".`} />
      ) : (
        <>
          <div className="overflow-hidden rounded-xl border bg-card max-[500px]:hidden">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <SortableHead
                    className="w-px whitespace-nowrap"
                    label="Name"
                    sort={sort}
                    sortKey="name"
                  />
                  <SortableHead label="Description" sort={sort} sortKey="description" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((skill) => (
                  <TableRow key={skill.name}>
                    <TableCell className="w-px max-w-[360px] whitespace-nowrap pr-6">
                      <Link
                        className="block truncate text-sm font-medium hover:underline"
                        params={{ item: skill.name, view: "configured" }}
                        to="/workspace/skills/$view/$item"
                      >
                        {humanizeSkillName(skill.name)}
                      </Link>
                      <p className="truncate font-mono text-xs text-muted-foreground">
                        {skill.name}
                      </p>
                    </TableCell>
                    <TableCell className="w-full max-w-0">
                      <p className="line-clamp-1 whitespace-normal text-xs text-muted-foreground">
                        {skill.description || NO_DESCRIPTION}
                      </p>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          <div className="divide-y overflow-hidden rounded-xl border bg-card min-[500px]:hidden">
            {[...rows]
              .sort((a, b) => humanizeSkillName(a.name).localeCompare(humanizeSkillName(b.name)))
              .map((skill) => (
                <Link
                  className="block px-4 py-3"
                  key={skill.name}
                  params={{ item: skill.name, view: "configured" }}
                  to="/workspace/skills/$view/$item"
                >
                  <span className="block truncate text-sm font-medium">
                    {humanizeSkillName(skill.name)}
                  </span>
                  <span className="line-clamp-2 text-xs text-muted-foreground">
                    {skill.description || NO_DESCRIPTION}
                  </span>
                </Link>
              ))}
          </div>
        </>
      )}
    </div>
  );
}
