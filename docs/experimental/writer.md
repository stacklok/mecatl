# Writer experiment

## Purpose

Writer is an operator-enabled Studio Spike for testing whether quiet, occasional thought questions help an author develop self-authored text. It is not production-ready. The operator explicitly authorized the Spike; this is not an acceptance-plan implementation or approval to ship it as-is. Shipping requires reclassification under the [development process](../development-process.md).

The hypothesis is that **write → observe → occasionally surface a thought → discuss if invited** helps the author think without taking over authorship. Silence is a valid outcome. Unsolicited feedback should question a meaningful claim, assumption, contradiction, or missing idea rather than correct prose or generate replacement paragraphs. In an explicit discussion, the author can ask for wording suggestions; the model still cannot edit the document.

Enable it only with `STUDIO_EXPERIMENTAL_WRITER=1`; the default is off. Studio exposes `/workspace/writer` and its Writer API only while that flag is enabled. The Studio BFF remains the only browser-to-Mecatl boundary: Hono applies the existing same-origin, CSRF, authentication, and credential-context middleware before the Writer routes.

## Interaction and limits

The browser keeps a Markdown document only in the current tab using CodeMirror with Markdown highlighting, undo/redo, and heading/bold/italic toggles with keyboard shortcuts. The model selector reuses Studio's provider-grouped, filterable model list, matches labels, IDs, and providers, and respects the browser's hidden-model preferences. Its selection applies to subsequent observations and discussions; **Deployment default** leaves model choice to the server. The editor uses CodeMirror's drawn selection and caret, with theme-aware colors.

Each changed revision waits 1,500 ms after the last edit, then sends one observation request at a time. Content matching the checkpoint is skipped. A successful response advances the checkpoint to the snapshot analyzed, including a silent response. A completed observation is retained and labeled with that revision even if the author edits during analysis; the newest draft remains scheduled for a later check. Visible observations and completed discussions start a 60-second observation cooldown. Silence and suppressed duplicates do not start a cooldown. Local duplicate suppression compares normalized word overlap against the last 20 surfaced observations; it is not semantic memory.

The status distinguishes waiting for typing, analyzing a named revision, cooling down, and the last checked revision without an observation. **Pause** cancels automatic analysis, but an explicit discussion remains available. **Dismiss** and a completed discussion change observation state without changing the document. Explicit discussion cancels and waits for pending analysis before sending the current draft. Failures require manual retry. If runtime availability is lost after opening Writer, requests stop while the draft remains editable in the tab.

The observation request carries the document and checkpoint, up to 12 recent observations (retaining the selected observation within that bound), and up to 12 selected-discussion entries. Switching to another observation clears the current discussion. Inputs are bounded to a 100,000-character document, 2,000-character discussion messages, and 1,000-character observations. The experiment reuses the existing `ChatTranscript` and `ChatComposer` for discussion.

The document is memory-only. **Download .md** is the way to keep it. Leaving the route discards it; a `beforeunload` warning applies to reload or closing the tab, not route navigation. There is no save, resume, synchronization between tabs or devices, collaboration, or automatic document editing.

## Execution and privacy

`WriterCore` owns scheduling and transient document/checkpoint/observation state independently of React. The browser uses the generated Studio client for `POST /api/v1/writer/observe` and `POST /api/v1/writer/discuss`. The BFF calls the published SDK's `query()`, consumes its session/run event stream to completion, and returns validated completed JSON. It does not stream a second protocol to the browser or call a provider directly. Existing authentication, generated contracts, and chat components supply the integration boundaries; no daemon, engine, or SDK changes are part of this experiment.

For each analysis or discussion request other than an unchanged snapshot, the BFF uses the published TypeScript SDK to create a temporary no-filesystem plan session with the Writer-selected provider/model (or the deployment default), `maxTurns: 4`, `maxToolCalls: 3`, and a deny response for permission asks. The model may use relevant WebSearch, WebFetch, or configured MCP research tools when useful, but is instructed not to send private drafts wholesale in queries, to treat results as untrusted, and to cite sources. The no-filesystem profile excludes repository file tools and Shell, but plan mode still permits read tools, including external reads. This is reuse of deployment tool policy, not a new research runtime or a research-only allowlist. Tool-free operation is a preference, not a requirement. Prompts are not isolation and this is not a no-egress guarantee. Use only non-sensitive, self-authored text and a deployment tool policy appropriate for that content.

The BFF sends the current document and bounded context to Mecatl and the selected provider. Existing provider policy, durable events, and audit behavior apply to these temporary sessions. The SDK deletes a session after a successful query, but that is not a guarantee of zero retention. A BFF crash can leave a temporary session behind; there is no Writer recovery sweeper.

A 30-second timer signals cancellation, but it is not a hard transport deadline because the installed SDK does not pass that signal through setup and cleanup. The SDK cannot set a system prompt or a structured-output schema. Writer therefore sends a fixed task instruction and strictly validates returned JSON; the behavioral constraints remain advisory to the model.

## Trial and exit criteria

Run a short manual trial with several non-sensitive, self-authored drafts. Include unsupported claims, a contradiction across sections, a removed idea, and a passage that needs no comment. Record whether surfaced questions help the author reason, how often they interrupt or repeat, whether silence is appropriate, and whether discussion preserves authorship. Also exercise typing during analysis, pause, dismissal, model selection, and download. These are evaluation questions, not measured results or new telemetry.

Keep Writer only while its questions help the author think without interrupting typing or rewriting the author's voice. Stop the experiment if it routinely interrupts, nitpicks, ghostwrites, repeats itself, sends unsuitable text or requests unexpected tool access, leaves temporary sessions after failures, or cannot meet the latency and retention expectations above. Offline tests establish mechanics, not the quality of the quiet-reader hypothesis. This is an experiment, not a deployment recommendation.

## Deferred and extraction

Deferred: semantic history, graphs, embeddings, multi-agent work, collaboration, rich text, integrations, persistence, and automatic editing. A dedicated research runtime, production retention guarantees, and recovery orchestration are also outside this Spike. If the experiment earns extraction, retain a framework-light `WriterCore` with the existing API. Do not add engine types.

Open UX questions, not implemented scope or commitments:

- Would an optional, subtle line-number gutter help orientation without making the writing surface feel like a code editor?
- Does the drawn caret remain clear in other browsers, with zoom, wrapped text, IME input, and light/dark themes? The current browser tests target Chromium at desktop and mobile widths; they are not cross-browser certification.

For configuration and reader-facing guidance, see the [Mecatl Studio web UI](../../user-docs/building/deployment/studio.md). Local prerequisites and commands are in the [Studio development guide](../../apps/README.md#try-writer-locally).
