---
matlatl: orphan-intentional
paths:
  - "docs/drafts/session-history/**"
---
# Session-history terminology

Follow the shared vocabulary in `docs/drafts/session-history/vocabulary.md`.
Use model request/response (or LLM request/response) for LLM interaction,
tool call/result for tool execution, and API command/receipt for client controls.
A model response can request tool calls; those are not model calls.
Qualify request, call, response, result, and identity where these concepts meet.
Write "model request or tool call", not "tool/model request".
Preserve actual API/type names and attributed source quotations; clarify their
meaning in surrounding prose rather than silently renaming them.
Edit Modelith YAML and regenerate its Markdown; never hand-edit generated models.
