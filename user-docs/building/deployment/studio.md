---
sidebar_position: 140
title: Mecatl Studio web UI
description:
  Run the early-access Studio browser UI against a Mecatl deployment with the
  signed container image, OIDC browser login, and the local Compose setup.
---

# Mecatl Studio web UI

:::warning[Early access: expect breaking changes between releases]

Studio is under active development. Its interface, its `/api/v1` endpoints, its
configuration variables, and the data it keeps in your browser can change in any
release, with no deprecation period and no migration path. A screen, a URL, or a
setting you rely on today may be renamed or removed in the next version.

Deploy it by an exact version tag rather than `latest`, read the release notes
before upgrading, and do not build an integration against its HTTP surface yet.
The published image carries `org.stacklok.mecatl.studio.stability=early-access`
so a deployment can assert on it.

:::

Mecatl Studio is the browser client for a Mecatl deployment. It runs as one
container that serves the web app and a backend for frontend (BFF) from the same
origin. Your browser talks only to the BFF; the BFF holds your credential and
talks to `mecated` or `mecak8s` over the same gRPC listener the terminal client
uses. Studio never receives a provider API key.

Every release includes a multi-architecture image for `linux/amd64` and
`linux/arm64`:

```text
ghcr.io/stacklok/mecatl/studio:<VERSION>
```

Pin an exact release version: `latest` moves with every release, and an
early-access release can change the interface without warning.

The image is signed with keyless Cosign, carries an SPDX SBOM attestation, and
has SLSA build provenance. Verify the signature and the provenance before you
deploy it:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/stacklok/mecatl/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/stacklok/mecatl/studio:<VERSION>

