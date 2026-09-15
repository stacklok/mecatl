// SPDX-License-Identifier: Apache-2.0
// Copyright (c) Stacklok, Inc. All rights reserved.

import type { MetadataRoute } from "next";

// Deliberately NO service worker: Studio is a client for a live local
// `mecated` daemon, so offline caching would lie about live state (sessions,
// runs, schedules). Installability no longer requires a service worker on
// Chrome/Android, and never did on iOS — the manifest alone is enough.
export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "Mecatl Studio",
    short_name: "Studio",
    description: "The web workspace for the Mecatl agent harness",
    start_url: "/workspace/chat",
    scope: "/",
    display: "standalone",
    background_color: "#03433e",
    theme_color: "#03433e",
    icons: [
      {
        src: "/icon-192.png",
        sizes: "192x192",
        type: "image/png",
      },
      {
        src: "/icon-512.png",
        sizes: "512x512",
        type: "image/png",
      },
      {
        src: "/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ],
  };
}
