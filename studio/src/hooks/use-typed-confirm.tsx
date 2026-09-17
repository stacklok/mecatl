"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

export interface TypedConfirmOptions {
  title: string;
  description?: string;
  /**
   * The exact phrase the person must type. Compared byte-for-byte —
   * case-sensitive, no trimming — so "clean up" or "CLEAN UP " never
   * unlocks the button.
   */
  phrase: string;
  confirmText?: string;
  cancelText?: string;
  /** Styles the confirm button as destructive; on by default, since a
   *  typed phrase exists to slow down an irreversible action. */
  destructive?: boolean;
}

/**
 * `useConfirm`'s sibling for irreversible bulk actions: the same
 * promise-returning dialog, plus a text field the person must fill with an
 * exact phrase before the confirm button enables. Closing the dialog any
 * other way (Cancel, Escape, the overlay) resolves false.
 */
export function useTypedConfirm() {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const [options, setOptions] = useState<TypedConfirmOptions | null>(null);
  const resolveRef = useRef<((confirmed: boolean) => void) | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const inputId = useId();
  const hintId = useId();

  const confirmTyped = useCallback(
    (opts: TypedConfirmOptions): Promise<boolean> =>
      new Promise((resolve) => {
        // A dialog re-opened over an unanswered one answers the first "no".
        resolveRef.current?.(false);
        resolveRef.current = resolve;
        setOptions(opts);
        setValue("");
        setOpen(true);
      }),
    [],
  );

  useEffect(() => {
    if (!open) return;
    const timer = setTimeout(() => inputRef.current?.focus(), 0);
    return () => clearTimeout(timer);
  }, [open]);

  const settle = useCallback((confirmed: boolean) => {
    setOpen(false);
    const resolve = resolveRef.current;
    resolveRef.current = null;
    resolve?.(confirmed);
  }, []);

  const matches = options !== null && value === options.phrase;

  const handleConfirm = useCallback(() => {
    if (!matches) return;
    settle(true);
  }, [matches, settle]);

  const TypedConfirmDialog = (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) settle(false);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {options?.title ?? "Are you sure?"}
          </AlertDialogTitle>
          {options?.description && (
            <AlertDialogDescription>
              {options.description}
            </AlertDialogDescription>
          )}
        </AlertDialogHeader>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={inputId} className="text-sm">
            Type{" "}
            <code className="font-mono font-semibold">{options?.phrase}</code>{" "}
            to confirm
          </Label>
          <Input
            ref={inputRef}
            id={inputId}
            value={value}
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                handleConfirm();
              }
            }}
            aria-describedby={hintId}
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
            className="font-mono"
          />
          <p id={hintId} className="text-xs text-muted-foreground">
            Exactly as shown — capitals matter.
          </p>
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={() => settle(false)}>
            {options?.cancelText ?? "Cancel"}
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={handleConfirm}
            disabled={!matches}
            className={cn(
              (options?.destructive ?? true) &&
                "bg-destructive text-white hover:bg-destructive/90",
            )}
          >
            {options?.confirmText ?? "Confirm"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  return { confirmTyped, TypedConfirmDialog };
}
