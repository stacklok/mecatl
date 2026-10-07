---
sidebar_position: 10
title: Observability and resilience
description:
  Understand runtime signals, resilient model calls, and product metrics.
---

# Observability and resilience

Mecatl records session activity and runtime health through several channels. The
event stream retains session facts; structured diagnostics report degraded
integrations and operational failures. Metrics summarize activity, traces follow
execution, and tool audit records account for individual calls and their timing.

Embedders inject the sinks they need; the engine does not use a global logger.
Operators [configure collection and exporters](/operating/observability.md).
Concurrent runs that share one trace sink also share its root span; those runs
currently lack independent root correlation.

## Model-call resilience

The supplied `mecated` and `mecak8s` servers wrap model providers with retry,
circuit-breaker, establishment timeout, and stream-idle controls. Direct engine
embedders supply their own provider wrappers.

### Retry and circuit breaker

Mecatl retries transient failures while a model step has not exposed meaningful
assistant text. Reasoning, whitespace, tool assembly, usage, and provider metadata
are buffered until that boundary or a clean completion. Failures after visible
output are terminal, so recovery does not replay visible text or completed tool
calls. Direct engine embedders supply their own recovery policy.

|Flag|Default|Purpose|
|-|-|-|
|`--llm-recovery-budget`|`30m`|Limit recovery of one precommit model step after its first retryable failure or breaker rejection. `0` disables additional waiting.|
|`--llm-max-attempts`|`60`|Limit calls for one precommit step, including the initial call.|
|`--llm-breaker-threshold`|`5`|Open the breaker after consecutive transient establishment failures. `0` disables it.|
|`--llm-breaker-cooldown`|`30s`|Wait before one half-open probe.|

These are server-owned command-line controls; `settings.yaml` has no equivalent
recovery-policy keys. Connected clients use the server's policy. Review
[per-step limits and provider costs](/features/sessions/choose-models.md#a-provider-error-ended-a-model-step)
before raising the defaults: discarded attempts can still be billed, and each
model step has its own limits.

A successful call resets the breaker. Permanent client errors other than 408 or
429 and caller cancellations do not count toward the threshold. Exhausted
recovery produces an `ExhaustedError`; an open breaker produces a `BreakerError`.

### Timeouts

|Flag|Default|Purpose|
|-|-|-|
|`--llm-per-attempt-timeout`|`300s`|Limit connection and time to the first raw chunk. `0` disables it; an active stream is not interrupted by this timer.|
|`--llm-stream-idle-timeout`|`180s`|Limit gaps between raw chunks after activity starts. `0` disables it.|

Raw stream activity resets the idle watchdog even before semantic output becomes
visible. Establishment timeouts and precommit idle stalls can recover. A failure
or idle timeout after visible output is terminal.

### Prompt caching

Provider-side prompt caching is enabled for Anthropic, OpenAI Responses, and
OpenRouter. The current OpenAI Chat Completions route uses no cache dialect.

|Flag|Default|Purpose|
|-|-|-|
|`--no-prompt-cache`|`false`|Disable Responses cache requests and conditional Messages conversation breakpoints.|
|`--anthropic-cache-ttl`|`1h` on `anthropic`, `openrouter-anthropic` and `toolhive-anthropic`; API default (`5m`) elsewhere|Set Anthropic cache breakpoints to `5m` or `1h`.|

Use `mecatl_tokens_total` and `mecatl_cache_hit_ratio` to confirm cache use.

`--no-prompt-cache` disables Responses cache requests and the three conversation
breakpoints on Anthropic Messages providers. Messages providers still emit a
system-prompt breakpoint, which asks the provider to retain that prefix for the
cache lifetime.

For a zero-retention arrangement, use `--no-prompt-cache` and a model route that
avoids Messages providers: `anthropic`, `openrouter-anthropic`,
`toolhive-anthropic`, and custom providers with
`api_flavor: anthropic-messages`.

### Failure diagnostics

Retry decisions and breaker transitions go to structured diagnostics without raw
errors or request content. The event log also receives sanitized
`network.attempt` records. `InspectSession {"view":"network"}` returns bounded
retry and terminal decisions without prompts, credentials, or response bodies.
Each attempt also records whether the provider's protocol terminal was observed
and an outcome: complete, incomplete, stream error, or cancelled. These bounded
summaries cover successful and cancelled streams as well as failures. Use them
to distinguish unexpected model output from an interrupted or failed transport.

## Anonymous product metrics

Mecatl separately reports aggregate adoption metrics to Stacklok by default.
These metrics include version, OS and architecture, a random installation ID,
enabled feature families, provider family, binary name, and coarse counts and
durations. Tool names are limited to built-ins or the category `mcp`.

The report excludes prompts, file paths, MCP server and tool names, session and
run IDs, model IDs, and other free text. A one-time stderr notice appears before
the first report.

Disable product metrics with any of these controls:

- `--product-metrics=false`
- `MECATL_PRODUCT_METRICS=false`
- A truthy `DO_NOT_TRACK` value
- `telemetry.productMetrics.enabled: false` in user-global settings

`MECATL_PRODUCT_METRICS=true` overrides `DO_NOT_TRACK`. Project settings cannot
change the operator's choice. Use `--product-metrics-dry-run` to print the
observations instead of sending them.

## Related information

- [Collect metrics, traces, and diagnostics](/operating/observability.md) for
  endpoints and collection setup.
- [Session continuity](/features/sessions/session-continuity.md) for persistence
  and recovery.
- [Go embedding](/building/go/embed-engine.md) for sink integration.
