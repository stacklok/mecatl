"use client";

import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  clampComposerInsert,
  getHarnessMcpPrompt,
  listHarnessMcpPrompts,
  type McpPromptView,
  type McpRenderedPrompt,
  renderPromptForComposer,
} from "@/lib/harness/mcp";
import {
  ALL_SERVERS,
  distinctServers,
  INSERT_INTO_MESSAGE,
  matchesQuery,
  mcpPickerErrorText,
  PickerBusy,
  PickerFilters,
  useMcpPickerListing,
} from "./mcp-picker-shared";

/**
 * The composer's MCP prompt picker — mecatui's f8 `mcpPrompts` →
 * `mcpPromptArgs` flow as a three-step dialog: (1) the prompts the connected
 * servers expose (`GET /v1/mcp/prompts`), filtered by text and server; (2) a
 * form with one field per argument, Render held until every required one is
 * filled; (3) the rendered messages (`POST /v1/mcp/prompts/get`) to review.
 * "Insert into message" hands the text to the composer — it is never sent
 * on the user's behalf (the TUI's "press enter to send" contract). A prompt
 * without arguments renders straight from the list.
 */

export const PROMPT_PICKER_TITLE = "Insert an MCP prompt";
const PROMPT_PICKER_DESCRIPTION =
  "Pick a prompt from a connected MCP server, fill in its arguments and review the result. The text lands in your message for you to edit and send.";
export const NO_MCP_PROMPTS_TEXT =
  "No MCP prompts are exposed by the connected servers.";
export const MCP_PROMPTS_UNSUPPORTED_TEXT =
  "This daemon does not serve MCP prompts.";
const RENDER_PROMPT_LABEL = "Render";

type Step =
  | { kind: "list" }
  | { kind: "args"; prompt: McpPromptView }
  | { kind: "preview"; prompt: McpPromptView; rendered: McpRenderedPrompt };

/** The prompt's display name: title, else its programmatic name. */
export function promptLabel(prompt: Pick<McpPromptView, "title" | "name">) {
  return prompt.title || prompt.name;
}

/** True once every REQUIRED argument holds a non-blank value. */
export function requiredArgumentsFilled(
  prompt: Pick<McpPromptView, "arguments">,
  values: Readonly<Record<string, string>>,
): boolean {
  return prompt.arguments.every(
    (argument) => !argument.required || (values[argument.name] ?? "").trim(),
  );
}

/** The arguments actually sent: blank optional fields are left out. */
export function argumentsToSend(
  values: Readonly<Record<string, string>>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(values)) {
    if (value.trim()) out[name] = value;
  }
  return out;
}

export interface McpPromptPickerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Receives the rendered, clamped text to append to the composer. */
  onInsert: (text: string) => void;
}

