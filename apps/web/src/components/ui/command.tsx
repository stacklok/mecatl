// SPDX-License-Identifier: Apache-2.0

import { Command as CommandPrimitive, useCommandState } from "cmdk";
import { SearchIcon } from "lucide-react";
import * as React from "react";
import { cn } from "../../lib/utils";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./dialog";

function Command({ className, ...props }: React.ComponentProps<typeof CommandPrimitive>) {
  return (
    <CommandPrimitive
      className={cn(
        "flex h-full w-full flex-col overflow-hidden rounded-md bg-popover text-popover-foreground",
        className,
      )}
      data-slot="command"
      {...props}
    />
  );
}

function CommandDialog({
  title = "Command Palette",
  description = "Search for a command to run...",
  children,
  className,
  showCloseButton = true,
  shouldFilter,
  commandProps,
  contentProps,
  ...props
}: React.ComponentProps<typeof Dialog> & {
  title?: string;
  /** `null` renders no description, so the dialog carries no `aria-describedby`. */
  description?: string | null;
  className?: string;
  showCloseButton?: boolean;
  /** Forwarded to the inner cmdk Command; false delegates filtering to the caller. */
  shouldFilter?: boolean;
  /** Forwarded to the inner cmdk Command, for example `label`, `loop`, or `vimBindings`. */
  commandProps?: Omit<React.ComponentProps<typeof Command>, "children" | "shouldFilter">;
  /** Forwarded to `DialogContent`, for example focus handlers or a style. */
  contentProps?: Omit<
    React.ComponentProps<typeof DialogContent>,
    "children" | "className" | "showCloseButton"
  >;
}) {
  return (
    <Dialog {...props}>
      <DialogContent
        className={cn("overflow-hidden p-0", className)}
        showCloseButton={showCloseButton}
        {...(description === null ? { "aria-describedby": undefined } : {})}
        {...contentProps}
      >
        {/* Inside the content, so the title and description leave the page with the dialog. */}
        <DialogHeader className="sr-only">
          <DialogTitle>{title}</DialogTitle>
          {description !== null && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <Command
          {...commandProps}
          className={cn(
            "**:data-[slot=command-input-wrapper]:h-12 [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground [&_[cmdk-group]]:px-2 [&_[cmdk-group]:not([hidden])_~[cmdk-group]]:pt-0 [&_[cmdk-input-wrapper]_svg]:h-5 [&_[cmdk-input-wrapper]_svg]:w-5 [&_[cmdk-input]]:h-12 [&_[cmdk-item]]:px-2 [&_[cmdk-item]]:py-3 [&_[cmdk-item]_svg]:h-5 [&_[cmdk-item]_svg]:w-5",
            commandProps?.className,
          )}
          shouldFilter={shouldFilter}
        >
          {children}
        </Command>
      </DialogContent>
    </Dialog>
  );
}

function CommandInput({
  className,
  icon,
  ref,
  trailing,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Input> & {
  /** Replaces the leading search icon, for example with a loading indicator. */
  icon?: React.ReactNode;
  /** Rendered after the field, for example a close control. */
  trailing?: React.ReactNode;
}) {
  const inputRef = React.useRef<HTMLInputElement | null>(null);
  const setRefs = React.useCallback(
    (node: HTMLInputElement | null) => {
      inputRef.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );
  useSelectedOptionDescendant(inputRef);
  return (
    <div className="flex h-9 items-center gap-2 border-b px-3" data-slot="command-input-wrapper">
      {icon ?? <SearchIcon className="size-4 shrink-0 opacity-50" />}
      <CommandPrimitive.Input
        className={cn(
          "flex h-10 w-full rounded-md bg-transparent py-3 text-sm outline-hidden placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        data-slot="command-input"
        ref={setRefs}
        {...props}
      />
      {trailing}
    </div>
  );
}

/**
 * cmdk 1.1.1 reads the selected option's id before an automatic selection
 * (the first result, or a replacement for a removed option) has rendered, so
 * its `aria-activedescendant` can be missing or name the previous option.
 * After each selection change, point the input and the list at the option
 * that is actually selected, so assistive technology follows the highlight.
 */
function useSelectedOptionDescendant(inputRef: React.RefObject<HTMLInputElement | null>) {
  const selection = useCommandState((state) => `${state.value}\u0000${state.selectedItemId ?? ""}`);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `selection` is the trigger; the effect reads the rendered DOM
  React.useLayoutEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    const list = document.getElementById(input.getAttribute("aria-controls") ?? "");
    const selected = list?.querySelector<HTMLElement>('[cmdk-item][aria-selected="true"]');
    for (const element of [input, list]) {
      if (!element) continue;
      if (selected) element.setAttribute("aria-activedescendant", selected.id);
      else element.removeAttribute("aria-activedescendant");
    }
  }, [inputRef, selection]);
}

function CommandList({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.List>) {
  return (
    <CommandPrimitive.List
      className={cn("max-h-[300px] scroll-py-1 overflow-x-hidden overflow-y-auto", className)}
      data-slot="command-list"
      {...props}
    />
  );
}

function CommandEmpty({ ...props }: React.ComponentProps<typeof CommandPrimitive.Empty>) {
  return (
    <CommandPrimitive.Empty
      className="py-6 text-center text-sm"
      data-slot="command-empty"
      {...props}
    />
  );
}

function CommandGroup({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      className={cn(
        "overflow-hidden p-1 text-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-xs [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground",
        className,
      )}
      data-slot="command-group"
      {...props}
    />
  );
}

function CommandSeparator({
  className,
  ...props
}: React.ComponentProps<typeof CommandPrimitive.Separator>) {
  return (
    <CommandPrimitive.Separator
      className={cn("-mx-1 h-px bg-border", className)}
      data-slot="command-separator"
      {...props}
    />
  );
}

function CommandItem({ className, ...props }: React.ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      className={cn(
        "relative flex cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-hidden select-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 [&_svg:not([class*='text-'])]:text-muted-foreground",
        className,
      )}
      data-slot="command-item"
      {...props}
    />
  );
}

function CommandShortcut({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      className={cn("ml-auto text-xs tracking-widest text-muted-foreground", className)}
      data-slot="command-shortcut"
      {...props}
    />
  );
}

export {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
  CommandShortcut,
};