gh attestation verify oci://ghcr.io/stacklok/mecatl/studio:<VERSION> --repo stacklok/mecatl
```

The Cosign command checks that a GitHub Actions workflow in the
`stacklok/mecatl` repository signed the image.

## What Studio offers

Studio exposes the shared capabilities described in the
[feature guides](/features/index.md), and it hides or disables what the
connected deployment does not enable:

- **Chats**: sessions with streamed runs, image attachments, permission
  approvals, steering, and model and reasoning-effort selection.
- **Scheduled**: [scheduled tasks](/features/scheduled-tasks.md) with a cron
  builder and fire history, when the deployment enables scheduling.
- **Skills**: configured and learned skills, learning proposals, and session
  reflection, when the deployment enables them.
- **Settings**: personal browser preferences and memory decisions alongside
  read-only facts about the connected deployment, its providers, and its models.

A global search palette and a keyboard-shortcuts reference page complete the
set. Open `/workspace/shortcuts` directly or follow **Keyboard shortcuts**
from **Settings > About**. The page lists the current browser bindings and
features enabled by the connected deployment.

## Try the Writer experiment

:::caution[Experimental]

Writer is a Spike for evaluating occasional thought questions while you write.
It is not production-ready. Enable it by setting `STUDIO_EXPERIMENTAL_WRITER=1`
on the Studio container or local Studio process, then open **Writer** from the
workspace navigation. The default is off, and the value must be exactly `1`.

:::

Use Writer with non-sensitive text that you wrote yourself. Write Markdown in the
highlighted document surface. The muted line-number gutter is visible by default;
wrapped visual lines retain the number of their Markdown line. Use **H1**, **B**,
and **I** to toggle formatting (or Ctrl/⌘+Alt+1, Ctrl/⌘+B, and Ctrl/⌘+I); **Undo**
and **Redo** affect only edits you make or explicitly accept. **Document key bindings** (the settings icon in the document toolbar) defaults to Standard; select Vim for normal/insert/visual document editing and a visible mode indicator, or select Standard to turn Vim off. The choice lasts only while this Writer workspace is open, not across navigation/reload. The conversation, brief, decision, and candidate controls stay standard. In Vim mode use the toolbar to format text; the listed formatting shortcuts apply in Standard mode. Vim uses the editor's ordinary undo/redo history, including accepted proposals. No custom mappings, vimrc, shell editor, or `:w`/`:q` save/quit integration are provided. Choose a provider/model from the deployment's
model inventory, or leave **Deployment default** selected. Filter by model name,
ID, or provider; models hidden in your browser's **Models** settings remain hidden.
The choice applies to subsequent checks and discussions.

Select the outlined **Add brief** button above the document if you want to describe
its audience, purpose, or the feedback you want. Apply, edit, or clear this optional
brief at any time. Changing it does not immediately analyze or edit your draft;
select **Read this now** to check the current text against the new brief. Writer
waits for meaningful edits and a quiet interval between automatic checks, including
when the last check was silent. Turn off **Automatic feedback** to run checks only
when you select **Read this now**. **Read this now** and **Ask Writer** work while
automatic feedback is off and before any observation appears.

Open observations remain in the active list. **Addressed** and **Not relevant** move threads into collapsed **History**; expand it to revisit or reopen them. Select **Open thread** to revisit a question and its conversation, then use **Back to conversation** to return to the default composer without making a request. Discussion alone does not close it. The general conversation is separate from these threads. To retain an explicit outcome, enter it under **Decision for this draft** and select **Save decision**. Writer uses saved decisions in future feedback without changing the document. You can edit or clear a saved decision. Up to 100 decisions stay in context even when their observations are old; clear one before adding another at the limit. Writer does not infer such decisions. A quote under an observation can be revealed in the current document: **Reveal passage** scrolls to the first match without moving the caret or changing undo history; automatic feedback never scrolls. Missing or repeated passages are marked as earlier-draft/changed rather than linked to a guessed location.
Ask explicitly in the existing observation or general conversation if you want a revision. Select one contiguous passage in the editor to supply exact context (Vim character and linewise visual selections work, including backwards selections; visual blocks or multiple ranges show a message and cannot be used as one passage), or discuss an observation with one uniquely matching quote. A selection or reference file alone never asks Writer to edit. Missing, repeated, or multi-quote anchors require you to select the passage yourself. Writer can also answer without proposing text. A candidate appears in the originating discussion with the original passage and an editable replacement; continue discussing, request a refined candidate, **Apply** or **Discard**. Applying is one undoable edit, does not close an observation or confirm a decision, and is blocked if the draft, brief, or references change.

For an empty document, ask Writer in the general conversation for an outline (the default), organization of supplied material, or a rough draft. Edit the starting-point preview and choose **Use this starting point** or **Discard**; typing into the document before adoption prevents replacement. Previews are transient and are not restored. Accepted text follows ordinary undo and recovery settings. Writer does not continue automatically after adoption.

To start with a local Markdown or plain-text file, select **Open document…** in the **Document** toolbar (not **Add context**). Choose a UTF-8 `.md`, `.markdown`, or `.txt` file up to 400,000 bytes and 100,000 characters. Opening a valid file replaces the editor document; if your draft is nonempty, Writer asks before replacing it. Canceling, invalid files and edits made while the file loads leave your draft intact. On success the prior brief, observations, discussions, decisions, selected passage, pending preview and attached references are cleared; the old document cannot be brought back with Undo. Edits made *after* opening remain undoable. Your Standard/Vim selection stays in place. Opening alone makes no model request; edit or choose **Read this now** when you want feedback. This is a browser-local snapshot, not repository access or a linked file. **Download .md** exports it separately and never overwrites the source file.

You can explicitly attach local text, Markdown or source-code snapshots from the Writer composer’s **Add context** menu (three files, up to 8,000 UTF-8 bytes each and 16,000 bytes total). The same menu can include the current editor selection; both references and the selected passage have remove controls before sending. File names and remove controls show what is currently attached. Choosing a file does not send it immediately or change the document; **subsequent Writer requests send its contents to the configured provider**, including explicit reads while a snapshot is attached. References are untrusted evidence, not instructions or permission to edit. They are not linked to local files or repository access, and are not restored after reload/navigation, even if you opted into draft recovery. Remove a file to exclude it from future requests; changes invalidate a pending candidate. Use only non-sensitive files and verify all model output.
If a request fails, retry it manually; if Writer becomes unavailable, your
current tab remains editable.

The draft stays in this tab until you choose **Save locally (opt in)**. With
browser storage and Web Locks available under a verified signed-in Studio
account, Writer then saves your draft, applied brief, threads, and decisions
together. Recovery is unavailable when browser login is disabled or this tab's
account no longer matches browser storage, including pending account cleanup.
Use **Download .md** in those cases. Check **Saved in this browser** before leaving. **Saving…** and
**Save failed** do not indicate a recoverable copy; if storage is blocked, full,
invalid, or changed in another tab, download the draft before leaving. A
recovered draft opens only for the same account on the same browser origin;
recovery itself does not check the draft until your next edit or **Read this now**.
Studio's existing account cleanup clears it on sign-out or account change.
**Forget local draft** asks for confirmation, removes the saved copy, and stops
future saves without clearing the current tab. It refuses to delete a copy changed
by another tab. Invalid saved data stays untouched until you confirm **Forget local draft**;
new edits do not overwrite it. **Download .md** is independent of recovery. Browser-local storage
is not a backup or cross-device sync; route departure loses anything not saved or
downloaded.

Writer sends the current document and bounded recent context through Studio to
the connected Mecatl deployment and the selected provider (or the deployment
default). Its temporary no-filesystem plan sessions use the deployment's existing
tool policy. Writer can use external research tools when relevant, including
WebSearch, WebFetch, and configured MCP tools. It asks for source citations and
limits search queries, but it is not a no-egress boundary. Existing Mecatl
provider policy, durable events, and audit behavior apply; deletion after a
successful request does not guarantee zero retention. Do not use Writer for
sensitive content.

For limits, failure behavior, and manual trial stop conditions, see the
[Writer experiment design](https://github.com/stacklok/mecatl/blob/main/docs/experimental/writer.md).

## Inspect delegated activity

When a run delegates work, its transcript shows an activity card for each
observed Subagent, Parallel group, or Team member. Select a card to open that
work in the activity panel. Select **Activity** in the chat header to open the
session roster instead. The panel opens only when you select one of these
controls.

Use the **Subagents**, **Parallel**, and **Teams** tabs to inspect observed
states, tool summaries, branch winners, team tasks and findings, and stop
reasons. A selected child, branch, or member shows its 12 most recent trace
entries and the number of older entries omitted. If replay history is
incomplete, Studio marks it as incomplete and leaves unobserved outcomes
unknown. Close the panel with Escape or its close button to return focus to the
control that opened it.

## Review settings

Open `/workspace/settings/profile` for personal preferences. Every settings
section has its own URL, so you can bookmark or reload it. The section list is
available in the desktop sidebar and the mobile selector.

|Section|Source|Owner and available action|
|-|-|-|
|Profile|Browser preferences; signed-in account from the BFF session|Personal display choices; account identity is read-only.|
|Appearance|Browser preferences|Personal theme and interface choices.|
|Agent|Browser display preferences; BFF runtime capabilities|Personal name and avatar; deployment-managed behavior is read-only.|
|Permissions|BFF runtime capability|Deployment-managed posture, read-only.|
|Providers|BFF settings inventory and provider detail|Deployment-managed provider facts, read-only.|
|Models|BFF settings inventory; browser visibility preference|Personal picker visibility; deployment-managed default and routing are read-only.|
|MCP tools|BFF runtime capabilities|Deployment-managed availability, read-only.|
|Storage|BFF storage health|Deployment-managed health, read-only.|
|Memory|BFF user-memory reads|Personal facts and approved consolidation actions; store settings are deployment-managed.|
|Learning|BFF learning and reflection reads|Personal proposal decisions; learning configuration is deployment-managed.|
|Diagnostics|BFF runtime, settings inventory, and storage health|Deployment-managed facts, read-only.|
|Labs|Studio availability and BFF runtime capabilities|Deployment-managed availability, read-only.|
|About|BFF runtime, settings inventory, and sign-in session|Deployment and Studio build facts are read-only; the signed-in user can sign out.|

**Providers** shows each provider's status and model count. A provider detail
page shows its models and a sanitized display endpoint when the daemon reports
one. **Models** shows IDs, providers, context limits, and image and reasoning
support. The **Visible** switch affects model pickers in this browser. Provider
credentials, the default model, and routing remain under deployment control.

**Memory** lists the user's facts. A fact detail opens at
`/workspace/memory?item=<KEY>`; existing links using
`/workspace/settings/memory?item=<KEY>` lead to the same fact. A key containing
`/` stays intact in the `item` query value.

**About** identifies three separate builds: the Studio image release tag,
the installed TypeScript SDK version, and the connected daemon's build ID. It
also shows the daemon implementation, runtime source, connection, and deployment
label reported by the BFF. A fact the BFF cannot report appears as **Not
reported**. Local Studio builds have no image release tag. The **Copy support
summary** action includes these displayed build and runtime facts. About links
to this documentation, problem reporting, and the shortcuts reference.

## Install Studio in your browser

Open your Studio URL over HTTPS in a browser that offers app installation, then
choose the browser's **Install app** command. On a local workstation, the
loopback URL in the Compose setup also qualifies. Launch the installed Studio
from your app list; it opens Chats at `/workspace/chat`.

The installed window loads Studio from its origin and needs a connection to the
BFF and Mecatl deployment for agent actions. A loaded window reports an
unavailable connection if that link drops. For a fully offline launch, the
browser handles the missing connection; Studio does not provide an offline
starting page.

## Set your appearance

1. Open **Settings**, then **Personalise**.
1. Under **Appearance**, choose **Light**, **Dark**, or **System** for the theme.
   System follows your device's light or dark setting.
1. Choose **Default**, **Aztec**, **Mono**, or **Solar** for the palette. The
   palette choice does not change your theme.

Studio applies both choices when the page loads and updates other open Studio
tabs on the same origin. The default is the System theme with the Stacklok green
palette. If browser storage is unavailable, your choices work until you reload
the page. Inter and Merriweather fonts load from Studio's own origin.

## Use a chat

Open **Chats**, choose an available model, reasoning effort, and permission
mode, then write a message and select **Send**. You can also set tool access
before starting a chat. In an existing chat, change the permission mode in the
chat controls; choosing another model creates a fork of that chat. Press Enter
to send or Shift+Enter for a new line. Enter used to confirm an input method
candidate leaves the message in the composer.

Starter prompts fill the composer for you to review. A link with `?prompt=` also
fills it without starting a run. If the link includes `send=1`, Studio shows the
prompt, target chat, model, and permission mode before you select **Send prompt**
or **Edit prompt**. For a new chat, it also shows tool access. Opening or
reloading the link never sends it automatically. If you need to sign in first,
keep the original tab open. Its prompt stays there while the popup or new tab
completes sign-in.

If the selected model supports images, use **Attach images** to add up to 16
images to one message. Each image can be at most 10 MiB, with a combined limit
of 20 MiB. Studio sends images only; other file types are not chat attachments.
You can select the microphone button to dictate in a browser that supports
speech recognition on a secure origin. Review or edit the recognized text before
sending. If recognition is unavailable or microphone access fails, Studio
explains the failure and keeps the typed draft available.

### Follow and control a run

The transcript streams messages with Markdown, code blocks, and tool activity.
Saved inline images appear in the transcript; an external image URL appears as a
link you can choose to open. You can scroll back to read earlier messages and
use **Scroll to latest message** to resume following the stream. The status
distinguishes sending, working, waiting for approval, and a recorded completed,
stopped, canceled, or failed result. If a stream closes without a recorded
result, Studio reports an unknown outcome and checks saved history when you
reconnect. A history gap does not claim that the run finished.

While Mecatl is working, the composer can queue a text message for the next run
or steer the active run. Your Interface preference chooses what Enter does;
Shift+Enter selects the other action. You can edit or remove queued messages.
Use **Stop** to cancel the active run. If a run fails and offers **Retry**, that
action retries the failed run without sending the prompt again. Image messages
can be sent after the current run ends.

If a tool needs external authorization, select **Review authorization** in the
chat. Select **Open authorization** to complete the external step in a
new tab, then return to Studio and select **Recheck**. The panel shows the
observed status. **Cancel authorization** ends the pending handoff. Opening the
external page alone does not grant access, and closing the review panel leaves
the handoff pending. Open the page from the Studio panel. The Studio
authorization link rejects address-bar navigation and links from other sites.

If Studio cannot confirm a **Recheck** or **Cancel authorization** result, the
panel disables both actions. Select **Refresh activity** to check for a later
status from the same handoff. A new pending status restores the actions; a
resolved status updates the panel. If no later status appears, the outcome
remains uncertain and the actions stay disabled.

Folders, queued messages, and account preferences survive a reload for the same
account. Studio clears them when you sign out or switch accounts. The device
theme and palette remain.

### Return to a chat

Generated chat titles appear in both the chat list and header when Studio
receives a newer title, even if generation finishes after the run. A later
operator rename takes precedence over an older generated title. If you return to
the same open chat after its tab was hidden for at least 20 seconds, Studio
refreshes its state and briefly reports only what it can verify, such as a run
still working or the chat waiting for approval. Reloading the page does not
create a return notice.

When a [scheduled task](/features/scheduled-tasks.md) delivers a start or
completion note to this chat, the transcript shows the recorded note with its
schedule and fire attribution. The note body appears as plain text. A task's
fire history alone does not add a note to the chat. An open, visible chat checks
saved history while idle, so a short delivery's reply and turns added from
another client appear even when their live activity was missed.

## Try it locally with Compose

The repository's `apps/docker-compose.yml` starts a `mecated` container and
Studio on an internal network and publishes Studio on `127.0.0.1:3100`.

1. Clone the repository and change into `apps/`.
1. Copy `.env.example` to `.env` and set one provider key, for example
   `ANTHROPIC_API_KEY`.
1. Run:

   ```sh
   docker compose up --build
   ```

1. Open [http://127.0.0.1:3100](http://127.0.0.1:3100).

The local `mecated` runs without OIDC, so Studio starts in its unauthenticated
mode and every browser that reaches it acts as the same principal. The Compose
file opts into that with `STUDIO_ALLOW_UNAUTHENTICATED=1` because it publishes
Studio on the loopback address only.

## Deploy against mecak8s

In production Studio runs behind your ingress and connects to a `mecak8s`
deployment that publishes an OIDC profile (see
[Cloud-native k8s with mecak8s](./mecak8s.md)). Users sign in through the
deployment's identity provider with Authorization Code and PKCE; Studio keeps
the session in encrypted, HttpOnly cookies and forwards the user's token on every
request.

Set these variables on the Studio container:

|Variable|Value|
|-|-|
|`MECATL_BASE_URL`|The `https://` address of the `mecak8s` gRPC listener, for example `https://mecak8s.example.com`.|
|`STUDIO_PUBLIC_URL`|The origin your users open, for example `https://studio.example.com`. Studio derives the OIDC callback `https://studio.example.com/api/v1/auth/callback` and the `Secure` cookie flag from it.|
|`STUDIO_SESSION_SECRET`|At least 32 bytes, shared by every Studio replica. Generate one with `openssl rand -base64 48`.|
|`STUDIO_TRUSTED_PROXY_HOPS`|`1` when one ingress or load balancer sits in front of Studio, so rate limiting and audit records see the client address from `X-Forwarded-For` instead of the proxy.|