export function McpPromptPicker({
  open,
  onOpenChange,
  onInsert,
}: McpPromptPickerProps) {
  const idPrefix = useId();
  const listing = useMcpPickerListing<McpPromptView>(
    open,
    (signal) => listHarnessMcpPrompts("", signal),
    MCP_PROMPTS_UNSUPPORTED_TEXT,
  );
  const [step, setStep] = useState<Step>({ kind: "list" });
  const [query, setQuery] = useState("");
  const [server, setServer] = useState(ALL_SERVERS);
  const [values, setValues] = useState<Record<string, string>>({});
  const [rendering, setRendering] = useState(false);
  const [renderError, setRenderError] = useState<string | null>(null);

  // A fresh pick per opening: a half-filled form must not ride into the
  // next prompt.
  useEffect(() => {
    if (open) {
      setStep({ kind: "list" });
      setQuery("");
      setServer(ALL_SERVERS);
      setValues({});
      setRendering(false);
      setRenderError(null);
    }
  }, [open]);

  const prompts = listing.status === "ready" ? listing.items : [];
  const servers = useMemo(() => distinctServers(prompts), [prompts]);
  const visible = useMemo(
    () =>
      prompts.filter(
        (prompt) =>
          (server === ALL_SERVERS || prompt.server === server) &&
          matchesQuery(query, [
            prompt.title,
            prompt.name,
            prompt.description,
            prompt.server,
          ]),
      ),
    [prompts, server, query],
  );

  const render = useCallback(
    async (prompt: McpPromptView, args: Record<string, string>) => {
      setRendering(true);
      setRenderError(null);
      try {
        const rendered = await getHarnessMcpPrompt(
          prompt.server,
          prompt.name,
          args,
        );
        setStep({ kind: "preview", prompt, rendered });
      } catch (error) {
        setRenderError(mcpPickerErrorText(error, MCP_PROMPTS_UNSUPPORTED_TEXT));
      } finally {
        setRendering(false);
      }
    },
    [],
  );

  const pick = (prompt: McpPromptView) => {
    setRenderError(null);
    if (prompt.arguments.length === 0) {
      void render(prompt, {});
      return;
    }
    setValues({});
    setStep({ kind: "args", prompt });
  };

  const insert = (rendered: McpRenderedPrompt) => {
    onInsert(clampComposerInsert(renderPromptForComposer(rendered.messages)));
    onOpenChange(false);
  };

  const formId = `${idPrefix}-args`;

  let body: React.ReactNode;
  let footer: React.ReactNode;

  if (step.kind === "list") {
    body = (
      <div className="space-y-3">
        <PickerFilters
          idPrefix={idPrefix}
          query={query}
          onQueryChange={setQuery}
          queryLabel="Filter prompts"
          servers={servers}
          server={server}
          onServerChange={setServer}
        />
        {listing.status === "loading" && (
          <PickerBusy>Loading MCP prompts…</PickerBusy>
        )}
        {listing.status === "error" && (
          <p className="text-sm text-destructive break-words" role="alert">
            {listing.message}
          </p>
        )}
        {listing.status === "ready" && prompts.length === 0 && (
          <p className="text-sm text-muted-foreground">{NO_MCP_PROMPTS_TEXT}</p>
        )}
        {listing.status === "ready" &&
          prompts.length > 0 &&
          visible.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No prompt matches the filter.
            </p>
          )}
        {rendering && <PickerBusy>Rendering the prompt…</PickerBusy>}
        {renderError && (
          <p className="text-sm text-destructive break-words" role="alert">
            {renderError}
          </p>
        )}
        {visible.length > 0 && (
          <ul
            className="max-h-80 divide-y divide-border overflow-y-auto rounded-md border border-border"
            aria-label="MCP prompts"
          >
            {visible.map((prompt) => (
              <li key={`${prompt.server}/${prompt.name}`}>
                <button
                  type="button"
                  className="flex w-full flex-col items-start gap-1 px-3 py-2 text-left hover:bg-accent/50 focus-visible:bg-accent/50 focus-visible:outline-none disabled:opacity-50"
                  onClick={() => pick(prompt)}
                  disabled={rendering}
                  data-testid="mcp-prompt-row"
                >
                  <span className="flex w-full flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">
                      {promptLabel(prompt)}
                    </span>
                    {prompt.server && (
                      <Badge variant="outline" className="font-mono">
                        {prompt.server}
                      </Badge>
                    )}
                    {prompt.arguments.length > 0 && (
                      <span className="text-xs text-muted-foreground">
                        {prompt.arguments.length === 1
                          ? "1 argument"
                          : `${prompt.arguments.length} arguments`}
                      </span>
                    )}
                  </span>
                  {prompt.description && (
                    <span className="text-xs text-muted-foreground">
                      {prompt.description}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    );
    footer = (
      <Button variant="outline" onClick={() => onOpenChange(false)}>
        Cancel
      </Button>
    );
  } else if (step.kind === "args") {
    const { prompt } = step;
    const canRender = !rendering && requiredArgumentsFilled(prompt, values);
    // The fields and the footer's Render button share ONE form (wrapped
    // below), so Enter in any field is the browser's implicit submission.
    body = (
      <div className="space-y-3">
        <p className="text-sm">
          <span className="font-medium">{promptLabel(prompt)}</span>
          {prompt.server && (
            <span className="text-muted-foreground"> · {prompt.server}</span>
          )}
          {prompt.description && (
            <span className="block text-xs text-muted-foreground">
              {prompt.description}
            </span>
          )}
        </p>
        {prompt.arguments.map((argument, index) => {
          const fieldId = `${idPrefix}-arg-${index}`;
          const hintId = argument.description ? `${fieldId}-hint` : undefined;
          return (
            <div key={argument.name} className="space-y-1">
              <Label htmlFor={fieldId}>
                {argument.title || argument.name}
                {argument.required ? (
                  <span className="text-muted-foreground"> (required)</span>
                ) : (
                  <span className="text-muted-foreground"> (optional)</span>
                )}
              </Label>
              <Input
                id={fieldId}
                value={values[argument.name] ?? ""}
                onChange={(event) =>
                  setValues((prev) => ({
                    ...prev,
                    [argument.name]: event.target.value,
                  }))
                }
                aria-required={argument.required}
                aria-describedby={hintId}
                autoComplete="off"
                // The first field takes focus so the form is typeable at once.
                autoFocus={index === 0}
              />
              {hintId && (
                <p id={hintId} className="text-xs text-muted-foreground">
                  {argument.description}
                </p>
              )}
            </div>
          );
        })}
        {rendering && <PickerBusy>Rendering the prompt…</PickerBusy>}
        {renderError && (
          <p className="text-sm text-destructive break-words" role="alert">
            {renderError}
          </p>
        )}
      </div>
    );
    footer = (
      <>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            setRenderError(null);
            setStep({ kind: "list" });
          }}
          disabled={rendering}
        >
          Back
        </Button>
        <Button type="submit" disabled={!canRender}>
          {RENDER_PROMPT_LABEL}
        </Button>
      </>
    );
  } else {
    const { prompt, rendered } = step;
    body = (
      <div className="space-y-3">
        <p className="text-sm">
          <span className="font-medium">{promptLabel(prompt)}</span>
          {prompt.server && (
            <span className="text-muted-foreground"> · {prompt.server}</span>
          )}
          {rendered.description && (
            <span className="block text-xs text-muted-foreground">
              {rendered.description}
            </span>
          )}
        </p>
        {rendered.messages.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            The server rendered no message for this prompt.
          </p>
        ) : (
          <ol
            className="max-h-80 space-y-3 overflow-y-auto"
            aria-label="Rendered messages"
          >
            {rendered.messages.map((message, index) => (
              <li
                // biome-ignore lint/suspicious/noArrayIndexKey: rendered messages carry no id; the list is read-only
                key={index}
                className="space-y-1"
              >
                <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                  {message.role || "message"}
                </p>
                <pre className="whitespace-pre-wrap break-words rounded-md border border-border bg-muted/40 p-3 font-sans text-sm">
                  {message.text}
                </pre>
              </li>
            ))}
          </ol>
        )}
      </div>
    );
    footer = (
      <>
        <Button
          variant="outline"
          onClick={() =>
            setStep(
              prompt.arguments.length > 0
                ? { kind: "args", prompt }
                : { kind: "list" },
            )
          }
        >
          Back
        </Button>
        <Button
          onClick={() => insert(rendered)}
          disabled={rendered.messages.length === 0}
        >
          {INSERT_INTO_MESSAGE}
        </Button>
      </>
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl" data-testid="mcp-prompt-picker">
        <DialogHeader>
          <DialogTitle>{PROMPT_PICKER_TITLE}</DialogTitle>
          <DialogDescription>{PROMPT_PICKER_DESCRIPTION}</DialogDescription>
        </DialogHeader>
        {step.kind === "args" ? (
          <form
            id={formId}
            className="grid gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (!rendering && requiredArgumentsFilled(step.prompt, values)) {
                void render(step.prompt, argumentsToSend(values));
              }
            }}
          >
            {body}
            <DialogFooter>{footer}</DialogFooter>
          </form>
        ) : (
          <>
            {body}
            <DialogFooter>{footer}</DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
