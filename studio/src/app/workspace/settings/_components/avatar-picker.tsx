"use client";

import { X } from "lucide-react";
import { useRef } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

/** Longest edge of a stored avatar; they render at 56px, so this is ample. */
const AVATAR_MAX_DIM = 512;

/**
 * Downscale a picked image in the browser so any size of upload fits
 * comfortably in local storage: longest edge capped at AVATAR_MAX_DIM,
 * re-encoded as JPEG (composited over white — JPEG has no alpha).
 */
async function downscaleAvatar(file: File): Promise<string> {
  const url = URL.createObjectURL(file);
  try {
    const img = await new Promise<HTMLImageElement>((resolve, reject) => {
      const el = new Image();
      el.onload = () => resolve(el);
      el.onerror = () => reject(new Error("undecodable image"));
      el.src = url;
    });
    const scale = Math.min(
      1,
      AVATAR_MAX_DIM / Math.max(img.naturalWidth, img.naturalHeight),
    );
    const width = Math.max(1, Math.round(img.naturalWidth * scale));
    const height = Math.max(1, Math.round(img.naturalHeight * scale));
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("canvas unavailable");
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, width, height);
    ctx.drawImage(img, 0, 0, width, height);
    return canvas.toDataURL("image/jpeg", 0.85);
  } finally {
    URL.revokeObjectURL(url);
  }
}

/**
 * A stored-picture control: circular preview (or the given fallback),
 * upload/change via the file picker with in-browser downscaling, and remove.
 * Backed by any of the browser-local avatar preferences.
 */
export function AvatarPicker({
  avatarUrl,
  onChange,
  alt,
  fallback,
}: {
  avatarUrl: string | null;
  onChange: (next: string | null) => void;
  alt: string;
  /** Rendered inside the circle when no picture is stored. */
  fallback: React.ReactNode;
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);

  function handleFileChange(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      toast.error("Choose an image file.");
      return;
    }
    downscaleAvatar(file)
      .then(onChange)
      .catch(() => toast.error("Could not read that image."));
  }

  return (
    <div className="flex items-center gap-4">
      <div className="group relative size-14 shrink-0">
        <div className="flex size-full items-center justify-center overflow-hidden rounded-full bg-muted text-muted-foreground">
          {avatarUrl ? (
            // biome-ignore lint/performance/noImgElement: a locally stored data URL, not a remote image
            <img src={avatarUrl} alt={alt} className="size-full object-cover" />
          ) : (
            fallback
          )}
        </div>
        {/* Remove lives on the picture itself: hover (or keyboard focus)
            reveals a small ×; without a picture there is nothing to remove. */}
        {avatarUrl && (
          <button
            type="button"
            aria-label="Remove picture"
            onClick={() => onChange(null)}
            className="absolute -top-1 -right-1 flex size-5 items-center justify-center rounded-full border bg-background text-muted-foreground opacity-0 shadow-sm transition-opacity group-hover:opacity-100 hover:text-foreground focus-visible:opacity-100"
          >
            <X className="size-3" />
          </button>
        )}
      </div>
      <div className="flex items-center gap-2">
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          className="hidden"
          onChange={handleFileChange}
        />
        <Button
          variant="outline"
          size="sm"
          className="rounded-full"
          onClick={() => fileInputRef.current?.click()}
        >
          {avatarUrl ? "Change picture" : "Upload picture"}
        </Button>
      </div>
    </div>
  );
}