Register the callback URL with your identity provider's public client. Studio
discovers the issuer, client ID, audience, and scopes from the deployment's
protected-resource document and refuses to start when that document cannot be
fetched, so a misconfigured target fails at deploy time.

`MECATL_RESOURCE_URL` is the deployment's canonical resource identifier: the
exact value its protected-resource document publishes as `resource`. Studio
fetches the document from that URL's `/.well-known/oauth-protected-resource`
path and refuses to start when the published `resource` differs from it. Set it
only when that identifier is not the gRPC address in `MECATL_BASE_URL`. The
local Compose file sets it because `mecated` serves gRPC and HTTP on separate
ports.

Studio answers only requests whose `Host` is the host of `STUDIO_PUBLIC_URL`,
so the ingress in front of it must pass the original `Host` header through.

Studio listens on port `3100` on every interface inside the image, answers
`GET /api/health` on any host name as soon as it starts, and runs as a non-root
user. Scale it horizontally without shared storage: every
replica needs only the same `STUDIO_SESSION_SECRET`.

### Sign in and recover

On your first visit, select **Sign in to Mecatl** in the centered card. If the
card reports an outage, select **Try again** after the connection returns. When
a session expires in an open workspace, use **Sign in** in the status banner.
Studio keeps the original tab, route, and unfinished draft open while the popup
completes login. If the browser blocks the popup or you close it, use **Retry**
or the manual new-tab sign-in link. Return to the Studio tab after signing in
through the new tab so it can check your session.

