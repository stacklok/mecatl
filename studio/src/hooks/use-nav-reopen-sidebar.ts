"use client";

import { useEffect } from "react";

export function useNavReopenSidebar(setSidebarOpen: (open: boolean) => void) {
  useEffect(() => {
    const handler = () => setSidebarOpen(true);
    window.addEventListener("nav-reopen-sidebar", handler);
    return () => window.removeEventListener("nav-reopen-sidebar", handler);
  }, [setSidebarOpen]);
}
