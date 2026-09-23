---
matlatl: orphan-intentional
paths:
  - "cmd/**"
  - "internal/app/**"
  - "internal/adapter/**"
  - "engine/adapter/**"
  - "engine/port/**"
---
# Log delivery review and tests

When changing logging or sink composition, trace the production caller through sink selection, delivery, and cleanup. Test a record arriving at the actual destination (including fallback paths), not just a configured logger or helper result. Cover concurrent owners, relevant open/write/retention failures, and lock/resource release; distinguish explicit quiet mode from accidental loss.

Where the existing contract requires a failure or fallback notice, verify it through an independent usable reporting channel, including when later startup fails or blocks. A notice sent only to the unavailable sink is not delivery. Use isolated offline fixtures and deterministic failure or synchronization seams; mutation-check absence and ordering assertions at the caller, not source-text checks.

Keep operational `port.Diagnostics`, the audit `port.ToolCallRecorder`, and durable `port.EventLog` distinct. Review each against its own delivery, durability, redaction, and failure policy. This is review/test discipline, not a claim that every sink failure currently reports or a mandate to impose one runtime policy on all sinks; escalate a missing product contract rather than redesigning it in a regression-test change.
