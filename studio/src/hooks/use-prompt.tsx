"use client";

import { useCallback, useEffect, useRef, useState } from "react";
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

interface PromptOptions {
  title?: string;
  description?: string;
  placeholder?: string;
  defaultValue?: string;
  confirmText?: string;
  cancelText?: string;
}

export function usePrompt() {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const [resolvePromise, setResolvePromise] = useState<
    ((value: string | null) => void) | null
  >(null);
  const [options, setOptions] = useState<PromptOptions>({});
  const inputRef = useRef<HTMLInputElement>(null);

  const prompt = useCallback(
    (opts: PromptOptions = {}): Promise<string | null> => {
      return new Promise((resolve) => {
        setOptions(opts);
        setValue(opts.defaultValue ?? "");
        setOpen(true);
        setResolvePromise(() => resolve);
      });
    },
    [],
  );

  useEffect(() => {
    if (open) {
      const timer = setTimeout(() => inputRef.current?.focus(), 0);
      return () => clearTimeout(timer);
    }
  }, [open]);

  const handleConfirm = useCallback(() => {
    if (!value.trim()) return;
    setOpen(false);
    if (resolvePromise) {
      resolvePromise(value.trim());
      setResolvePromise(null);
    }
  }, [resolvePromise, value]);

  const handleCancel = useCallback(() => {
    setOpen(false);
    if (resolvePromise) {
      resolvePromise(null);
      setResolvePromise(null);
    }
  }, [resolvePromise]);

  const PromptDialog = (
    <Dialog open={open} onOpenChange={(v) => !v && handleCancel()}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{options.title || "Enter a value"}</DialogTitle>
          {options.description && (
            <DialogDescription>{options.description}</DialogDescription>
          )}
        </DialogHeader>
        <Input
          ref={inputRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={options.placeholder}
          onKeyDown={(e) => {
            if (e.key === "Enter") handleConfirm();
          }}
        />
        <DialogFooter>
          <Button variant="outline" onClick={handleCancel}>
            {options.cancelText || "Cancel"}
          </Button>
          <Button onClick={handleConfirm} disabled={!value.trim()}>
            {options.confirmText || "Create"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );

  return { prompt, PromptDialog };
}
