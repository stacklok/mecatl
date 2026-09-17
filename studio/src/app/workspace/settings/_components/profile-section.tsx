"use client";

import { User } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useUserAvatar, useUserDisplayName } from "@/lib/profile-preferences";
import { AvatarPicker } from "./avatar-picker";
import { SettingsCard, SettingsRow } from "./settings-card";

/**
 * Settings → You: the user's own identity preferences, browser-local only.
 * There is no agent-side user profile to write back to, and the agent does
 * not read this name — it only labels your messages in chat, so the copy
 * says exactly that and no more. The agent's identity lives on the separate
 * Agent page.
 */
export function ProfileSection() {
  const { avatarUrl, setAvatarUrl } = useUserAvatar();
  const { name, setName } = useUserDisplayName();

  return (
    <SettingsCard title="You">
      <div className="divide-y divide-border/60">
        <SettingsRow
          label="Your name"
          htmlFor="user-display-name"
          description="Shown on your messages."
        >
          <Input
            id="user-display-name"
            value={name}
            placeholder="You"
            onChange={(event) => setName(event.target.value)}
            maxLength={40}
            className="w-44 min-[500px]:w-60"
          />
        </SettingsRow>
        <SettingsRow label="Picture" description="Shown next to your messages.">
          <AvatarPicker
            avatarUrl={avatarUrl}
            onChange={setAvatarUrl}
            alt={name || "You"}
            fallback={<User className="size-6" />}
          />
        </SettingsRow>
      </div>
    </SettingsCard>
  );
}
