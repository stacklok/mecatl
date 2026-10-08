---
slug: /features/ask-about-mecatl
sidebar_position: 20
title: Ask Mecatl about itself
description:
  Ask a running agent how Mecatl works and get an answer grounded in this
  documentation.
---

# Ask Mecatl about itself

Ask a running agent how Mecatl works. It carries a short product overview and
knows where to find this documentation. For example, ask "what modes are
available in `mecatui`?" or "what does posture `auto` allow?"

The overview covers `mecatui`, `mecated`, `mecak8s`, and `mecatequi`, so you can
ask about a specific component.

## Availability

Every ordinary session you talk to carries the account, on every deployment. It
comes from the server that builds the session rather than from a client feature
or an operator setting, so a session with no filesystem answers these questions
the same way a local `mecatui` session does.

Subagents and
[analysis sessions](/mecatui/sessions.md#diagnose-a-stored-session) are the
exception. A delegated task worker is scoped to its own job, and an analysis
session opened with `mecatui debug` is scoped to reading another session's
evidence; neither carries the account, so ask the session directly rather than
through a subagent or an analysis session.

## What the agent knows on its own

The overview explains each component, the documentation structure, and the
distinction among session permission modes, operator postures, and guardrails.
Each control has its own purpose and configuration.
[Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
owns the full treatment of both controls, and the agent points you there.

A question the account already covers gets answered from it, with no tool call
and no web search. The agent treats its own documentation as a known address to
read rather than something to search for.

The account deliberately stops at structure. Flags, defaults, and exact behavior
live in the documentation, which stays authoritative, so there is only one copy
of them to keep current.

## How the agent answers deeper questions

For anything past that structure, the agent reads the relevant page from
[the documentation site](https://mecatl.dev/docs/) and answers from what it
read, naming the page it used so you can follow up.

Reading a page needs the `WebFetch` tool, which the built-in permission rules
allow by default. A session without web access answers from the account it
carries, tells you which part it is not certain of, and names the page that
settles the question.

## Why the agent skips your workspace

Questions about Mecatl are not questions about your code, so the agent does not
search the open workspace for Mecatl's own documentation. The project you have
open is usually unrelated to Mecatl, and finding nothing in it says nothing
about how Mecatl behaves. When Mecatl's own source is the project you have open,
the agent searches it like any other codebase.

## Related information

- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
- [Capability and deployment matrix](/features/get-oriented/capability-matrix.md)
- [Configuration reference](/reference/configuration.md)
