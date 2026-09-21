// SPDX-License-Identifier: Apache-2.0

import { Clock3, GraduationCap, MessageCircle } from "lucide-react";

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
  {
    key: "schedules",
    label: "Scheduled",
    to: "/workspace/schedules",
    icon: Clock3,
  },
  {
    key: "skills",
    label: "Skills",
    to: "/workspace/skills",
    icon: GraduationCap,
  },
] as const;
