---
sidebar_position: 10
title: Observability and resilience
description: Understand runtime signals, resilient model calls, and product metrics.
---

# Observability and resilience

Mecatl records session activity and runtime health through several channels.
The event stream retains session facts; structured diagnostics report degraded
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

Mecatl retries transient failures only before the first response content is
committed. Retryable failures include rate limits, server errors, network
errors, establishment timeouts, and malformed initial SSE frames. It does not
retry other client errors, caller cancellations, or failures after streaming
begins.

|Flag|Default|Purpose|
|-|-|-|
|`--llm-max-attempts`|`3`|Limit the initial call plus retries.|
|`--llm-breaker-threshold`|`5`|Open the breaker after consecutive transient failures. `0` disables it.|
|`--llm-breaker-cooldown`|`30s`|Wait before a half-open trial.|

A successful call resets the breaker. Permanent client errors other than 408 or
429 and caller cancellations do not count toward the threshold. Exhausted
retries produce an `ExhaustedError`; an open breaker produces a `BreakerError`.

### Timeouts

|Flag|Default|Purpose|
|-|-|-|
|`--llm-per-attempt-timeout`|`300s`|Limit connection and time to first committed content. `0` disables it.|
|`--llm-stream-idle-timeout`|`180s`|Limit the gap between later stream chunks. `0` disables it.|

An establishment timeout is retryable. A stream-idle timeout is terminal because
replaying a partially visible response could duplicate work.

### Prompt caching

Provider-side prompt caching is enabled for Anthropic, OpenAI Responses, and
OpenRouter. The current OpenAI Chat Completions route uses no cache dialect.

|Flag|Default|Purpose|
|-|-|-|
|`--no-prompt-cache`|`false`|Disable Responses cache requests and conditional Messages conversation breakpoints.|
|`--anthropic-cache-ttl`|`1h` on `anthropic`, `openrouter-anthropic` and `toolhive-anthropic`; API default (`5m`) elsewhere|Set Anthropic cache breakpoints to `5m` or `1h`.|

Use `mecatl_tokens_total` and `mecatl_cache_hit_ratio` to confirm cache use.

To stop asking for a prompt cache, run with `--no-prompt-cache`. It turns off
every Responses-side ask and the three conversation breakpoints on the Anthropic
Messages providers.

It does not turn off caching completely. On a Messages provider, the breakpoint
covering the system prompt is emitted whatever you set, so the provider is still
asked to retain that prefix for the cache lifetime. A deployment relying on a
zero-retention arrangement therefore needs `--no-prompt-cache` *and* a model
route that avoids the Messages providers: `anthropic`, `openrouter-anthropic`,
`toolhive-anthropic`, and any provider you defined with
`api_flavor: anthropic-messages`.

### Failure diagnostics

Retry decisions and breaker transitions go to structured diagnostics without raw
errors or request content. The event log also receives sanitized
`network.attempt` records. `InspectSession {"view":"network"}` returns bounded
retry and terminal decisions without prompts, credentials, or response bodies.
Each attempt also carries a bounded structural summary — whether the
provider's protocol terminal was actually observed, and a closed outcome
(complete, incomplete, stream error, or cancelled) — covering successful and
cancelled streams as well as failures, so a session that finished without
error but produced unexpected output can still be distinguished from one whose
stream was cut off or errored in transport.

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

- [Collect metrics, traces, and diagnostics](/operating/observability.md) for endpoints and collection setup.
- [Session continuity](/features/sessions/session-continuity.md) for persistence and recovery.
- [Go embedding](/building/go/embed-engine.md) for sink integration.