If your session expires while Studio is open, sign in again. Studio refetches
reads after you return to the same account. Retry a write or stream yourself
because the first request may have reached the deployment before its response
was lost. If you sign in to a different account, Studio clears the previous
account's browser data and unfinished draft before opening that workspace.

The connection banner shows a Studio or deployment outage ahead of a sign-in
prompt. A pending connection check is neutral. When Studio cannot verify your
session, use the banner's retry action; the current route and draft stay in
place while it checks again.

### Static token or no authentication

Studio can also run as a single service identity by setting
`MECATL_AUTH_TOKEN` together with `MECATL_BASE_URL`, or against a deployment
that publishes no OIDC profile. In both cases every browser that reaches Studio
acts as one principal, so the image refuses to start unless you also set
`STUDIO_ALLOW_UNAUTHENTICATED=1`. Use this only behind your own access control.

## Configuration reference

|Variable|Default|Purpose|
|-|-|-|
|`MECATL_BASE_URL`|unset|Connects to an existing Mecatl gRPC listener. The image requires it.|
|`MECATL_RESOURCE_URL`|derived from `MECATL_BASE_URL`|Canonical RFC 9728 resource identifier. It must equal the `resource` the deployment publishes.|
|`MECATL_AUTH_TOKEN`|unset|Static bearer token; disables browser login.|
|`STUDIO_PUBLIC_URL`|unset|Browser-facing origin; required for browser login and inside the image.|
|`STUDIO_SESSION_SECRET`|unset|Secret sealing the session cookies; required for browser login.|
|`STUDIO_PORT`|`3100`|Listening port.|
|`STUDIO_HOST`|`0.0.0.0` in the image, `127.0.0.1` elsewhere|Listening address.|
|`STUDIO_TRUSTED_PROXY_HOPS`|`0`|Reverse-proxy hops to trust when reading `X-Forwarded-For`.|
|`STUDIO_RATE_LIMIT_MAX`|`20`|Sign-in requests (login, callback, and logout) allowed per client within the window. The session check the browser makes on every page load is not counted.|
|`STUDIO_RATE_LIMIT_WINDOW_MS`|`60000`|Rate-limit window in milliseconds.|
|`STUDIO_ACTIVITY_REPLAY_MAX`|`2000`|Durable events one reattach replays before it tells the browser to read the transcript for older history. Live events are never bounded.|
|`STUDIO_ACTIVITY_MAX_STREAMS`|`4`|Concurrent activity streams one chat admits per replica.|
|`STUDIO_ALLOW_UNAUTHENTICATED`|unset|Set to `1` to run a static-token or no-authentication target inside the image.|
|`STUDIO_EXPERIMENTAL_WRITER`|unset|Set to `1` to expose the experimental Writer workspace and API.|
|`STUDIO_LOG_LEVEL`|`info`|One of `debug`, `info`, `warn`, or `error`. Logs are JSON lines on standard error.|

