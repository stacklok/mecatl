"use client";

import { Bot } from "lucide-react";
import { Input } from "@/components/ui/input";
import { useAgentAvatar, useAgentDisplayName } from "@/lib/profile-preferences";
import { AvatarPicker } from "../_components/avatar-picker";
import { RuntimeBehaviourSection } from "../_components/runtime-behaviour-section";
import { SettingsCard, SettingsRow } from "../_components/settings-card";

/**
 * Settings → Agent: how the agent appears in chat — its name and picture,
 * both stored in this browser only — followed by the agent-wide behaviour
 * card that applies to everyone using the agent.
 */
export default function AgentSettingsPage() {
  const { name, setName, defaultName } = useAgentDisplayName();
  const { avatarUrl, setAvatarUrl } = useAgentAvatar();

  return (
    <>
      <SettingsCard title="Agent" description="How the agent appears in chat.">
        <div className="divide-y divide-border/60">
          <SettingsRow
            label="Agent name"
            htmlFor="agent-display-name"
            description="Shown on the agent's replies."
          >
            <Input
              id="agent-display-name"
              value={name}
              placeholder={defaultName}
              onChange={(event) => setName(event.target.value)}
              maxLength={40}
              className="w-44 min-[500px]:w-60"
            />
          </SettingsRow>
          <SettingsRow
            label="Picture"
            description="Shown next to the agent's replies."
          >
            <AvatarPicker
              avatarUrl={avatarUrl}
              onChange={setAvatarUrl}
              alt={name}
              fallback={<Bot className="size-6" />}
            />
          </SettingsRow>
        </div>
      </SettingsCard>
      {/* Behaviour shared by everyone using this agent. The browser-only
          "Queue only" Enter preference lives on Settings → Personalize. */}
      <RuntimeBehaviourSection />
    </>
  );
}
