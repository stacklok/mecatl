// SPDX-License-Identifier: Apache-2.0

import { Bot } from "lucide-react";
import { Input } from "../../components/ui/input";
import {
  defaultAgentName,
  useAgentAvatar,
  useAgentDisplayName,
} from "../../lib/profile-preferences";
import { AvatarPicker } from "./avatar-picker";
import { IdentityCard, IdentityField } from "./identity-settings";

/**
 * The agent's identity as this browser shows it. Whether the agent takes
 * messages while working is a daemon startup flag (`--no-steer`) the BFF
 * cannot change, so there is no switch for it here, as there is none for
 * permissions or provider configuration.
 */
export function AgentSettings() {
  const agentName = useAgentDisplayName();
  const agentAvatar = useAgentAvatar();

  return (
    <IdentityCard description="Shown on the agent's replies." title="Agent">
      <IdentityField description="What the agent calls itself." label="Agent name">
        <Input
          aria-label="Agent name"
          className="max-w-64"
          maxLength={40}
          onChange={(event) => agentName.setValue(event.target.value)}
          placeholder={defaultAgentName}
          value={agentName.value}
        />
      </IdentityField>
      <IdentityField description="Shown next to the agent's replies." label="Picture">
        <AvatarPicker
          alt={agentName.value.trim() || defaultAgentName}
          avatarUrl={agentAvatar.value}
          fallback={<Bot aria-hidden="true" className="size-6" />}
          onChange={agentAvatar.setValue}
        />
      </IdentityField>
    </IdentityCard>
  );
}
