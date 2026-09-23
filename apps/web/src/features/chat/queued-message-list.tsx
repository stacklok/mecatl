// SPDX-License-Identifier: Apache-2.0

import { Check, ListEnd, Pencil, Trash2, X } from "lucide-react";
import { useState } from "react";
import { Button } from "../../components/ui/button";
import { Input } from "../../components/ui/input";
import type { QueuedMessage } from "./chat-queue";

export function QueuedMessageList({
  items,
  onDelete,
  onEdit,
}: {
  items: QueuedMessage[];
  onDelete: (id: string) => void;
  onEdit: (id: string, text: string) => void;
}) {
  const [editingId, setEditingId] = useState<string>();
  const [draft, setDraft] = useState("");
  if (items.length === 0) return null;

  return (
    <section
      aria-label="Queued messages"
      className="mx-auto mb-3 w-[calc(100%-2rem)] max-w-3xl overflow-hidden rounded-xl border bg-card"
    >
      <div className="flex items-center gap-2 border-b px-3 py-2 text-xs font-medium text-muted-foreground">
        <ListEnd aria-hidden="true" className="size-4" />
        {items.length} queued message{items.length === 1 ? "" : "s"}
      </div>
      <ol className="divide-y">
        {items.map((item, index) => (
          <li className="flex items-center gap-2 px-3 py-2" key={item.id}>
            <span className="w-5 shrink-0 text-center text-xs text-muted-foreground">
              {index + 1}
            </span>
            {editingId === item.id ? (
              <Input
                aria-label={`Edit queued message ${index + 1}`}
                autoFocus
                className="h-8 min-w-0 flex-1"
                onChange={(event) => setDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape") setEditingId(undefined);
                  if (event.key === "Enter" && draft.trim()) {
                    onEdit(item.id, draft);
                    setEditingId(undefined);
                  }
                }}
                value={draft}
              />
            ) : (
              <p className="min-w-0 flex-1 truncate text-sm">{item.text}</p>
            )}
            {editingId === item.id ? (
              <>
                <Button
                  aria-label={`Save queued message ${index + 1}`}
                  disabled={!draft.trim()}
                  onClick={() => {
                    onEdit(item.id, draft);
                    setEditingId(undefined);
                  }}
                  size="icon"
                  variant="ghost"
                >
                  <Check aria-hidden="true" />
                </Button>
                <Button
                  aria-label={`Cancel editing queued message ${index + 1}`}
                  onClick={() => setEditingId(undefined)}
                  size="icon"
                  variant="ghost"
                >
                  <X aria-hidden="true" />
                </Button>
              </>
            ) : (
              <>
                <Button
                  aria-label={`Edit queued message ${index + 1}`}
                  onClick={() => {
                    setDraft(item.text);
                    setEditingId(item.id);
                  }}
                  size="icon"
                  variant="ghost"
                >
                  <Pencil aria-hidden="true" />
                </Button>
                <Button
                  aria-label={`Delete queued message ${index + 1}`}
                  onClick={() => onDelete(item.id)}
                  size="icon"
                  variant="ghost"
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              </>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}
