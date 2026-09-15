"use client";

import { Ellipsis, Split } from "lucide-react";
import { useId, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { useHarnessRuntime } from "@/features/agent/hooks/use-harness-runtime";
import { useDisabledModels } from "@/lib/model-preferences";
import {
  ExternalManagedNote,
  Note,
  OfflineNote,
  SettingsCard,
  SettingsRow,
} from "./settings-card";

type Runtime = ReturnType<typeof useHarnessRuntime>;

interface DraftCategory {
  /** Stable react key — tier names are editable, so they cannot key the list. */
  key: string;
  name: string;
  description: string;
  model: string;
}

/** The controller's category-name grammar: a letter, then up to 39 more
 * letters/digits/dashes/underscores (compared case-insensitively). */
const categoryNamePattern = /^[a-z][a-z0-9_-]{0,39}$/;
const maxCategoryDescription = 300;

/** Names compare case-insensitively everywhere (grammar, duplicates, the
 * default-category match), so one normalization is shared by all of them. */
const normalizeName = (name: string) => name.trim().toLowerCase();

/**
 * Per-category validation for the add/edit dialog, mirroring the controller's
 * own rules so a bad category can never be saved INTO the draft — which keeps
 * the outer form message about the things only the whole draft knows (the
 * 2–8 count, the classifier, the default). `otherNames` holds the normalized
 * names of every OTHER category, so an edit never collides with itself.
 * Exported for its vitest.
 */
export function categoryProblem(
  category: { name: string; description: string; model: string },
  otherNames: ReadonlySet<string>,
): string | null {
  const name = normalizeName(category.name);
  if (!name) return "Give the category a name.";
  if (!categoryNamePattern.test(name)) {
    return "Names start with a letter, then letters, numbers, dashes or underscores.";
  }
  if (otherNames.has(name)) {
    return `Another category is already named "${name}".`;
  }
  if (!category.description.trim()) {
    return "Describe what belongs here — it is what the classifier matches on.";
  }
  if (category.description.length > maxCategoryDescription) {
    return `Keep the description at most ${maxCategoryDescription} characters.`;
  }
  if (!category.model.trim()) return "Choose a model for this category.";
  return null;
}

/**
 * Whole-draft validation, mirroring the controller's own rules so a mistake
 * surfaces here rather than after a daemon restart has already been
 * attempted. The dialog gate above means the per-category arm is defensive —
 * in practice this reports the count, the classifier, or a missing default.
 */
function routingProblem(draft: {
  classifierModel: string;
  defaultCategory: string;
  categories: DraftCategory[];
}): string | null {
  if (!draft.classifierModel.trim()) return "Choose a classifier model.";
  if (draft.categories.length < 2 || draft.categories.length > 8) {
    return "Routing needs between 2 and 8 categories.";
  }
  const seen = new Set<string>();
  for (const category of draft.categories) {
    const problem = categoryProblem(category, seen);
    if (problem) {
      return `Category "${normalizeName(category.name) || "(unnamed)"}": ${problem}`;
    }
    seen.add(normalizeName(category.name));
  }
  if (!seen.has(normalizeName(draft.defaultCategory))) {
    return "Pick a default category from a category's menu.";
  }
  return null;
}

function ModelSelect({
  id,
  value,
  onChange,
  models,
  placeholder,
}: {
  id: string;
  value: string;
  onChange: (next: string) => void;
  models: { id: string; displayName: string }[];
  placeholder: string;
}) {
  // A saved model can fall out of the inventory (provider changed since the
  // save); keep it selectable so opening the form doesn't silently blank it.
  const options =
    !value || models.some((model) => model.id === value)
      ? models
      : [{ id: value, displayName: value }, ...models];
  return (
    <Select value={value || undefined} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {options.map((model) => (
          <SelectItem key={model.id} value={model.id}>
            {model.displayName}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/**
 * The add/edit category dialog: name + model + description with modal-local
 * state, validated by `categoryProblem` before it may write back into the
 * draft. Follows the skill dialogs' shell (left-aligned header, pill footer
 * buttons, full-screen under 500px).
 */
function CategoryDialog({
  initial,
  models,
  takenNames,
  onSave,
  onClose,
}: {
  /** The category being edited, or null when adding a new one. */
  initial: { name: string; description: string; model: string } | null;
  models: { id: string; displayName: string }[];
  /** Normalized names of every OTHER category in the draft. */
  takenNames: ReadonlySet<string>;
  onSave: (next: { name: string; description: string; model: string }) => void;
  onClose: () => void;
}) {
  const fieldId = useId();
  const [name, setName] = useState(initial?.name ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [model, setModel] = useState(initial?.model ?? "");

  // Validation is quiet: an incomplete category just keeps Save disabled.
  const problem = categoryProblem({ name, description, model }, takenNames);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="flex max-h-[90vh] flex-col max-[499px]:top-0 max-[499px]:left-0 max-[499px]:h-dvh max-[499px]:max-h-none max-[499px]:w-screen max-[499px]:max-w-none max-[499px]:translate-x-0 max-[499px]:translate-y-0 max-[499px]:rounded-none max-[499px]:border-0 sm:max-w-lg">
        <DialogHeader className="text-left">
          <DialogTitle>
            {initial ? "Edit category" : "Add category"}
          </DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-3">
            <Label htmlFor={`${fieldId}-name`}>Name</Label>
            <Input
              id={`${fieldId}-name`}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-3">
            <Label htmlFor={`${fieldId}-model`}>Model</Label>
            <ModelSelect
              id={`${fieldId}-model`}
              value={model}
              onChange={setModel}
              models={models}
              placeholder="Select"
            />
          </div>
          <div className="flex flex-col gap-3">
            <Label htmlFor={`${fieldId}-desc`}>
              What belongs in this category
            </Label>
            <Textarea
              id={`${fieldId}-desc`}
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              className="min-h-24 text-sm"
            />
          </div>
        </div>

        <DialogFooter className="flex-row justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            className="rounded-full"
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            type="button"
            variant="action"
            className="rounded-full"
            disabled={problem !== null}
            onClick={() => onSave({ name, description, model })}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Editor for the semantic model router: a small classifier reads each prompt
 * and picks a category, so cheap work lands on a cheap model without the user
 * choosing per message. The daemon owns this config — the draft is seeded from
 * it once edited and discarded on save, so the form never diverges from what
 * is actually running. Two cards share the one draft: the router itself, and
 * the category list below it — the save footer covers both.
 */
export function ModelRouterSection({ runtime }: { runtime: Runtime }) {
  const router = runtime.router;
  const { disabled: disabledModels } = useDisabledModels();
  // The daemon's live model inventory IS the routable set — whatever
  // GET /v1/models reports, a router category may name (the old
  // gateway-only filter blanked these pickers on any non-ToolHive
  // deployment). Models switched off on the provider page are hidden here
  // too (a Studio-side preference — ModelSelect still keeps an
  // already-SAVED disabled model selectable, so opening the form never
  // blanks it).
  const models = runtime.models.filter(
    (model) => model.id && !disabledModels.has(model.id),
  );

  const keyCounter = useRef(0);
  const nextKey = () => {
    keyCounter.current += 1;
    return `new-${keyCounter.current}`;
  };

  const [draft, setDraft] = useState<{
    enabled: boolean;
    classifierModel: string;
    defaultCategory: string;
    categories: DraftCategory[];
  } | null>(null);
  /** null = closed; { category: null } = adding; { category } = editing. */
  const [editor, setEditor] = useState<{
    category: DraftCategory | null;
  } | null>(null);

  const editing = draft !== null;
  const view = draft ?? {
    enabled: router?.enabled ?? false,
    classifierModel: router?.classifierModel ?? "",
    defaultCategory: router?.defaultCategory ?? "",
    categories: (router?.categories ?? []).map((category, index) => ({
      key: `saved-${index}`,
      ...category,
    })),
  };
  const problem = editing ? routingProblem(view) : null;

  const patch = (next: Partial<typeof view>) =>
    setDraft((prev) => ({ ...(prev ?? view), ...next }));

  const isDefault = (category: DraftCategory) => {
    const name = normalizeName(category.name);
    return name !== "" && name === normalizeName(view.defaultCategory);
  };

  // The default always renders first; the rest keep their draft order (a
  // stable partition, never a re-sort), so "Set as default" visibly moves
  // that row to the top and nothing else shuffles.
  const displayCategories = [
    ...view.categories.filter(isDefault),
    ...view.categories.filter((category) => !isDefault(category)),
  ];

  const openAdd = () => setEditor({ category: null });

  const saveCategory = (next: {
    name: string;
    description: string;
    model: string;
  }) => {
    const target = editor?.category ?? null;
    if (target) {
      patch({
        categories: view.categories.map((category) =>
          category.key === target.key ? { ...category, ...next } : category,
        ),
        // A renamed default stays the default under its new name.
        defaultCategory: isDefault(target)
          ? normalizeName(next.name)
          : view.defaultCategory,
      });
    } else {
      patch({
        categories: [...view.categories, { key: nextKey(), ...next }],
        // The first category (or the first after the default was deleted)
        // becomes the default — setup shouldn't demand a separate menu trip.
        defaultCategory: view.defaultCategory.trim()
          ? view.defaultCategory
          : normalizeName(next.name),
      });
    }
    setEditor(null);
  };

  const removeCategory = (category: DraftCategory) =>
    patch({
      categories: view.categories.filter(
        (candidate) => candidate.key !== category.key,
      ),
      // Deleting the default clears it — the validation line asks for a new
      // pick until the user chooses one from another row's menu.
      defaultCategory: isDefault(category) ? "" : view.defaultCategory,
    });

  if (!runtime.live) {
    return (
      <SettingsCard title="Model router">
        <OfflineNote />
      </SettingsCard>
    );
  }

  if (runtime.mode === "external") {
    return (
      <SettingsCard title="Model router">
        <div className="flex flex-col gap-2">
          {runtime.status?.modelRouter ? (
            <p className="text-sm">
              Routing is{" "}
              <strong>
                {runtime.status.modelRouter.enabled ? "enabled" : "disabled"}
              </strong>{" "}
              with {runtime.status.modelRouter.categories} categor
              {runtime.status.modelRouter.categories === 1 ? "y" : "ies"}.
            </p>
          ) : null}
          <ExternalManagedNote />
        </div>
      </SettingsCard>
    );
  }

  if (router?.managedByOperator) {
    return (
      <SettingsCard title="Model router">
        <div className="flex flex-col gap-3">
          <p className="text-sm font-medium">
            Managed by an operator settings file
          </p>
          <Note>
            This daemon was started with an imported settings file that owns
            routing along with its aliases, slots and guardrails. Saving from
            here would drop those, so the controller refuses it — change the
            settings file instead. The current categories are shown below.
          </Note>
          <div className="flex flex-col gap-2">
            {(router?.categories ?? []).map((category) => (
              <div key={category.name} className="rounded-lg border p-4">
                <p className="font-mono text-sm font-medium">{category.name}</p>
                <p className="font-mono text-xs text-muted-foreground">
                  {category.model || "no model pinned"}
                </p>
                {category.description && (
                  <p className="mt-1 text-sm text-muted-foreground">
                    {category.description}
                  </p>
                )}
              </div>
            ))}
          </div>
        </div>
      </SettingsCard>
    );
  }

  // The save footer covers the whole draft. It normally lives in the
  // Categories card; while routing is off that card is hidden, so the footer
  // (and, for an incomplete draft, an honest gate explanation) moves up into
  // the router card — hiding the setup is presentation, never data loss.
  const saveFooter = (
    <div className="flex flex-wrap items-center justify-end gap-2">
      {editing && (
        <Button
          variant="outline"
          className="rounded-full"
          onClick={() => setDraft(null)}
        >
          Discard
        </Button>
      )}
      <Button
        variant="action"
        disabled={!editing || problem !== null || runtime.busy === "router"}
        onClick={async () => {
          await runtime.saveRouter({
            enabled: view.enabled,
            classifierModel: view.classifierModel.trim(),
            defaultCategory: view.defaultCategory.trim().toLowerCase(),
            categories: view.categories.map((category) => ({
              name: category.name.trim().toLowerCase(),
              description: category.description.trim(),
              model: category.model.trim(),
            })),
          });
          setDraft(null);
        }}
      >
        {runtime.busy === "router" ? "Saving…" : "Save"}
      </Button>
    </div>
  );

  return (
    <>
      <SettingsCard title="Model router">
        <div className="flex flex-col gap-4">
          <div className="divide-y divide-border/60">
            <SettingsRow
              label="Semantic routing"
              htmlFor="routing-enabled"
              description="Route each prompt to the right model."
            >
              <Switch
                id="routing-enabled"
                checked={view.enabled}
                onCheckedChange={(checked) => patch({ enabled: checked })}
                aria-label="Enable semantic model routing"
              />
            </SettingsRow>

            {view.enabled && (
              <SettingsRow
                label="Classifier model"
                htmlFor="routing-classifier"
                description="Select a fast, small model for categorization."
              >
                <div className="w-52 min-[500px]:w-72">
                  <ModelSelect
                    id="routing-classifier"
                    value={view.classifierModel}
                    onChange={(next) => patch({ classifierModel: next })}
                    models={models}
                    placeholder="Select"
                  />
                </div>
              </SettingsRow>
            )}
          </div>

          {!view.enabled && editing && saveFooter}
        </div>
      </SettingsCard>

      {view.enabled && (
        <SettingsCard title="Categories">
          <div className="flex flex-col gap-4">
            {view.categories.length > 0 && view.categories.length < 8 && (
              <div className="flex justify-end">
                <Button
                  size="sm"
                  variant="outline"
                  className="rounded-full"
                  onClick={openAdd}
                >
                  Add category
                </Button>
              </div>
            )}

            {view.categories.length === 0 ? (
              <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-6 py-12 text-center">
                <div className="flex size-11 items-center justify-center rounded-full bg-muted">
                  <Split className="size-5 text-muted-foreground" />
                </div>
                <div className="space-y-1">
                  <p className="text-sm font-medium">No categories yet</p>
                  <p className="max-w-sm text-sm text-muted-foreground">
                    Routing needs at least two categories — say what belongs in
                    each and which model should handle it.
                  </p>
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  className="rounded-full"
                  onClick={openAdd}
                >
                  Add category
                </Button>
              </div>
            ) : (
              <div className="divide-y overflow-hidden rounded-lg border">
                {displayCategories.map((category) => {
                  const isDef = isDefault(category);
                  // Display names come from the live inventory (unfiltered —
                  // a disabled model still labels correctly); a model the
                  // daemon no longer reports falls back to its raw id, in
                  // mono because it IS an id.
                  const displayName = runtime.models.find(
                    (model) => model.id === category.model,
                  )?.displayName;
                  return (
                    <div
                      key={category.key}
                      className="flex items-center gap-3 px-4 py-3"
                    >
                      <div className="min-w-0 flex-1">
                        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span className="truncate font-mono text-sm font-medium">
                            {category.name || "(unnamed)"}
                          </span>
                          {category.model && (
                            <Badge variant="outline" className="max-w-48">
                              <span
                                className={
                                  displayName
                                    ? "truncate"
                                    : "truncate font-mono"
                                }
                              >
                                {displayName ?? category.model}
                              </span>
                            </Badge>
                          )}
                          {isDef && <Badge variant="info">default</Badge>}
                        </span>
                        <p className="line-clamp-2 text-xs text-muted-foreground">
                          {category.description}
                        </p>
                      </div>
                      <DropdownMenu modal={false}>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 shrink-0"
                            aria-label={`Actions for category ${category.name}`}
                          >
                            <Ellipsis className="size-4" />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem
                            disabled={isDef}
                            onClick={() =>
                              patch({
                                defaultCategory: normalizeName(category.name),
                              })
                            }
                          >
                            Set as default
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onClick={() => setEditor({ category })}
                          >
                            Edit
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => removeCategory(category)}
                          >
                            Delete
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  );
                })}
              </div>
            )}

            {/* Validation is quiet — an incomplete draft just keeps Save
                disabled (the controller hard-rejects an invalid config). */}
            {saveFooter}
          </div>
        </SettingsCard>
      )}

      {editor && (
        <CategoryDialog
          initial={editor.category}
          models={models}
          takenNames={
            new Set(
              view.categories
                .filter((category) => category.key !== editor.category?.key)
                .map((category) => normalizeName(category.name)),
            )
          }
          onSave={saveCategory}
          onClose={() => setEditor(null)}
        />
      )}
    </>
  );
}
