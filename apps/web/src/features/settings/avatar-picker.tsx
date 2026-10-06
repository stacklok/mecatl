// SPDX-License-Identifier: Apache-2.0

import { X } from "lucide-react";
import { type ReactNode, useRef, useState } from "react";
import { Button } from "../../components/ui/button";
import { scaledAvatarSize } from "./avatar-utils";

export function AvatarPicker({
  alt,
  avatarUrl,
  fallback,
  onChange,
}: {
  alt: string;
  avatarUrl: string;
  fallback: ReactNode;
  onChange: (value: string) => void;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string>();

  async function onFileChange(file?: File) {
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      setError("Choose an image file.");
      return;
    }
    setError(undefined);
    try {
      onChange(await downscaleAvatar(file));
    } catch {
      setError("That image could not be read.");
    }
  }

  return (
    <div>
      <div className="flex flex-wrap items-center gap-4">
        <div className="group relative size-14 shrink-0">
          <div className="flex size-full items-center justify-center overflow-hidden rounded-full bg-muted text-muted-foreground">
            {avatarUrl ? (
              <img alt={alt} className="size-full object-cover" src={avatarUrl} />
            ) : (
              fallback
            )}
          </div>
          {avatarUrl && (
            <button
              aria-label={`Remove ${alt} picture`}
              className="absolute -right-5 -top-5 flex size-11 items-center justify-center rounded-full text-muted-foreground opacity-100 transition-opacity hover:text-foreground focus-visible:outline-2 focus-visible:outline-brand min-[500px]:opacity-0 min-[500px]:focus-visible:opacity-100 min-[500px]:group-hover:opacity-100"
              onClick={() => onChange("")}
              type="button"
            >
              <span className="flex size-6 items-center justify-center rounded-full border bg-background shadow-sm">
                <X aria-hidden="true" className="size-3" />
              </span>
            </button>
          )}
        </div>
        <input
          accept="image/*"
          className="hidden"
          onChange={(event) => {
            void onFileChange(event.target.files?.[0]);
            event.target.value = "";
          }}
          ref={inputRef}
          type="file"
        />
        <Button
          className="min-h-11"
          onClick={() => inputRef.current?.click()}
          size="sm"
          variant="outline"
        >
          {avatarUrl ? "Change picture" : "Upload picture"}
        </Button>
      </div>
      {error && <p className="mt-2 text-xs text-destructive">{error}</p>}
    </div>
  );
}

async function downscaleAvatar(file: File): Promise<string> {
  const objectUrl = URL.createObjectURL(file);
  try {
    const image = await loadImage(objectUrl);
    const size = scaledAvatarSize(image.naturalWidth, image.naturalHeight);
    const canvas = document.createElement("canvas");
    canvas.width = size.width;
    canvas.height = size.height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Canvas is unavailable");
    context.fillStyle = "#ffffff";
    context.fillRect(0, 0, size.width, size.height);
    context.drawImage(image, 0, 0, size.width, size.height);
    return canvas.toDataURL("image/jpeg", 0.85);
  } finally {
    URL.revokeObjectURL(objectUrl);
  }
}

function loadImage(source: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error("Image could not be decoded"));
    image.src = source;
  });
}
