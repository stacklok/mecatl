// SPDX-License-Identifier: Apache-2.0

import { formatDate } from "../../lib/formatters";

/**
 * A schedule's run time: the relative label ("in 13h", "10h ago", "Overdue")
 * inside a `<time dateTime>` that also holds the exact local date as text.
 *
 * DECISION (#2369 review): the exact date is never hover-only. A native `title`
 * on plain text can't be reached by keyboard or touch, and screen readers
 * announce it inconsistently. `exact="hidden"` keeps the date for screen
 * readers only, where a row keeps the prototype's density (the inventory
 * table and the phone list; a row opens the detail page). `exact="visible"`
 * shows it after the label, as the detail page's facts do, so every user can
 * read an upcoming run's exact time there.
 *
 * Without a valid instant, only the label renders.
 */
export function RunTime({
  at,
  exact,
  label,
}: {
  at: string;
  exact: "hidden" | "visible";
  label: string;
}) {
  if (!at || Number.isNaN(Date.parse(at))) return label;
  const date = formatDate(at);
  return (
    <time dateTime={at}>
      {label}
      {exact === "hidden" ? (
        <span className="sr-only"> ({date})</span>
      ) : (
        // A narrow row breaks after the dot, never inside the date.
        <>
          {" · "}
          <span className="whitespace-nowrap">{date}</span>
        </>
      )}
    </time>
  );
}
