// SPDX-License-Identifier: Apache-2.0

import { Check, Ellipsis, Hourglass, ListEnd, X } from "lucide-react";
import { useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../../components/ui/dropdown-menu";
import { Input } from "../../components/ui/input";
import type { QueuedMessage } from "./chat-queue";

/**
 * Messages held above the composer as one divided group, from the prototype's
 * `QueuedMessageStrip` (`stack-08`): a header with the count, then one row per
 * message with an actions menu.
 *
 * The rows act on Studio's queue (`chat-queue.ts`):
 *
 * - **Steer** sends the message into the live run. It shows only while a run
 *   can be steered (`onSteer`), and the message leaves the queue only when the
 *   steer is accepted.
 * - **Edit** changes the message in place, so it keeps its place in the queue.
 * - **Delete** removes it.
 *
 * The queue drains on its own when a run settles, so there is no paused
 * state, no "Clear all", and no ↑ gesture as in the prototype.
 */
export function QueuedMessageStrip({
  items,
  onDelete,
  onEdit,
  onSteer,
  steeringId,
}: {
  items: QueuedMessage[];
  onDelete: (id: string) => void;
  onEdit: (id: string, text: string) => void;
  /** Present only while a run can be steered. */
  onSteer?: (id: string) => void;
  /** The message whose steer is in flight. */
  steeringId?: string;
}) {
  const [editingId, setEditingId] = useState<string>();
  const [draft, setDraft] = useState("");
  // Edit swaps the row for its field, so the menu must not hand focus back to its gone trigger.
  const editorTakesFocus = useRef(false);
  if (items.length === 0) return null;

  function save(id: string) {
    if (!draft.trim()) return;
    onEdit(id, draft);
    setEditingId(undefined);
  }

  return (
    <section
      aria-label="Queued messages"
      className="mx-auto mb-1.5 w-full max-w-3xl px-4 max-[499px]:px-3 min-[500px]:px-6"
    >
      <div className="divide-y divide-border/60 overflow-hidden rounded-xl border bg-background">
        <div className="flex items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground">
          <Hourglass aria-hidden="true" className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1">
            {items.length} queued message{items.length === 1 ? "" : "s"}
          </span>
        </div>
        <ol className="divide-y divide-border/60">
          {items.map((item, index) => {
            const position = index + 1;
            const editing = editingId === item.id;
            return (
              <li className="flex min-h-10 items-center gap-2 py-1 pr-1 pl-3" key={item.id}>
                <ListEnd aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
                {editing ? (
                  <>
                    <Input
                      aria-label={`Edit queued message ${position}`}
                      autoFocus
                      className="h-8 min-w-0 flex-1"
                      onChange={(event) => setDraft(event.target.value)}
                      onKeyDown={(event) => {
                        if (event.key === "Escape") setEditingId(undefined);
                        if (event.key === "Enter") save(item.id);
                      }}
                      value={draft}
                    />
                    <Button
                      aria-label={`Save queued message ${position}`}
                      className="size-8 shrink-0"
                      disabled={!draft.trim()}
                      onClick={() => save(item.id)}
                      size="icon"
                      variant="ghost"
                    >
                      <Check aria-hidden="true" />
                    </Button>
                    <Button
                      aria-label={`Cancel editing queued message ${position}`}
                      className="size-8 shrink-0"
                      onClick={() => setEditingId(undefined)}
                      size="icon"
                      variant="ghost"
                    >
                      <X aria-hidden="true" />
                    </Button>
                  </>
                ) : (
                  <>
                    <span className="min-w-0 flex-1 truncate text-sm" title={item.text}>
                      {item.text}
                    </span>
                    <DropdownMenu modal={false}>
                      <DropdownMenuTrigger asChild>
                        <Button
                          aria-label={`Actions for queued message ${position}`}
                          className="size-8 shrink-0 text-muted-foreground"
                          disabled={steeringId === item.id}
                          size="icon"
                          variant="ghost"
                        >
                          <Ellipsis aria-hidden="true" className="size-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent
                        align="end"
                        onCloseAutoFocus={(event) => {
                          if (!editorTakesFocus.current) return;
                          editorTakesFocus.current = false;
                          event.preventDefault();
                        }}
                      >
                        {onSteer && (
                          <DropdownMenuItem
                            disabled={steeringId !== undefined}
                            onSelect={() => onSteer(item.id)}
                          >
                            Steer
                          </DropdownMenuItem>
                        )}
                        <DropdownMenuItem
                          onSelect={() => {
                            editorTakesFocus.current = true;
                            setDraft(item.text);
                            setEditingId(item.id);
                          }}
                        >
                          Edit
                        </DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => onDelete(item.id)} variant="destructive">
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </>
                )}
              </li>
            );
          })}
        </ol>
      </div>
    </section>
  );
}
