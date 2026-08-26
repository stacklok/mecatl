---
id: 04-provider-root-integration
title: Provider configuration across command roots and user documentation
blocked_by: [03-provider-model-inventory]
status: done
branch: plan-operator-defined-llm-providers/04-provider-root-integration
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Finish command-root integration for mecated, mecatui's embedded server, mecatequi, and
mecak8s through the shared provider configuration/bootstrap path. Preserve mecatui connect
mode as a remote client that does not resolve local provider credentials. Complete
user-facing docs for the settings/auth configuration, ensure generated docs are current,
and add an offline composition-root proof.

## Acceptance criteria

- AC5.4: mecated, embedded mecatui, mecatequi, and mecak8s construct the same configured
  custom API-key and no-auth providers; mecatui connect mode does not require local
  provider credentials.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_CompositionRoots`