For local development outside a container, the
[Studio README](https://github.com/stacklok/mecatl/blob/main/apps/README.md)
covers the spawn and mock runtime modes and the `task studio:*` commands.

## Next steps

- [Cloud-native k8s with mecak8s](./mecak8s.md) to deploy the server Studio
  connects to.
- [Configure a deployment](./settings.md) to enable the capabilities Studio
  shows.
- [Permissions and posture](/features/permissions-and-posture.md) to understand
  the approvals Studio surfaces in a chat.

## Troubleshooting

<details>
<summary>Studio shows "Page not found" or "Something went wrong"</summary>

Use **Go to Chats** to return to the workspace. If a page reports an unexpected
error, choose **Try again** first. If Studio cannot render its main interface,
choose **Reload Studio**. Check the Studio URL if the page remains missing.

</details>

<details>
<summary>Studio exits with "authentication setup failed (auth_discovery_failed)"</summary>

Studio could not fetch the deployment's protected-resource document. Check that
`MECATL_BASE_URL` (or `MECATL_RESOURCE_URL`) points at the HTTPS address that
serves `/.well-known/oauth-protected-resource`. A deployment without OIDC answers
`404` there, which Studio treats as "no browser login" instead of an error.

</details>

<details>
<summary>Studio exits with "authentication setup failed (auth_discovery_invalid)"</summary>

Studio fetched the protected-resource document but refused its contents. The
most common cause is a `resource` value that differs from `MECATL_RESOURCE_URL`,
or from `MECATL_BASE_URL` when `MECATL_RESOURCE_URL` is unset. Compare the
`resource` field the deployment publishes with the value Studio uses, including
scheme, port, and trailing path. The document must also name exactly one
authorization server over HTTPS, accept header bearer tokens, and publish the
Mecatl audience and client ID.

</details>

<details>
<summary>Every page answers "Host not allowed" (421)</summary>

The request reached Studio with a `Host` header other than the host of
`STUDIO_PUBLIC_URL`. Configure the ingress or load balancer to preserve the
original `Host`, and make sure users open exactly the `STUDIO_PUBLIC_URL`
origin. Without `STUDIO_PUBLIC_URL`, Studio answers only on `localhost`,
`127.0.0.1`, or `[::1]`.

</details>

<details>
<summary>Sign-in loops back to the login screen</summary>

Studio sets `Secure` cookies when `STUDIO_PUBLIC_URL` is `https://`. Confirm
users open exactly that origin, that the identity provider redirects to
`STUDIO_PUBLIC_URL/api/v1/auth/callback`, and that every replica shares the
same `STUDIO_SESSION_SECRET`.

</details>
