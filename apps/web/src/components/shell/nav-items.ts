// SPDX-License-Identifier: Apache-2.0

import { MessageCircle } from "lucide-react";

/**
 * Primary navigation. Each feature plan appends its own entry (schedules,
 * skills, settings follow chat).
 */
export const navItems = [
  {
    key: "chat",
    label: "Chats",
    to: "/workspace/chat",
    icon: MessageCircle,
  },
] as const;
