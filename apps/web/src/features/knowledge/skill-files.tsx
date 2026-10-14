// SPDX-License-Identifier: Apache-2.0

import { listSkillFilesOptions } from "@mecatl-studio/contracts/query";
import { useQuery } from "@tanstack/react-query";
import { bundledLanguages } from "shiki/langs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { errorMessage } from "../../lib/error-message";
import { formatBytes } from "../../lib/formatters";
import { HighlightedCode } from "../chat/code-highlight";

/**
 * The Files section of the configured skill page: every file of the skill with its text, listed
 * under each other. The BFF loads all the text in one response.
 *
 * DECISION: a plain stacked list, in the order the BFF returns it (SKILL.md first, then the other
 * files alphabetically), each file scrolling on its own. Reason: real skills have few files, so
 * nothing needs collapsing or loading on click, and the page reuses the bordered-panel look the
 * rest of Studio already has. Rejected: collapsible rows and a file picker.
 *
 * DECISION: file text is syntax-highlighted with the same Shiki component the chat uses, chosen by
 * file extension and falling back to plain text. Reason: it already follows the light and dark
 * themes and renders tokens as text, never as markup. Rejected: a second highlighter, and an
 * editor component (Studio has none, and files here are read-only).
 *
 * DECISION: a daemon that predates the skill-file RPCs gets no fallback; the BFF answers 501 with a
 * short explanation and the panel shows it. Reason: Studio is built from the same commit as the
 * daemon contract it targets, so no client runs against an older daemon that needs migrating.
 *
 * DECISION: the skill's instructions are labelled as such, from the `instructions` flag, and not
 * from the name. Reason: the daemon lists the instruction body, without its frontmatter, under the
 * name SKILL.md, so it is not the raw file and its size is the body's size. Without the label a
 * reader looking for `allowed-tools` would assume it is missing. Rejected: showing it as a plain
 * SKILL.md. Showing the frontmatter fields is tracked in #2301.
 *
 * LIMITATION (intentional): only the first files are shown, and a line says how many more exist.
 * There is no paging, virtualization, or load-more. Reason: a skill with dozens of files is very
 * unlikely, and showing them well needs a real front-end design that does not exist yet.
 * LIMITATION (intentional): a binary file is listed with a message, not offered for download. The
 * daemon refuses non-text files by design (reads return text only), so a download would need a new
 * byte-level contract first.
 *
 * SPEC: file text renders as text inside a bounded, scrollable block, never as markup. Keyboard
 * users reach a block that scrolls by Tab in Chromium and Firefox, which focus scrollable areas on
 * their own (checked); Safari was not tested, and the repo's a11y lint forbids `tabIndex` on a `pre`. A block that
 * scrolls fades at the edge it can still scroll toward (the shadcn `scroll-fade-y` utility).
 * SPEC: a file whose text could not be loaded still appears, with its name, size, and a one-line
 * reason in place of the text; one such file never hides the others.
 * DECISION: a learned skill reviewed from the Learned view opens a dialog that shows its body, with no
 * Files section. An active learned skill is also in the All list and opens this page, whose Files
 * section lists its one file, the instructions. Reason: a learned skill is body-only, and the
 * baseline gives agent-owned skills the same page. Rejected: hiding Files for agent-owned skills.
 */
export function SkillFiles({ name }: { name: string }) {
  const query = useQuery(listSkillFilesOptions({ path: { name } }));

  // A failed background refetch leaves the files already loaded in place; only show an error when
  // there is nothing to show.
  if (query.data === undefined) {
    return query.isError ? (
      <Panel destructive text={errorMessage(query.error)} />
    ) : (
      <Panel text="Loading files…" />
    );
  }
  const { files, omitted } = query.data;
  if (files.length === 0) return <Panel text="This skill has no files." />;
  return (
    <div className="space-y-3">
      {files.map((file) => (
        <div className="overflow-hidden rounded-lg border bg-background" key={file.name}>
          <div className="flex items-center justify-between gap-3 border-b px-4 py-2 text-xs">
            <span className="flex min-w-0 items-baseline gap-2">
              <Tooltip onlyWhenTruncated>
                <TooltipTrigger asChild>
                  <span className="min-w-0 truncate font-mono font-medium">{file.name}</span>
                </TooltipTrigger>
                <TooltipContent className="max-w-[min(32rem,calc(100vw-2rem))] break-words font-mono">
                  {file.name}
                </TooltipContent>
              </Tooltip>
              {file.instructions && (
                <span className="shrink-0 text-muted-foreground">
                  Instructions, without frontmatter
                </span>
              )}
            </span>
            <span className="shrink-0 text-muted-foreground">{formatBytes(file.size)}</span>
          </div>
          {file.unavailable ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{file.unavailable}</p>
          ) : (
            <pre className="scroll-fade-y max-h-96 overflow-auto whitespace-pre-wrap px-4 py-3 leading-5">
              <HighlightedCode code={file.content} lang={langForFileName(file.name)} />
            </pre>
          )}
        </div>
      ))}
      {omitted > 0 && (
        <p className="text-xs text-muted-foreground">
          {omitted === 1 ? "1 more file isn't shown." : `${omitted} more files aren't shown.`}
        </p>
      )}
    </div>
  );
}

/** The Shiki language for a file name's extension, or plain text when there is none we know. */
export function langForFileName(fileName: string): string {
  const dot = fileName.lastIndexOf(".");
  const extension = dot < 0 ? "" : fileName.slice(dot + 1).toLowerCase();
  return extension !== "" && extension in bundledLanguages ? extension : "text";
}

function Panel({ destructive, text }: { destructive?: boolean; text: string }) {
  return (
    <div className="rounded-lg border bg-background p-6">
      <p
        className={`text-sm leading-relaxed ${destructive ? "text-destructive" : "text-muted-foreground"}`}
      >
        {text}
      </p>
    </div>
  );
}
