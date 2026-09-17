---
title: TypeScript SDK core API
description: Look up the transport-neutral TypeScript SDK functions, methods, types, and errors.
sidebar_position: 2
toc_max_heading_level: 2
---

import Heading from "@theme/Heading";

# TypeScript SDK core API

{/* Generated from API Extractor models and sdk/typescript/src TSDoc. Regenerate with task sdk:docs. DO NOT EDIT. */}

This reference describes the declarations exported by `@stacklok-oss/mecatl-sdk`.

## Symbol index

| Symbol | Kind |
| --- | --- |
| [`ActivityGapError`](#api-activitygaperror-class) | Class |
| [`AgentEvent`](#api-agentevent-typealias) | Type alias |
| [`Agents`](#api-agents-interface) | Interface |
| [`ApprovalEventPayload`](#api-approvaleventpayload-interface) | Interface |
| [`ArchivedConversationMessage`](#api-archivedconversationmessage-interface) | Interface |
| [`AttachedRun`](#api-attachedrun-interface) | Interface |
| [`AttachOptions`](#api-attachoptions-interface) | Interface |
| [`audioPart`](#api-audiopart-function) | Function |
| [`audioPartFromBlob`](#api-audiopartfromblob-function) | Function |
| [`AudioPromptPart`](#api-audiopromptpart-interface) | Interface |
| [`AuthenticationError`](#api-authenticationerror-class) | Class |
| [`ClearSessionOptions`](#api-clearsessionoptions-interface) | Interface |
| [`Client`](#api-client-interface) | Interface |
| [`ClientDiagnosticsOptions`](#api-clientdiagnosticsoptions-interface) | Interface |
| [`Commands`](#api-commands-interface) | Interface |
| [`CompactionArchiveEventPayload`](#api-compactionarchiveeventpayload-interface) | Interface |
| [`connect`](#api-connect-function) | Function |
| [`ConnectionStatus`](#api-connectionstatus-typealias) | Type alias |
| [`ConnectionStatusListener`](#api-connectionstatuslistener-typealias) | Type alias |
| [`ConnectionStatusStore`](#api-connectionstatusstore-interface) | Interface |
| [`ConnectOptions`](#api-connectoptions-typealias) | Type alias |
| [`createHttpTransport`](#api-createhttptransport-function) | Function |
| [`createRawClient`](#api-createrawclient-function) | Function |
| [`CreateSessionOptions`](#api-createsessionoptions-interface) | Interface |
| [`CreateTeamOptions`](#api-createteamoptions-interface) | Interface |
| [`CredentialOptions`](#api-credentialoptions-interface) | Interface |
| [`CredentialProvider`](#api-credentialprovider-typealias) | Type alias |
| [`CursorExpiredError`](#api-cursorexpirederror-class) | Class |
| [`CursorMalformedError`](#api-cursormalformederror-class) | Class |
| [`CursorScopeError`](#api-cursorscopeerror-class) | Class |
| [`DiagnosticFieldValue`](#api-diagnosticfieldvalue-typealias) | Type alias |
| [`DiagnosticLevel`](#api-diagnosticlevel-typealias) | Type alias |
| [`DiagnosticRecord`](#api-diagnosticrecord-interface) | Interface |
| [`DiagnosticsSink`](#api-diagnosticssink-typealias) | Type alias |
| [`DreamPlans`](#api-dreamplans-interface) | Interface |
| [`DreamTargetCapability`](#api-dreamtargetcapability-interface) | Interface |
| [`ErrorOrigin`](#api-errororigin-typealias) | Type alias |
| [`Event`](#api-event-typealias) | Type alias |
| [`EventCommon`](#api-eventcommon-interface) | Interface |
| [`EventContent`](#api-eventcontent-interface) | Interface |
| [`EventContentBlock`](#api-eventcontentblock-interface) | Interface |
| [`EventOf`](#api-eventof-typealias) | Type alias |
| [`EventPayloads`](#api-eventpayloads-interface) | Interface |
| [`EventUsage`](#api-eventusage-interface) | Interface |
| [`ForkSessionOptions`](#api-forksessionoptions-interface) | Interface |
| [`getRawJson`](#api-getrawjson-function) | Function |
| [`HookEventPayload`](#api-hookeventpayload-interface) | Interface |
| [`HttpTransportOptions`](#api-httptransportoptions-interface) | Interface |
| [`imagePart`](#api-imagepart-function) | Function |
| [`imagePartFromBlob`](#api-imagepartfromblob-function) | Function |
| [`ImagePromptPart`](#api-imagepromptpart-interface) | Interface |
| [`IncompatibleServerError`](#api-incompatibleservererror-class) | Class |
| [`InjectedTransportOptions`](#api-injectedtransportoptions-interface) | Interface |
| [`InvalidStateError`](#api-invalidstateerror-class) | Class |
| [`KnownEvent`](#api-knownevent-typealias) | Type alias |
| [`KnownEventKind`](#api-knowneventkind-typealias) | Type alias |
| [`LearnedSkills`](#api-learnedskills-interface) | Interface |
| [`LearningAttempts`](#api-learningattempts-interface) | Interface |
| [`LearningProposals`](#api-learningproposals-interface) | Interface |
| [`ManualDreamCapabilities`](#api-manualdreamcapabilities-interface) | Interface |
| [`MAX_MEDIA_PART_BYTES`](#api-max-media-part-bytes-variable) | Variable |
| [`MAX_PROMPT_MEDIA_BYTES`](#api-max-prompt-media-bytes-variable) | Variable |
| [`MAX_PROMPT_MEDIA_PARTS`](#api-max-prompt-media-parts-variable) | Variable |
| [`McpAuthorization`](#api-mcpauthorization-interface) | Interface |
| [`McpAuthorizationFlow`](#api-mcpauthorizationflow-interface) | Interface |
| [`McpAuthorizationFlowOptions`](#api-mcpauthorizationflowoptions-interface) | Interface |
| [`McpAuthorizationOperation`](#api-mcpauthorizationoperation-typealias) | Type alias |
| [`McpAuthorizationResult`](#api-mcpauthorizationresult-typealias) | Type alias |
| [`McpAuthorizationStatus`](#api-mcpauthorizationstatus-typealias) | Type alias |
| [`McpConnectorAvailability`](#api-mcpconnectoravailability-typealias) | Type alias |
| [`McpConnectorAvailability`](#api-mcpconnectoravailability-variable) | Variable |
| [`McpConnectorCatalogueState`](#api-mcpconnectorcataloguestate-typealias) | Type alias |
| [`McpConnectorCatalogueState`](#api-mcpconnectorcataloguestate-variable) | Variable |
| [`McpConnectorEnrollmentState`](#api-mcpconnectorenrollmentstate-typealias) | Type alias |
| [`McpConnectorEnrollmentState`](#api-mcpconnectorenrollmentstate-variable) | Variable |
| [`McpConnectorInventory`](#api-mcpconnectorinventory-interface) | Interface |
| [`McpConnectorStatus`](#api-mcpconnectorstatus-interface) | Interface |
| [`McpInventory`](#api-mcpinventory-interface) | Interface |
| [`MECATL_ATTACH_FILTERED_KINDS`](#api-mecatl-attach-filtered-kinds-variable) | Variable |
| [`MECATL_ERROR_CODES`](#api-mecatl-error-codes-variable) | Variable |
| [`MECATL_EVENT_KINDS`](#api-mecatl-event-kinds-variable) | Variable |
| [`MECATL_WATCH_PHASES`](#api-mecatl-watch-phases-variable) | Variable |
| [`MecatlError`](#api-mecatlerror-class) | Class |
| [`MecatlErrorCode`](#api-mecatlerrorcode-typealias) | Type alias |
| [`MecatlErrorOptions`](#api-mecatlerroroptions-interface) | Interface |
| [`MediaPartOptions`](#api-mediapartoptions-interface) | Interface |
| [`MediaPartSource`](#api-mediapartsource-interface) | Interface |
| [`ModelRetryEventPayload`](#api-modelretryeventpayload-interface) | Interface |
| [`Models`](#api-models-interface) | Interface |
| [`NoRunsError`](#api-norunserror-class) | Class |
| [`ParallelEventPayload`](#api-paralleleventpayload-interface) | Interface |
| [`PermissionAskAlreadyResolvedError`](#api-permissionaskalreadyresolvederror-class) | Class |
| [`PermissionAskEventPayload`](#api-permissionaskeventpayload-interface) | Interface |
| [`PermissionAskResponder`](#api-permissionaskresponder-typealias) | Type alias |
| [`PermissionVerdict`](#api-permissionverdict-typealias) | Type alias |
| [`PlanApprovalRequiredError`](#api-planapprovalrequirederror-class) | Class |
| [`PlanApprovalResponder`](#api-planapprovalresponder-typealias) | Type alias |
| [`PlanApprovalVerdict`](#api-planapprovalverdict-typealias) | Type alias |
| [`PlanContinuationStartError`](#api-plancontinuationstarterror-class) | Class |
| [`PlanResolution`](#api-planresolution-interface) | Interface |
| [`PlanResolutionResult`](#api-planresolutionresult-interface) | Interface |
| [`PromptInput`](#api-promptinput-typealias) | Type alias |
| [`PromptPart`](#api-promptpart-typealias) | Type alias |
| [`PromptValidationError`](#api-promptvalidationerror-class) | Class |
| [`PromptValidationReason`](#api-promptvalidationreason-typealias) | Type alias |
| [`ProtocolError`](#api-protocolerror-class) | Class |
| [`RawClient`](#api-rawclient-interface) | Interface |
| [`RawClientOptions`](#api-rawclientoptions-interface) | Interface |
| [`Reflection`](#api-reflection-interface) | Interface |
| [`RequestOptions`](#api-requestoptions-typealias) | Type alias |
| [`ResultEventPayload`](#api-resulteventpayload-interface) | Interface |
| [`RetryDisposition`](#api-retrydisposition-typealias) | Type alias |
| [`Run`](#api-run-interface) | Interface |
| [`RunAuthorizationRequiredError`](#api-runauthorizationrequirederror-class) | Class |
| [`RunAuthorizationRequiredOutcome`](#api-runauthorizationrequiredoutcome-interface) | Interface |
| [`RunCompletedOutcome`](#api-runcompletedoutcome-interface) | Interface |
| [`RunControls`](#api-runcontrols-interface) | Interface |
| [`RunOptions`](#api-runoptions-interface) | Interface |
| [`RunOutcome`](#api-runoutcome-typealias) | Type alias |
| [`RunResult`](#api-runresult-interface) | Interface |
| [`RunSteerAcknowledgement`](#api-runsteeracknowledgement-interface) | Interface |
| [`RunSteerCancellationAcknowledgement`](#api-runsteercancellationacknowledgement-interface) | Interface |
| [`RunSteerOptions`](#api-runsteeroptions-interface) | Interface |
| [`ScheduleEventPayload`](#api-scheduleeventpayload-interface) | Interface |
| [`Schedules`](#api-schedules-interface) | Interface |
| [`SdkCursor`](#api-sdkcursor-typealias) | Type alias |
| [`SDKErrorCode`](#api-sdkerrorcode-typealias) | Type alias |
| [`Server`](#api-server-interface) | Interface |
| [`ServerCapabilities`](#api-servercapabilities-interface) | Interface |
| [`ServerCompatibility`](#api-servercompatibility-interface) | Interface |
| [`ServerError`](#api-servererror-class) | Class |
| [`ServerErrorCode`](#api-servererrorcode-typealias) | Type alias |
| [`ServerFeature`](#api-serverfeature-typealias) | Type alias |
| [`ServerFeature`](#api-serverfeature-variable) | Variable |
| [`ServerInfo`](#api-serverinfo-interface) | Interface |
| [`ServerInfoOptions`](#api-serverinfooptions-interface) | Interface |
| [`ServerPosture`](#api-serverposture-typealias) | Type alias |
| [`ServerPosture`](#api-serverposture-variable) | Variable |
| [`Session`](#api-session-interface) | Interface |
| [`SESSION_ID_HEADER_NAME`](#api-session-id-header-name-variable) | Variable |
| [`SessionActivity`](#api-sessionactivity-interface) | Interface |
| [`SessionActivityReplayStatus`](#api-sessionactivityreplaystatus-interface) | Interface |
| [`SessionBusyError`](#api-sessionbusyerror-class) | Class |
| [`SessionCapabilities`](#api-sessioncapabilities-interface) | Interface |
| [`SessionLimits`](#api-sessionlimits-interface) | Interface |
| [`SessionMcpServer`](#api-sessionmcpserver-interface) | Interface |
| [`SessionMode`](#api-sessionmode-typealias) | Type alias |
| [`SessionMode`](#api-sessionmode-variable) | Variable |
| [`SessionPlacement`](#api-sessionplacement-interface) | Interface |
| [`SessionRelationship`](#api-sessionrelationship-interface) | Interface |
| [`SessionResolvedModel`](#api-sessionresolvedmodel-interface) | Interface |
| [`Sessions`](#api-sessions-interface) | Interface |
| [`SessionSnapshot`](#api-sessionsnapshot-interface) | Interface |
| [`SessionSnapshotLimits`](#api-sessionsnapshotlimits-interface) | Interface |
| [`SessionTitle`](#api-sessiontitle-interface) | Interface |
| [`SessionTitleAttempt`](#api-sessiontitleattempt-interface) | Interface |
| [`SessionTitleEventPayload`](#api-sessiontitleeventpayload-interface) | Interface |
| [`SessionTokenUsage`](#api-sessiontokenusage-interface) | Interface |
| [`SessionTranscript`](#api-sessiontranscript-interface) | Interface |
| [`SessionTranscriptMessage`](#api-sessiontranscriptmessage-interface) | Interface |
| [`Skills`](#api-skills-interface) | Interface |
| [`Soul`](#api-soul-interface) | Interface |
| [`SteerEventPayload`](#api-steereventpayload-interface) | Interface |
| [`SteerOutcomeEventPayload`](#api-steeroutcomeeventpayload-interface) | Interface |
| [`Storage`](#api-storage-interface) | Interface |
| [`StreamProgress`](#api-streamprogress-typealias) | Type alias |
| [`SubagentEventPayload`](#api-subagenteventpayload-interface) | Interface |
| [`SUPPORTED_API_MAJOR`](#api-supported-api-major-variable) | Variable |
| [`Team`](#api-team-interface) | Interface |
| [`TeamEvent`](#api-teamevent-typealias) | Type alias |
| [`TeamEventPayload`](#api-teameventpayload-interface) | Interface |
| [`TeamFindingEventPayload`](#api-teamfindingeventpayload-interface) | Interface |
| [`TeamMemberDispositionEventPayload`](#api-teammemberdispositioneventpayload-interface) | Interface |
| [`TeamMemberOptions`](#api-teammemberoptions-interface) | Interface |
| [`TeamMemberRunEvent`](#api-teammemberrunevent-typealias) | Type alias |
| [`TeamMemberSpecEventPayload`](#api-teammemberspeceventpayload-interface) | Interface |
| [`TeamMessageOptions`](#api-teammessageoptions-interface) | Interface |
| [`TeamOutcomeRunEvent`](#api-teamoutcomerunevent-interface) | Interface |
| [`TeamRun`](#api-teamrun-interface) | Interface |
| [`TeamRunEvent`](#api-teamrunevent-typealias) | Type alias |
| [`Teams`](#api-teams-interface) | Interface |
| [`TeamTaskEventPayload`](#api-teamtaskeventpayload-interface) | Interface |
| [`textPart`](#api-textpart-function) | Function |
| [`TextPromptPart`](#api-textpromptpart-interface) | Interface |
| [`TitleAttemptEventPayload`](#api-titleattempteventpayload-interface) | Interface |
| [`ToolCallEventPayload`](#api-toolcalleventpayload-interface) | Interface |
| [`ToolResultEventPayload`](#api-toolresulteventpayload-interface) | Interface |
| [`TransportError`](#api-transporterror-class) | Class |
| [`TransportKind`](#api-transportkind-typealias) | Type alias |
| [`TurnEndEventPayload`](#api-turnendeventpayload-interface) | Interface |
| [`UnknownEvent`](#api-unknownevent-typealias) | Type alias |
| [`UnknownGrpcEvent`](#api-unknowngrpcevent-interface) | Interface |
| [`UnknownHttpEvent`](#api-unknownhttpevent-interface) | Interface |
| [`UnknownWatchEnvelope`](#api-unknownwatchenvelope-interface) | Interface |
| [`UnsupportedFeatureError`](#api-unsupportedfeatureerror-class) | Class |
| [`UserModel`](#api-usermodel-interface) | Interface |
| [`UserPromptEventPayload`](#api-userprompteventpayload-interface) | Interface |
| [`WATCH_SESSION_EVENTS_FEATURE`](#api-watch-session-events-feature-variable) | Variable |
| [`WatchBoundaryEnvelope`](#api-watchboundaryenvelope-interface) | Interface |
| [`WatchEnvelope`](#api-watchenvelope-typealias) | Type alias |
| [`WatchEventEnvelope`](#api-watcheventenvelope-interface) | Interface |
| [`WatchGapEnvelope`](#api-watchgapenvelope-interface) | Interface |
| [`withSessionAffinity`](#api-withsessionaffinity-function) | Function |
| [`WorkspaceEnrollment`](#api-workspaceenrollment-interface) | Interface |
| [`WorkspaceEnrollmentStatus`](#api-workspaceenrollmentstatus-typealias) | Type alias |
| [`WorkspaceEnrollmentStatus`](#api-workspaceenrollmentstatus-variable) | Variable |
| [`Worktrees`](#api-worktrees-interface) | Interface |

## Classes

<Heading as="h3" id="api-activitygaperror-class"><code>ActivityGapError</code></Heading>

Durable activity is known to contain a delivery gap.

```ts
export declare class ActivityGapError extends MecatlError
```

Callable members: [`constructor`](#api-activitygaperror-constructor-constructor)

<Heading as="h4" id="api-activitygaperror-constructor-constructor"><code>ActivityGapError.constructor</code></Heading>

Constructs a new instance of the `ActivityGapError` class

```ts
constructor(message?: string, options?: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`, optional)
- `options` (`Omit<MecatlErrorOptions, "code">`, optional)

<Heading as="h3" id="api-authenticationerror-class"><code>AuthenticationError</code></Heading>

Credential resolution or server authentication failed.

```ts
export declare class AuthenticationError extends MecatlError
```

Callable members: [`constructor`](#api-authenticationerror-constructor-constructor)

<Heading as="h4" id="api-authenticationerror-constructor-constructor"><code>AuthenticationError.constructor</code></Heading>

Constructs a new instance of the `AuthenticationError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-cursorexpirederror-class"><code>CursorExpiredError</code></Heading>

The server cursor belongs to a superseded event-log generation.

```ts
export declare class CursorExpiredError extends MecatlError
```

Callable members: [`constructor`](#api-cursorexpirederror-constructor-constructor)

<Heading as="h4" id="api-cursorexpirederror-constructor-constructor"><code>CursorExpiredError.constructor</code></Heading>

Constructs a new instance of the `CursorExpiredError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-cursormalformederror-class"><code>CursorMalformedError</code></Heading>

An SDK cursor is not a structurally valid `sdkcur/1` envelope.

```ts
export declare class CursorMalformedError extends MecatlError
```

Callable members: [`constructor`](#api-cursormalformederror-constructor-constructor)

<Heading as="h4" id="api-cursormalformederror-constructor-constructor"><code>CursorMalformedError.constructor</code></Heading>

Constructs a new instance of the `CursorMalformedError` class

```ts
constructor(message?: string, options?: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`, optional)
- `options` (`Omit<MecatlErrorOptions, "code">`, optional)

<Heading as="h3" id="api-cursorscopeerror-class"><code>CursorScopeError</code></Heading>

An SDK cursor would widen the set of durable events delivered by its source view.

```ts
export declare class CursorScopeError extends MecatlError
```

Callable members: [`constructor`](#api-cursorscopeerror-constructor-constructor)

<Heading as="h4" id="api-cursorscopeerror-constructor-constructor"><code>CursorScopeError.constructor</code></Heading>

Constructs a new instance of the `CursorScopeError` class

```ts
constructor(message?: string);
```

Parameters:

- `message` (`string`, optional)

<Heading as="h3" id="api-incompatibleservererror-class"><code>IncompatibleServerError</code></Heading>

The connected server does not satisfy the SDK compatibility floor.

```ts
export declare class IncompatibleServerError extends MecatlError
```

Callable members: [`constructor`](#api-incompatibleservererror-constructor-constructor)

<Heading as="h4" id="api-incompatibleservererror-constructor-constructor"><code>IncompatibleServerError.constructor</code></Heading>

Constructs a new instance of the `IncompatibleServerError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-invalidstateerror-class"><code>InvalidStateError</code></Heading>

An operation is invalid for the current local SDK lifecycle state.

```ts
export declare class InvalidStateError extends MecatlError
```

Callable members: [`constructor`](#api-invalidstateerror-constructor-constructor)

<Heading as="h4" id="api-invalidstateerror-constructor-constructor"><code>InvalidStateError.constructor</code></Heading>

Constructs a new instance of the `InvalidStateError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-mecatlerror-class"><code>MecatlError</code></Heading>

Base class for every error authored by the SDK.

```ts
export declare class MecatlError extends Error
```

Callable members: [`constructor`](#api-mecatlerror-constructor-constructor), [`toJSON()`](#api-mecatlerror-tojson-method)

<Heading as="h4" id="api-mecatlerror-code-property"><code>MecatlError.code</code></Heading>

```ts
readonly code: MecatlErrorCode;
```

<Heading as="h4" id="api-mecatlerror-constructor-constructor"><code>MecatlError.constructor</code></Heading>

Constructs a new instance of the `MecatlError` class

```ts
constructor(message: string, options: MecatlErrorOptions);
```

Parameters:

- `message` (`string`)
- `options` (`MecatlErrorOptions`)

<Heading as="h4" id="api-mecatlerror-requestid-property"><code>MecatlError.requestId</code></Heading>

```ts
readonly requestId: string | undefined;
```

<Heading as="h4" id="api-mecatlerror-status-property"><code>MecatlError.status</code></Heading>

```ts
readonly status: number | undefined;
```

<Heading as="h4" id="api-mecatlerror-tojson-method"><code>MecatlError.toJSON</code></Heading>

Returns a JSON-safe representation without the original cause.

```ts
toJSON(): Record<string, unknown>;
```

Returns: `Record<string, unknown>`

<Heading as="h4" id="api-mecatlerror-transport-property"><code>MecatlError.transport</code></Heading>

```ts
readonly transport: ErrorOrigin;
```

<Heading as="h3" id="api-norunserror-class"><code>NoRunsError</code></Heading>

The readable session log contains no event associated with a run.

```ts
export declare class NoRunsError extends MecatlError
```

Callable members: [`constructor`](#api-norunserror-constructor-constructor)

<Heading as="h4" id="api-norunserror-constructor-constructor"><code>NoRunsError.constructor</code></Heading>

Constructs a new instance of the `NoRunsError` class

```ts
constructor();
```

<Heading as="h3" id="api-permissionaskalreadyresolvederror-class"><code>PermissionAskAlreadyResolvedError</code></Heading>

A permission ask is no longer pending on its originating run.

```ts
export declare class PermissionAskAlreadyResolvedError extends InvalidStateError
```

Callable members: [`constructor`](#api-permissionaskalreadyresolvederror-constructor-constructor)

<Heading as="h4" id="api-permissionaskalreadyresolvederror-askid-property"><code>PermissionAskAlreadyResolvedError.askId</code></Heading>

```ts
readonly askId: string;
```

<Heading as="h4" id="api-permissionaskalreadyresolvederror-constructor-constructor"><code>PermissionAskAlreadyResolvedError.constructor</code></Heading>

Constructs a new instance of the `PermissionAskAlreadyResolvedError` class

```ts
constructor(askId: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `askId` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-planapprovalrequirederror-class"><code>PlanApprovalRequiredError</code></Heading>

query() plan mode was requested without its required plan-specific responder.

```ts
export declare class PlanApprovalRequiredError extends InvalidStateError
```

Callable members: [`constructor`](#api-planapprovalrequirederror-constructor-constructor)

<Heading as="h4" id="api-planapprovalrequirederror-constructor-constructor"><code>PlanApprovalRequiredError.constructor</code></Heading>

Constructs a new instance of the `PlanApprovalRequiredError` class

```ts
constructor();
```

<Heading as="h3" id="api-plancontinuationstarterror-class"><code>PlanContinuationStartError</code></Heading>

The approved plan's continuation could not be admitted before it received a run ID.

```ts
export declare class PlanContinuationStartError extends MecatlError
```

Callable members: [`constructor`](#api-plancontinuationstarterror-constructor-constructor)

<Heading as="h4" id="api-plancontinuationstarterror-constructor-constructor"><code>PlanContinuationStartError.constructor</code></Heading>

Constructs a new instance of the `PlanContinuationStartError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-promptvalidationerror-class"><code>PromptValidationError</code></Heading>

A structured prompt failed local validation before any request was sent.

```ts
export declare class PromptValidationError extends MecatlError
```

Callable members: [`constructor`](#api-promptvalidationerror-constructor-constructor)

<Heading as="h4" id="api-promptvalidationerror-constructor-constructor"><code>PromptValidationError.constructor</code></Heading>

Constructs a new instance of the `PromptValidationError` class

```ts
constructor(reason: PromptValidationReason, message: string);
```

Parameters:

- `reason` (`PromptValidationReason`)
- `message` (`string`)

<Heading as="h4" id="api-promptvalidationerror-reason-property"><code>PromptValidationError.reason</code></Heading>

```ts
readonly reason: PromptValidationReason;
```

<Heading as="h3" id="api-protocolerror-class"><code>ProtocolError</code></Heading>

A transport response violated the SDK's protocol contract.

```ts
export declare class ProtocolError extends MecatlError
```

Callable members: [`constructor`](#api-protocolerror-constructor-constructor)

<Heading as="h4" id="api-protocolerror-constructor-constructor"><code>ProtocolError.constructor</code></Heading>

Constructs a new instance of the `ProtocolError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-runauthorizationrequirederror-class"><code>RunAuthorizationRequiredError</code></Heading>

`Run.result()` consumed a valid authorization handoff instead of a completed result. Read `outcome` to create `Session.mcpAuthorization()` with the exact authorization ID.

```ts
export declare class RunAuthorizationRequiredError extends InvalidStateError
```

Callable members: [`constructor`](#api-runauthorizationrequirederror-constructor-constructor)

<Heading as="h4" id="api-runauthorizationrequirederror-constructor-constructor"><code>RunAuthorizationRequiredError.constructor</code></Heading>

Constructs a new instance of the `RunAuthorizationRequiredError` class

```ts
constructor(outcome: RunAuthorizationRequiredOutcome, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `outcome` (`RunAuthorizationRequiredOutcome`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h4" id="api-runauthorizationrequirederror-outcome-property"><code>RunAuthorizationRequiredError.outcome</code></Heading>

```ts
readonly outcome: RunAuthorizationRequiredOutcome;
```

<Heading as="h3" id="api-servererror-class"><code>ServerError</code></Heading>

A typed domain failure returned by the Mecatl server.

```ts
export declare class ServerError extends MecatlError
```

Callable members: [`constructor`](#api-servererror-constructor-constructor)

<Heading as="h4" id="api-servererror-code-property"><code>ServerError.code</code></Heading>

```ts
readonly code: ServerErrorCode;
```

<Heading as="h4" id="api-servererror-constructor-constructor"><code>ServerError.constructor</code></Heading>

Constructs a new instance of the `ServerError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code"> & {
        code: ServerErrorCode;
    });
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code"> & { code: ServerErrorCode; }`)

<Heading as="h3" id="api-sessionbusyerror-class"><code>SessionBusyError</code></Heading>

A local run is already active on this Session handle.

```ts
export declare class SessionBusyError extends InvalidStateError
```

<Heading as="h3" id="api-transporterror-class"><code>TransportError</code></Heading>

A request failed before the server returned a domain response.

```ts
export declare class TransportError extends MecatlError
```

Callable members: [`constructor`](#api-transporterror-constructor-constructor)

<Heading as="h4" id="api-transporterror-constructor-constructor"><code>TransportError.constructor</code></Heading>

Constructs a new instance of the `TransportError` class

```ts
constructor(message: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `message` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h3" id="api-unsupportedfeatureerror-class"><code>UnsupportedFeatureError</code></Heading>

The connected server does not advertise a required feature.

```ts
export declare class UnsupportedFeatureError extends MecatlError
```

Callable members: [`constructor`](#api-unsupportedfeatureerror-constructor-constructor)

<Heading as="h4" id="api-unsupportedfeatureerror-constructor-constructor"><code>UnsupportedFeatureError.constructor</code></Heading>

Constructs a new instance of the `UnsupportedFeatureError` class

```ts
constructor(feature: string, options: Omit<MecatlErrorOptions, "code">);
```

Parameters:

- `feature` (`string`)
- `options` (`Omit<MecatlErrorOptions, "code">`)

<Heading as="h4" id="api-unsupportedfeatureerror-feature-property"><code>UnsupportedFeatureError.feature</code></Heading>

```ts
readonly feature: string;
```

## Functions

<Heading as="h3" id="api-audiopart-function"><code>audioPart</code></Heading>

Constructs an audio part from inline bytes or an HTTPS URL.

```ts
export declare function audioPart(options: MediaPartOptions): AudioPromptPart;
```

Parameters:

- `options` (`MediaPartOptions`): Audio source and MIME type.

Returns: `AudioPromptPart`: A validated audio prompt part.

Throws: `PromptValidationError` when the source, MIME type, or size is invalid.

<Heading as="h3" id="api-audiopartfromblob-function"><code>audioPartFromBlob</code></Heading>

Constructs an audio part from a browser Blob or File.

```ts
export declare function audioPartFromBlob(blob: Blob, mimeType?: string): Promise<AudioPromptPart>;
```

Parameters:

- `blob` (`Blob`): Browser media value to read.
- `mimeType` (`string`, optional): Audio MIME type. Defaults to the Blob's type.

Returns: `Promise<AudioPromptPart>`: A validated audio prompt part containing the Blob's bytes.

Throws: `PromptValidationError` when the MIME type or size is invalid.

<Heading as="h3" id="api-connect-function"><code>connect</code></Heading>

Creates an isomorphic Client over HTTP or a caller-injected transport.

```ts
export declare function connect(options: ConnectOptions): Client;
```

Parameters:

- `options` (`ConnectOptions`): HTTP transport settings or a caller-owned transport.

Returns: `Client`: A high-level Mecatl client.

<Heading as="h3" id="api-createhttptransport-function"><code>createHttpTransport</code></Heading>

Creates a browser-compatible Connect-ES transport over Mecatl's HTTP and SSE API.

```ts
export declare function createHttpTransport(options: HttpTransportOptions): Transport;
```

Parameters:

- `options` (`HttpTransportOptions`): HTTP endpoint, credentials, and fetch implementation.

Returns: `Transport`: A Connect-ES transport for Mecatl's HTTP and SSE routes.

<Heading as="h3" id="api-createrawclient-function"><code>createRawClient</code></Heading>

Creates a transport-neutral client for low-level RPC operations. Before the first requested operation, the client performs a stateless compatibility check.

```ts
export declare function createRawClient(options: RawClientOptions): RawClient;
```

Parameters:

- `options` (`RawClientOptions`): Caller-owned transport and its protocol kind.

Returns: `RawClient`: A low-level client that enforces SDK compatibility before operations.

<Heading as="h3" id="api-getrawjson-function"><code>getRawJson</code></Heading>

Returns the exact JSON value received by the HTTP transport, including unknown fields.

```ts
export declare function getRawJson(message: object): JsonValue | undefined;
```

Parameters:

- `message` (`object`): Decoded protobuf message returned by the SDK.

Returns: `JsonValue | undefined`: The original JSON value, or `undefined` when none was recorded.

<Heading as="h3" id="api-imagepart-function"><code>imagePart</code></Heading>

Constructs an image part from inline bytes or an HTTPS URL.

```ts
export declare function imagePart(options: MediaPartOptions): ImagePromptPart;
```

Parameters:

- `options` (`MediaPartOptions`): Image source and MIME type.

Returns: `ImagePromptPart`: A validated image prompt part.

Throws: `PromptValidationError` when the source, MIME type, or size is invalid.

<Heading as="h3" id="api-imagepartfromblob-function"><code>imagePartFromBlob</code></Heading>

Constructs an image part from a browser Blob or File.

```ts
export declare function imagePartFromBlob(blob: Blob, mimeType?: string): Promise<ImagePromptPart>;
```

Parameters:

- `blob` (`Blob`): Browser media value to read.
- `mimeType` (`string`, optional): Image MIME type. Defaults to the Blob's type.

Returns: `Promise<ImagePromptPart>`: A validated image prompt part containing the Blob's bytes.

Throws: `PromptValidationError` when the MIME type or size is invalid.

<Heading as="h3" id="api-textpart-function"><code>textPart</code></Heading>

Constructs a text segment for a structured prompt.

```ts
export declare function textPart(text: string): TextPromptPart;
```

Parameters:

- `text` (`string`): Text to send in this prompt segment.

Returns: `TextPromptPart`: A text prompt part.

<Heading as="h3" id="api-withsessionaffinity-function"><code>withSessionAffinity</code></Heading>

Returns call options bound to one explicit session without replacing caller headers. Throws synchronously when sessionId cannot be represented byte-exactly as the affinity header. The binding is a routing hint only; authentication and authorization remain independent.

```ts
export declare function withSessionAffinity(sessionId: string, options?: CallOptions): CallOptions;
```

Parameters:

- `sessionId` (`string`): Session ID to carry as the affinity header.
- `options` (`CallOptions`, optional): Existing call options whose headers must be preserved.

Returns: `CallOptions`: Call options containing exactly one session-affinity header.

Throws: `RangeError` when the session ID is not printable ASCII or is otherwise invalid.

## Interfaces

<Heading as="h3" id="api-agents-interface"><code>Agents</code></Heading>

Resolved agent-definition inventory operations.

```ts
export interface Agents
```

Callable members: [`list()`](#api-agents-list-methodsignature)

<Heading as="h4" id="api-agents-list-methodsignature"><code>Agents.list</code></Heading>

Lists the resolved agent definitions.

```ts
list(request: ListAgentsRequest, options?: RequestOptions): Promise<ListAgentsResponse>;
```

Parameters:

- `request` (`ListAgentsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListAgentsResponse>`

<Heading as="h3" id="api-approvaleventpayload-interface"><code>ApprovalEventPayload</code></Heading>

The payload of an `approval` replay event.

```ts
export interface ApprovalEventPayload
```

<Heading as="h4" id="api-approvaleventpayload-allowalways-propertysignature"><code>ApprovalEventPayload.allowAlways</code></Heading>

```ts
readonly allowAlways: boolean;
```

<Heading as="h4" id="api-approvaleventpayload-askid-propertysignature"><code>ApprovalEventPayload.askId</code></Heading>

```ts
readonly askId: string;
```

<Heading as="h4" id="api-approvaleventpayload-callid-propertysignature"><code>ApprovalEventPayload.callId</code></Heading>

```ts
readonly callId: string;
```

<Heading as="h4" id="api-approvaleventpayload-tool-propertysignature"><code>ApprovalEventPayload.tool</code></Heading>

```ts
readonly tool: string;
```

<Heading as="h4" id="api-approvaleventpayload-verdict-propertysignature"><code>ApprovalEventPayload.verdict</code></Heading>

```ts
readonly verdict: string;
```

<Heading as="h3" id="api-archivedconversationmessage-interface"><code>ArchivedConversationMessage</code></Heading>

One conversation entry in a compaction archive.

```ts
export interface ArchivedConversationMessage
```

<Heading as="h4" id="api-archivedconversationmessage-parts-propertysignature"><code>ArchivedConversationMessage.parts</code></Heading>

```ts
readonly parts: readonly EventContent[];
```

<Heading as="h4" id="api-archivedconversationmessage-providerphase-propertysignature"><code>ArchivedConversationMessage.providerPhase</code></Heading>

```ts
readonly providerPhase: string;
```

<Heading as="h4" id="api-archivedconversationmessage-reasoning-propertysignature"><code>ArchivedConversationMessage.reasoning</code></Heading>

```ts
readonly reasoning: string;
```

<Heading as="h4" id="api-archivedconversationmessage-reasoningitemid-propertysignature"><code>ArchivedConversationMessage.reasoningItemId</code></Heading>

```ts
readonly reasoningItemId: string;
```

<Heading as="h4" id="api-archivedconversationmessage-role-propertysignature"><code>ArchivedConversationMessage.role</code></Heading>

```ts
readonly role: string;
```

<Heading as="h4" id="api-archivedconversationmessage-text-propertysignature"><code>ArchivedConversationMessage.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-archivedconversationmessage-toolcalls-propertysignature"><code>ArchivedConversationMessage.toolCalls</code></Heading>

```ts
readonly toolCalls: readonly ToolCallEventPayload[];
```

<Heading as="h4" id="api-archivedconversationmessage-toolresult-propertysignature"><code>ArchivedConversationMessage.toolResult</code></Heading>

```ts
readonly toolResult?: ToolResultEventPayload | undefined;
```

<Heading as="h3" id="api-attachedrun-interface"><code>AttachedRun</code></Heading>

A durable activity stream bound to one run.

```ts
export interface AttachedRun extends SessionActivity
```

Callable members: [`approve()`](#api-attachedrun-approve-methodsignature), [`cancel()`](#api-attachedrun-cancel-methodsignature), [`resolveAsk()`](#api-attachedrun-resolveask-methodsignature), [`steer()`](#api-attachedrun-steer-methodsignature)

<Heading as="h4" id="api-attachedrun-approve-methodsignature"><code>AttachedRun.approve</code></Heading>

Reports that approval controls are unavailable on durable attachments.

```ts
approve(askId: string, allow: boolean): Promise<never>;
```

Parameters:

- `askId` (`string`): Permission-ask ID, retained for parity with a live run.
- `allow` (`boolean`): Boolean verdict, retained for parity with a live run.

Returns: `Promise<never>`: A rejected promise.

Throws: `UnsupportedFeatureError` for every call.

<Heading as="h4" id="api-attachedrun-cancel-methodsignature"><code>AttachedRun.cancel</code></Heading>

Cancels the attached run using its exact run ID.

```ts
cancel(): Promise<void>;
```

Returns: `Promise<void>`: A promise that resolves after the cancellation request is accepted.

<Heading as="h4" id="api-attachedrun-live-propertysignature"><code>AttachedRun.live</code></Heading>

True until this attachment observes its run's terminal result.

```ts
readonly live: boolean;
```

<Heading as="h4" id="api-attachedrun-resolveask-methodsignature"><code>AttachedRun.resolveAsk</code></Heading>

Reports that ask resolution is unavailable on durable attachments.

```ts
resolveAsk(askId: string, verdict: PermissionVerdict): Promise<never>;
```

Parameters:

- `askId` (`string`): Permission-ask ID, retained for parity with a live run.
- `verdict` (`PermissionVerdict`): Permission verdict, retained for parity with a live run.

Returns: `Promise<never>`: A rejected promise.

Throws: `UnsupportedFeatureError` for every call.

<Heading as="h4" id="api-attachedrun-runid-propertysignature"><code>AttachedRun.runId</code></Heading>

```ts
readonly runId: string;
```

<Heading as="h4" id="api-attachedrun-steer-methodsignature"><code>AttachedRun.steer</code></Heading>

Reports that steering is unavailable on durable attachments.

```ts
steer(text: string): Promise<never>;
```

Parameters:

- `text` (`string`): Steering text, retained for parity with a live run.

Returns: `Promise<never>`: A rejected promise.

Throws: `UnsupportedFeatureError` for every call.

<Heading as="h3" id="api-attachoptions-interface"><code>AttachOptions</code></Heading>

Where an attached run begins reading its durable activity.

```ts
export interface AttachOptions
```

<Heading as="h4" id="api-attachoptions-from-propertysignature"><code>AttachOptions.from</code></Heading>

Starts with events received after attachment, discarding the existing replay locally.

```ts
from?: "now" | "start" | SdkCursor;
```

<Heading as="h4" id="api-attachoptions-includelogonly-propertysignature"><code>AttachOptions.includeLogOnly</code></Heading>

Includes durable records omitted by the high-level view by default.

```ts
includeLogOnly?: boolean;
```

<Heading as="h4" id="api-attachoptions-signal-propertysignature"><code>AttachOptions.signal</code></Heading>

Detaches this view when aborted; it never cancels a run.

```ts
signal?: AbortSignal;
```

<Heading as="h3" id="api-audiopromptpart-interface"><code>AudioPromptPart</code></Heading>

Audio in a structured prompt.

```ts
export interface AudioPromptPart
```

<Heading as="h4" id="api-audiopromptpart-bytes-propertysignature"><code>AudioPromptPart.bytes</code></Heading>

```ts
readonly bytes?: Uint8Array;
```

<Heading as="h4" id="api-audiopromptpart-kind-propertysignature"><code>AudioPromptPart.kind</code></Heading>

```ts
readonly kind: "audio";
```

<Heading as="h4" id="api-audiopromptpart-mimetype-propertysignature"><code>AudioPromptPart.mimeType</code></Heading>

```ts
readonly mimeType: string;
```

<Heading as="h4" id="api-audiopromptpart-url-propertysignature"><code>AudioPromptPart.url</code></Heading>

```ts
readonly url?: string;
```

<Heading as="h3" id="api-clearsessionoptions-interface"><code>ClearSessionOptions</code></Heading>

Optional overrides accepted when clearing a session.

```ts
export interface ClearSessionOptions
```

<Heading as="h4" id="api-clearsessionoptions-worktreeselector-propertysignature"><code>ClearSessionOptions.worktreeSelector</code></Heading>

Opaque source-scoped selector for an existing worktree.

```ts
worktreeSelector?: string;
```

<Heading as="h3" id="api-client-interface"><code>Client</code></Heading>

The high-level Mecatl client.

```ts
export interface Client
```

Callable members: [`[Symbol.asyncDispose]()`](#api-client-symbol-asyncdispose-methodsignature), [`close()`](#api-client-close-methodsignature)

<Heading as="h4" id="api-client-symbol-asyncdispose-methodsignature"><code>Client[Symbol.asyncDispose]</code></Heading>

Releases the same resources as close() when used with await using.

```ts
[Symbol.asyncDispose](): Promise<void>;
```

Returns: `Promise<void>`

<Heading as="h4" id="api-client-agents-propertysignature"><code>Client.agents</code></Heading>

```ts
readonly agents: Agents;
```

<Heading as="h4" id="api-client-close-methodsignature"><code>Client.close</code></Heading>

Releases activity, transports, and resources owned by this client.

```ts
close(): Promise<void>;
```

Returns: `Promise<void>`

<Heading as="h4" id="api-client-commands-propertysignature"><code>Client.commands</code></Heading>

```ts
readonly commands: Commands;
```

<Heading as="h4" id="api-client-dreamplans-propertysignature"><code>Client.dreamPlans</code></Heading>

```ts
readonly dreamPlans: DreamPlans;
```

<Heading as="h4" id="api-client-learnedskills-propertysignature"><code>Client.learnedSkills</code></Heading>

```ts
readonly learnedSkills: LearnedSkills;
```

<Heading as="h4" id="api-client-learningattempts-propertysignature"><code>Client.learningAttempts</code></Heading>

```ts
readonly learningAttempts: LearningAttempts;
```

<Heading as="h4" id="api-client-learningproposals-propertysignature"><code>Client.learningProposals</code></Heading>

```ts
readonly learningProposals: LearningProposals;
```

<Heading as="h4" id="api-client-mcp-propertysignature"><code>Client.mcp</code></Heading>

```ts
readonly mcp: McpInventory;
```

<Heading as="h4" id="api-client-models-propertysignature"><code>Client.models</code></Heading>

```ts
readonly models: Models;
```

<Heading as="h4" id="api-client-reflection-propertysignature"><code>Client.reflection</code></Heading>

```ts
readonly reflection: Reflection;
```

<Heading as="h4" id="api-client-schedules-propertysignature"><code>Client.schedules</code></Heading>

```ts
readonly schedules: Schedules;
```

<Heading as="h4" id="api-client-server-propertysignature"><code>Client.server</code></Heading>

```ts
readonly server: Server;
```

<Heading as="h4" id="api-client-sessions-propertysignature"><code>Client.sessions</code></Heading>

```ts
readonly sessions: Sessions;
```

<Heading as="h4" id="api-client-skills-propertysignature"><code>Client.skills</code></Heading>

```ts
readonly skills: Skills;
```

<Heading as="h4" id="api-client-soul-propertysignature"><code>Client.soul</code></Heading>

```ts
readonly soul: Soul;
```

<Heading as="h4" id="api-client-status-propertysignature"><code>Client.status</code></Heading>

```ts
readonly status: ConnectionStatusStore;
```

<Heading as="h4" id="api-client-storage-propertysignature"><code>Client.storage</code></Heading>

```ts
readonly storage: Storage;
```

<Heading as="h4" id="api-client-teams-propertysignature"><code>Client.teams</code></Heading>

```ts
readonly teams: Teams;
```

<Heading as="h4" id="api-client-usermodel-propertysignature"><code>Client.userModel</code></Heading>

```ts
readonly userModel: UserModel;
```

<Heading as="h4" id="api-client-worktrees-propertysignature"><code>Client.worktrees</code></Heading>

```ts
readonly worktrees: Worktrees;
```

<Heading as="h3" id="api-clientdiagnosticsoptions-interface"><code>ClientDiagnosticsOptions</code></Heading>

Client-construction option shared by SDK entry points that emit local diagnostics.

```ts
export interface ClientDiagnosticsOptions
```

<Heading as="h4" id="api-clientdiagnosticsoptions-diagnostics-propertysignature"><code>ClientDiagnosticsOptions.diagnostics</code></Heading>

Receives SDK-local diagnostics. Nothing is written to console by default.

```ts
diagnostics?: DiagnosticsSink;
```

<Heading as="h3" id="api-commands-interface"><code>Commands</code></Heading>

Session-scoped slash-command inventory operations.

```ts
export interface Commands
```

Callable members: [`list()`](#api-commands-list-methodsignature)

<Heading as="h4" id="api-commands-list-methodsignature"><code>Commands.list</code></Heading>

Lists slash commands available to a session.

```ts
list(request: ListCommandsRequest, options?: RequestOptions): Promise<ListCommandsResponse>;
```

Parameters:

- `request` (`ListCommandsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListCommandsResponse>`

<Heading as="h3" id="api-compactionarchiveeventpayload-interface"><code>CompactionArchiveEventPayload</code></Heading>

The payload of a `compaction.archive` replay event.

```ts
export interface CompactionArchiveEventPayload
```

<Heading as="h4" id="api-compactionarchiveeventpayload-replaced-propertysignature"><code>CompactionArchiveEventPayload.replaced</code></Heading>

```ts
readonly replaced: readonly ArchivedConversationMessage[];
```

<Heading as="h3" id="api-connectionstatusstore-interface"><code>ConnectionStatusStore</code></Heading>

A multicast view of the client's latest connection status.

```ts
export interface ConnectionStatusStore
```

Callable members: [`getSnapshot()`](#api-connectionstatusstore-getsnapshot-methodsignature), [`subscribe()`](#api-connectionstatusstore-subscribe-methodsignature)

<Heading as="h4" id="api-connectionstatusstore-getsnapshot-methodsignature"><code>ConnectionStatusStore.getSnapshot</code></Heading>

Returns the client's current connection status.

```ts
getSnapshot(): ConnectionStatus;
```

Returns: `ConnectionStatus`

<Heading as="h4" id="api-connectionstatusstore-subscribe-methodsignature"><code>ConnectionStatusStore.subscribe</code></Heading>

Registers a listener and returns a function that removes it.

```ts
subscribe(listener: ConnectionStatusListener): () => void;
```

Parameters:

- `listener` (`ConnectionStatusListener`)

Returns: `() => void`

<Heading as="h3" id="api-createsessionoptions-interface"><code>CreateSessionOptions</code></Heading>

Session-creation fields map directly onto CreateSessionRequest.

```ts
export interface CreateSessionOptions
```

<Heading as="h4" id="api-createsessionoptions-debugmcpservers-propertysignature"><code>CreateSessionOptions.debugMcpServers</code></Heading>

Configured server-global MCP servers selected for a diagnostic session.

```ts
debugMcpServers?: string[];
```

<Heading as="h4" id="api-createsessionoptions-debugtargetsessionid-propertysignature"><code>CreateSessionOptions.debugTargetSessionId</code></Heading>

Existing session ID used to create a separate diagnostic session.

```ts
debugTargetSessionId?: string;
```

<Heading as="h4" id="api-createsessionoptions-limits-propertysignature"><code>CreateSessionOptions.limits</code></Heading>

Stop conditions for the new session.

```ts
limits?: SessionLimits;
```

<Heading as="h4" id="api-createsessionoptions-mcpservers-propertysignature"><code>CreateSessionOptions.mcpServers</code></Heading>

Client-provided streaming-HTTP MCP servers mounted for this session.

```ts
mcpServers?: SessionMcpServer[];
```

<Heading as="h4" id="api-createsessionoptions-mode-propertysignature"><code>CreateSessionOptions.mode</code></Heading>

Permission posture for the new session.

```ts
mode?: SessionMode;
```

<Heading as="h4" id="api-createsessionoptions-modelid-propertysignature"><code>CreateSessionOptions.modelId</code></Heading>

Model selector within `providerId`.

```ts
modelId?: string;
```

<Heading as="h4" id="api-createsessionoptions-profile-propertysignature"><code>CreateSessionOptions.profile</code></Heading>

Tool-surface profile, or the deployment default when omitted.

```ts
profile?: string;
```

<Heading as="h4" id="api-createsessionoptions-providerid-propertysignature"><code>CreateSessionOptions.providerId</code></Heading>

Configured model-provider ID, or the deployment default when omitted.

```ts
providerId?: string;
```

<Heading as="h4" id="api-createsessionoptions-reasoningeffort-propertysignature"><code>CreateSessionOptions.reasoningEffort</code></Heading>

Requested reasoning-effort tier. The server reports the effective value.

```ts
reasoningEffort?: string;
```

<Heading as="h3" id="api-createteamoptions-interface"><code>CreateTeamOptions</code></Heading>

Options used to create a server-owned team.

```ts
export interface CreateTeamOptions
```

<Heading as="h4" id="api-createteamoptions-goal-propertysignature"><code>CreateTeamOptions.goal</code></Heading>

Objective supplied to the coordinating member.

```ts
goal?: string;
```

<Heading as="h4" id="api-createteamoptions-maxteamtokens-propertysignature"><code>CreateTeamOptions.maxTeamTokens</code></Heading>

Optional team-wide token limit. The daemon applies the lower of this value and its configured cap. Omit it to use the daemon's cap.

```ts
maxTeamTokens?: number;
```

<Heading as="h4" id="api-createteamoptions-members-propertysignature"><code>CreateTeamOptions.members</code></Heading>

Initial members enrolled atomically.

```ts
members?: readonly TeamMemberOptions[];
```

<Heading as="h4" id="api-createteamoptions-name-propertysignature"><code>CreateTeamOptions.name</code></Heading>

Optional human-readable team label.

```ts
name?: string;
```

<Heading as="h4" id="api-createteamoptions-sessionid-propertysignature"><code>CreateTeamOptions.sessionId</code></Heading>

Session that owns the team.

```ts
sessionId: string;
```

<Heading as="h3" id="api-credentialoptions-interface"><code>CredentialOptions</code></Heading>

Static or per-request credentials accepted by SDK transports.

```ts
export interface CredentialOptions
```

<Heading as="h4" id="api-credentialoptions-credentialprovider-propertysignature"><code>CredentialOptions.credentialProvider</code></Heading>

Invoked for every request, after static headers have been copied.

```ts
credentialProvider?: CredentialProvider;
```

<Heading as="h4" id="api-credentialoptions-headers-propertysignature"><code>CredentialOptions.headers</code></Heading>

Headers copied once at transport construction.

```ts
headers?: HeadersInit;
```

<Heading as="h3" id="api-diagnosticrecord-interface"><code>DiagnosticRecord</code></Heading>

A structured SDK-local observation that is separate from the server event stream.

```ts
export interface DiagnosticRecord
```

<Heading as="h4" id="api-diagnosticrecord-cause-propertysignature"><code>DiagnosticRecord.cause</code></Heading>

The original failure value when the diagnostic observes a thrown cause.

```ts
readonly cause?: unknown;
```

<Heading as="h4" id="api-diagnosticrecord-code-propertysignature"><code>DiagnosticRecord.code</code></Heading>

Stable machine-readable identifier for the observation.

```ts
readonly code: string;
```

<Heading as="h4" id="api-diagnosticrecord-fields-propertysignature"><code>DiagnosticRecord.fields</code></Heading>

Typed context that is safe to expose to the application.

```ts
readonly fields: Readonly<Record<string, DiagnosticFieldValue>>;
```

<Heading as="h4" id="api-diagnosticrecord-level-propertysignature"><code>DiagnosticRecord.level</code></Heading>

Diagnostic severity.

```ts
readonly level: DiagnosticLevel;
```

<Heading as="h4" id="api-diagnosticrecord-message-propertysignature"><code>DiagnosticRecord.message</code></Heading>

Human-readable summary.

```ts
readonly message: string;
```

<Heading as="h3" id="api-dreamplans-interface"><code>DreamPlans</code></Heading>

Dream-plan generation and server-owned decision operations.

```ts
export interface DreamPlans
```

Callable members: [`decide()`](#api-dreamplans-decide-methodsignature), [`generate()`](#api-dreamplans-generate-methodsignature)

<Heading as="h4" id="api-dreamplans-decide-methodsignature"><code>DreamPlans.decide</code></Heading>

Applies or dismisses a generated dream plan.

```ts
decide(request: DecideDreamPlanRequest, options?: RequestOptions): Promise<DecideDreamPlanResponse>;
```

Parameters:

- `request` (`DecideDreamPlanRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<DecideDreamPlanResponse>`

<Heading as="h4" id="api-dreamplans-generate-methodsignature"><code>DreamPlans.generate</code></Heading>

Generates a bounded-lifetime dream plan.

```ts
generate(request: GenerateDreamPlanRequest, options?: RequestOptions): Promise<GenerateDreamPlanResponse>;
```

Parameters:

- `request` (`GenerateDreamPlanRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GenerateDreamPlanResponse>`

<Heading as="h3" id="api-dreamtargetcapability-interface"><code>DreamTargetCapability</code></Heading>

Manual dream operations available for one target.

```ts
export interface DreamTargetCapability
```

<Heading as="h4" id="api-dreamtargetcapability-decide-propertysignature"><code>DreamTargetCapability.decide</code></Heading>

```ts
readonly decide: boolean;
```

<Heading as="h4" id="api-dreamtargetcapability-generate-propertysignature"><code>DreamTargetCapability.generate</code></Heading>

```ts
readonly generate: boolean;
```

<Heading as="h4" id="api-dreamtargetcapability-unavailablereason-propertysignature"><code>DreamTargetCapability.unavailableReason</code></Heading>

```ts
readonly unavailableReason?: string;
```

<Heading as="h3" id="api-eventcommon-interface"><code>EventCommon</code></Heading>

Fields decoded for every event, including future event kinds.

```ts
export interface EventCommon
```

<Heading as="h4" id="api-eventcommon-runid-propertysignature"><code>EventCommon.runId</code></Heading>

```ts
readonly runId: string;
```

<Heading as="h4" id="api-eventcommon-seq-propertysignature"><code>EventCommon.seq</code></Heading>

```ts
readonly seq: bigint;
```

<Heading as="h4" id="api-eventcommon-text-propertysignature"><code>EventCommon.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-eventcommon-turn-propertysignature"><code>EventCommon.turn</code></Heading>

```ts
readonly turn: number;
```

<Heading as="h4" id="api-eventcommon-usage-propertysignature"><code>EventCommon.usage</code></Heading>

```ts
readonly usage: EventUsage | undefined;
```

<Heading as="h3" id="api-eventcontent-interface"><code>EventContent</code></Heading>

One media part as represented on the protobuf event payloads.

```ts
export interface EventContent
```

<Heading as="h4" id="api-eventcontent-data-propertysignature"><code>EventContent.data</code></Heading>

```ts
readonly data: Uint8Array;
```

<Heading as="h4" id="api-eventcontent-kind-propertysignature"><code>EventContent.kind</code></Heading>

```ts
readonly kind: 0 | 1 | 2;
```

<Heading as="h4" id="api-eventcontent-mimetype-propertysignature"><code>EventContent.mimeType</code></Heading>

```ts
readonly mimeType: string;
```

<Heading as="h4" id="api-eventcontent-url-propertysignature"><code>EventContent.url</code></Heading>

```ts
readonly url: string;
```

<Heading as="h3" id="api-eventcontentblock-interface"><code>EventContentBlock</code></Heading>

One raw protobuf content block carried by a tool result.

```ts
export interface EventContentBlock
```

<Heading as="h4" id="api-eventcontentblock-audience-propertysignature"><code>EventContentBlock.audience</code></Heading>

```ts
readonly audience: readonly string[];
```

<Heading as="h4" id="api-eventcontentblock-data-propertysignature"><code>EventContentBlock.data</code></Heading>

```ts
readonly data: Uint8Array;
```

<Heading as="h4" id="api-eventcontentblock-description-propertysignature"><code>EventContentBlock.description</code></Heading>

```ts
readonly description: string;
```

<Heading as="h4" id="api-eventcontentblock-kind-propertysignature"><code>EventContentBlock.kind</code></Heading>

```ts
readonly kind: 0 | 1 | 2 | 3 | 4 | 5 | 6;
```

<Heading as="h4" id="api-eventcontentblock-lastmodified-propertysignature"><code>EventContentBlock.lastModified</code></Heading>

```ts
readonly lastModified: string;
```

<Heading as="h4" id="api-eventcontentblock-mimetype-propertysignature"><code>EventContentBlock.mimeType</code></Heading>

```ts
readonly mimeType: string;
```

<Heading as="h4" id="api-eventcontentblock-name-propertysignature"><code>EventContentBlock.name</code></Heading>

```ts
readonly name: string;
```

<Heading as="h4" id="api-eventcontentblock-priority-propertysignature"><code>EventContentBlock.priority</code></Heading>

```ts
readonly priority: number;
```

<Heading as="h4" id="api-eventcontentblock-size-propertysignature"><code>EventContentBlock.size</code></Heading>

```ts
readonly size: bigint;
```

<Heading as="h4" id="api-eventcontentblock-text-propertysignature"><code>EventContentBlock.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-eventcontentblock-title-propertysignature"><code>EventContentBlock.title</code></Heading>

```ts
readonly title: string;
```

<Heading as="h4" id="api-eventcontentblock-url-propertysignature"><code>EventContentBlock.url</code></Heading>

```ts
readonly url: string;
```

<Heading as="h3" id="api-eventpayloads-interface"><code>EventPayloads</code></Heading>

Maps every supported event kind to its typed payload.

```ts
export interface EventPayloads
```

<Heading as="h4" id="api-eventpayloads-authorization-required-propertysignature"><code>EventPayloads["authorization.required"]</code></Heading>

```ts
readonly "authorization.required": AuthorizationEventPayload;
```

<Heading as="h4" id="api-eventpayloads-authorization-resolved-propertysignature"><code>EventPayloads["authorization.resolved"]</code></Heading>

```ts
readonly "authorization.resolved": AuthorizationEventPayload;
```

<Heading as="h4" id="api-eventpayloads-compaction-archive-propertysignature"><code>EventPayloads["compaction.archive"]</code></Heading>

```ts
readonly "compaction.archive": CompactionArchiveEventPayload;
```

<Heading as="h4" id="api-eventpayloads-message-delta-propertysignature"><code>EventPayloads["message.delta"]</code></Heading>

```ts
readonly "message.delta": undefined;
```

<Heading as="h4" id="api-eventpayloads-model-retry-propertysignature"><code>EventPayloads["model.retry"]</code></Heading>

```ts
readonly "model.retry": ModelRetryEventPayload;
```

<Heading as="h4" id="api-eventpayloads-network-attempt-propertysignature"><code>EventPayloads["network.attempt"]</code></Heading>

```ts
readonly "network.attempt": undefined;
```

<Heading as="h4" id="api-eventpayloads-parallel-branch-propertysignature"><code>EventPayloads["parallel.branch"]</code></Heading>

```ts
readonly "parallel.branch": ParallelEventPayload;
```

<Heading as="h4" id="api-eventpayloads-parallel-end-propertysignature"><code>EventPayloads["parallel.end"]</code></Heading>

```ts
readonly "parallel.end": ParallelEventPayload;
```

<Heading as="h4" id="api-eventpayloads-parallel-start-propertysignature"><code>EventPayloads["parallel.start"]</code></Heading>

```ts
readonly "parallel.start": ParallelEventPayload;
```

<Heading as="h4" id="api-eventpayloads-permission-ask-propertysignature"><code>EventPayloads["permission.ask"]</code></Heading>

```ts
readonly "permission.ask": PermissionAskEventPayload;
```

<Heading as="h4" id="api-eventpayloads-permission-retract-propertysignature"><code>EventPayloads["permission.retract"]</code></Heading>

```ts
readonly "permission.retract": PermissionAskEventPayload;
```

<Heading as="h4" id="api-eventpayloads-provider-route-propertysignature"><code>EventPayloads["provider.route"]</code></Heading>

```ts
readonly "provider.route": undefined;
```

<Heading as="h4" id="api-eventpayloads-reasoning-delta-propertysignature"><code>EventPayloads["reasoning.delta"]</code></Heading>

```ts
readonly "reasoning.delta": undefined;
```

<Heading as="h4" id="api-eventpayloads-request-manifest-propertysignature"><code>EventPayloads["request.manifest"]</code></Heading>

```ts
readonly "request.manifest": undefined;
```

<Heading as="h4" id="api-eventpayloads-schedule-failed-propertysignature"><code>EventPayloads["schedule.failed"]</code></Heading>

```ts
readonly "schedule.failed": ScheduleEventPayload;
```

<Heading as="h4" id="api-eventpayloads-schedule-fired-propertysignature"><code>EventPayloads["schedule.fired"]</code></Heading>

```ts
readonly "schedule.fired": ScheduleEventPayload;
```

<Heading as="h4" id="api-eventpayloads-schedule-skipped-propertysignature"><code>EventPayloads["schedule.skipped"]</code></Heading>

```ts
readonly "schedule.skipped": ScheduleEventPayload;
```

<Heading as="h4" id="api-eventpayloads-session-init-propertysignature"><code>EventPayloads["session.init"]</code></Heading>

```ts
readonly "session.init": undefined;
```

<Heading as="h4" id="api-eventpayloads-session-title-propertysignature"><code>EventPayloads["session.title"]</code></Heading>

```ts
readonly "session.title": SessionTitleEventPayload;
```

<Heading as="h4" id="api-eventpayloads-steer-outcome-propertysignature"><code>EventPayloads["steer.outcome"]</code></Heading>

```ts
readonly "steer.outcome": SteerOutcomeEventPayload;
```

<Heading as="h4" id="api-eventpayloads-subagent-end-propertysignature"><code>EventPayloads["subagent.end"]</code></Heading>

```ts
readonly "subagent.end": SubagentEventPayload;
```

<Heading as="h4" id="api-eventpayloads-subagent-start-propertysignature"><code>EventPayloads["subagent.start"]</code></Heading>

```ts
readonly "subagent.start": SubagentEventPayload;
```

<Heading as="h4" id="api-eventpayloads-subagent-tool-propertysignature"><code>EventPayloads["subagent.tool"]</code></Heading>

```ts
readonly "subagent.tool": SubagentEventPayload;
```

<Heading as="h4" id="api-eventpayloads-team-end-propertysignature"><code>EventPayloads["team.end"]</code></Heading>

```ts
readonly "team.end": TeamEventPayload;
```

<Heading as="h4" id="api-eventpayloads-team-findings-propertysignature"><code>EventPayloads["team.findings"]</code></Heading>

```ts
readonly "team.findings": TeamEventPayload;
```

<Heading as="h4" id="api-eventpayloads-team-member-propertysignature"><code>EventPayloads["team.member"]</code></Heading>

```ts
readonly "team.member": TeamEventPayload;
```

<Heading as="h4" id="api-eventpayloads-team-start-propertysignature"><code>EventPayloads["team.start"]</code></Heading>

```ts
readonly "team.start": TeamEventPayload;
```

<Heading as="h4" id="api-eventpayloads-team-tasks-propertysignature"><code>EventPayloads["team.tasks"]</code></Heading>

```ts
readonly "team.tasks": TeamEventPayload;
```

<Heading as="h4" id="api-eventpayloads-tool-call-propertysignature"><code>EventPayloads["tool.call"]</code></Heading>

```ts
readonly "tool.call": ToolCallEventPayload;
```

<Heading as="h4" id="api-eventpayloads-tool-progress-propertysignature"><code>EventPayloads["tool.progress"]</code></Heading>

```ts
readonly "tool.progress": undefined;
```

<Heading as="h4" id="api-eventpayloads-tool-result-propertysignature"><code>EventPayloads["tool.result"]</code></Heading>

```ts
readonly "tool.result": ToolResultEventPayload;
```

<Heading as="h4" id="api-eventpayloads-turn-end-propertysignature"><code>EventPayloads["turn.end"]</code></Heading>

```ts
readonly "turn.end": TurnEndEventPayload;
```

<Heading as="h4" id="api-eventpayloads-turn-start-propertysignature"><code>EventPayloads["turn.start"]</code></Heading>

```ts
readonly "turn.start": undefined;
```

<Heading as="h4" id="api-eventpayloads-approval-propertysignature"><code>EventPayloads.approval</code></Heading>

```ts
readonly approval: ApprovalEventPayload;
```

<Heading as="h4" id="api-eventpayloads-compaction-propertysignature"><code>EventPayloads.compaction</code></Heading>

```ts
readonly compaction: undefined;
```

<Heading as="h4" id="api-eventpayloads-hook-propertysignature"><code>EventPayloads.hook</code></Heading>

```ts
readonly hook: HookEventPayload;
```

<Heading as="h4" id="api-eventpayloads-no-progress-propertysignature"><code>EventPayloads.no_progress</code></Heading>

```ts
readonly no_progress: undefined;
```

<Heading as="h4" id="api-eventpayloads-recover-notice-propertysignature"><code>EventPayloads.recover_notice</code></Heading>

```ts
readonly recover_notice: undefined;
```

<Heading as="h4" id="api-eventpayloads-result-propertysignature"><code>EventPayloads.result</code></Heading>

```ts
readonly result: ResultEventPayload;
```

<Heading as="h4" id="api-eventpayloads-steer-propertysignature"><code>EventPayloads.steer</code></Heading>

```ts
readonly steer: SteerEventPayload;
```

<Heading as="h4" id="api-eventpayloads-user-prompt-propertysignature"><code>EventPayloads.user_prompt</code></Heading>

```ts
readonly user_prompt: UserPromptEventPayload;
```

<Heading as="h3" id="api-eventusage-interface"><code>EventUsage</code></Heading>

Token accounting carried by usage-bearing events.

```ts
export interface EventUsage
```

<Heading as="h4" id="api-eventusage-cachereadtokens-propertysignature"><code>EventUsage.cacheReadTokens</code></Heading>

```ts
readonly cacheReadTokens: bigint;
```

<Heading as="h4" id="api-eventusage-cachewritetokens-propertysignature"><code>EventUsage.cacheWriteTokens</code></Heading>

```ts
readonly cacheWriteTokens: bigint;
```

<Heading as="h4" id="api-eventusage-inputtokens-propertysignature"><code>EventUsage.inputTokens</code></Heading>

```ts
readonly inputTokens: bigint;
```

<Heading as="h4" id="api-eventusage-outputtokens-propertysignature"><code>EventUsage.outputTokens</code></Heading>

```ts
readonly outputTokens: bigint;
```

<Heading as="h4" id="api-eventusage-reasoningtokens-propertysignature"><code>EventUsage.reasoningTokens</code></Heading>

```ts
readonly reasoningTokens: bigint;
```

<Heading as="h3" id="api-forksessionoptions-interface"><code>ForkSessionOptions</code></Heading>

Optional overrides accepted when forking a session.

```ts
export interface ForkSessionOptions
```

<Heading as="h4" id="api-forksessionoptions-modelid-propertysignature"><code>ForkSessionOptions.modelId</code></Heading>

Model selector within `providerId`.

```ts
modelId?: string;
```

<Heading as="h4" id="api-forksessionoptions-providerid-propertysignature"><code>ForkSessionOptions.providerId</code></Heading>

Configured model-provider ID.

```ts
providerId?: string;
```

<Heading as="h4" id="api-forksessionoptions-reasoningeffort-propertysignature"><code>ForkSessionOptions.reasoningEffort</code></Heading>

Requested reasoning-effort tier for the forked session.

```ts
reasoningEffort?: string;
```

<Heading as="h4" id="api-forksessionoptions-title-propertysignature"><code>ForkSessionOptions.title</code></Heading>

Human-readable title for the forked session.

```ts
title?: string;
```

<Heading as="h4" id="api-forksessionoptions-worktreeselector-propertysignature"><code>ForkSessionOptions.worktreeSelector</code></Heading>

Opaque source-scoped selector for an existing worktree.

```ts
worktreeSelector?: string;
```

<Heading as="h3" id="api-hookeventpayload-interface"><code>HookEventPayload</code></Heading>

The payload of a `hook` event.

```ts
export interface HookEventPayload
```

<Heading as="h4" id="api-hookeventpayload-callid-propertysignature"><code>HookEventPayload.callId</code></Heading>

```ts
readonly callId: string;
```

<Heading as="h4" id="api-hookeventpayload-decision-propertysignature"><code>HookEventPayload.decision</code></Heading>

```ts
readonly decision: 0 | 1 | 2 | 3 | 4;
```

<Heading as="h4" id="api-hookeventpayload-phase-propertysignature"><code>HookEventPayload.phase</code></Heading>

```ts
readonly phase: string;
```

<Heading as="h4" id="api-hookeventpayload-tool-propertysignature"><code>HookEventPayload.tool</code></Heading>

```ts
readonly tool: string;
```

<Heading as="h3" id="api-httptransportoptions-interface"><code>HttpTransportOptions</code></Heading>

Options for the browser-compatible HTTP and SSE transport.

```ts
export interface HttpTransportOptions extends CredentialOptions
```

<Heading as="h4" id="api-httptransportoptions-baseurl-propertysignature"><code>HttpTransportOptions.baseUrl</code></Heading>

HTTP API base URL. Relative values resolve against the browser origin.

```ts
baseUrl: string;
```

<Heading as="h4" id="api-httptransportoptions-credentials-propertysignature"><code>HttpTransportOptions.credentials</code></Heading>

Passed to every request made by this transport.

```ts
credentials?: RequestCredentials;
```

<Heading as="h4" id="api-httptransportoptions-fetch-propertysignature"><code>HttpTransportOptions.fetch</code></Heading>

When supplied, global fetch is never consulted.

```ts
fetch?: typeof globalThis.fetch;
```

<Heading as="h3" id="api-imagepromptpart-interface"><code>ImagePromptPart</code></Heading>

An image in a structured prompt.

```ts
export interface ImagePromptPart
```

<Heading as="h4" id="api-imagepromptpart-bytes-propertysignature"><code>ImagePromptPart.bytes</code></Heading>

```ts
readonly bytes?: Uint8Array;
```

<Heading as="h4" id="api-imagepromptpart-kind-propertysignature"><code>ImagePromptPart.kind</code></Heading>

```ts
readonly kind: "image";
```

<Heading as="h4" id="api-imagepromptpart-mimetype-propertysignature"><code>ImagePromptPart.mimeType</code></Heading>

```ts
readonly mimeType: string;
```

<Heading as="h4" id="api-imagepromptpart-url-propertysignature"><code>ImagePromptPart.url</code></Heading>

```ts
readonly url?: string;
```

<Heading as="h3" id="api-injectedtransportoptions-interface"><code>InjectedTransportOptions</code></Heading>

Options accepted by the isomorphic entry point when injecting a transport.

```ts
export interface InjectedTransportOptions
```

<Heading as="h4" id="api-injectedtransportoptions-transport-propertysignature"><code>InjectedTransportOptions.transport</code></Heading>

A caller-owned Connect-ES transport.

```ts
transport: Transport;
```

<Heading as="h4" id="api-injectedtransportoptions-transportkind-propertysignature"><code>InjectedTransportOptions.transportKind</code></Heading>

Required only when an unregistered transport speaks the HTTP/JSON/SSE protocol.

```ts
transportKind?: TransportKind;
```

<Heading as="h3" id="api-learnedskills-interface"><code>LearnedSkills</code></Heading>

Learned-skill inventory and server-owned lifecycle operations.

```ts
export interface LearnedSkills
```

Callable members: [`activate()`](#api-learnedskills-activate-methodsignature), [`archive()`](#api-learnedskills-archive-methodsignature), [`diffVersions()`](#api-learnedskills-diffversions-methodsignature), [`get()`](#api-learnedskills-get-methodsignature), [`list()`](#api-learnedskills-list-methodsignature), [`listChanges()`](#api-learnedskills-listchanges-methodsignature), [`reject()`](#api-learnedskills-reject-methodsignature), [`rollback()`](#api-learnedskills-rollback-methodsignature)

<Heading as="h4" id="api-learnedskills-activate-methodsignature"><code>LearnedSkills.activate</code></Heading>

Activates a learned skill.

```ts
activate(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;
```

Parameters:

- `request` (`MutateLearnedSkillRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearnedSkillResponse>`

<Heading as="h4" id="api-learnedskills-archive-methodsignature"><code>LearnedSkills.archive</code></Heading>

Archives a learned skill.

```ts
archive(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;
```

Parameters:

- `request` (`MutateLearnedSkillRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearnedSkillResponse>`

<Heading as="h4" id="api-learnedskills-diffversions-methodsignature"><code>LearnedSkills.diffVersions</code></Heading>

Compares two versions of a learned skill.

```ts
diffVersions(request: DiffLearnedSkillVersionsRequest, options?: RequestOptions): Promise<DiffLearnedSkillVersionsResponse>;
```

Parameters:

- `request` (`DiffLearnedSkillVersionsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<DiffLearnedSkillVersionsResponse>`

<Heading as="h4" id="api-learnedskills-get-methodsignature"><code>LearnedSkills.get</code></Heading>

Gets one learned skill.

```ts
get(request: GetLearnedSkillRequest, options?: RequestOptions): Promise<GetLearnedSkillResponse>;
```

Parameters:

- `request` (`GetLearnedSkillRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetLearnedSkillResponse>`

<Heading as="h4" id="api-learnedskills-list-methodsignature"><code>LearnedSkills.list</code></Heading>

Lists learned skills and their lifecycle state.

```ts
list(request: ListLearnedSkillsRequest, options?: RequestOptions): Promise<ListLearnedSkillsResponse>;
```

Parameters:

- `request` (`ListLearnedSkillsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListLearnedSkillsResponse>`

<Heading as="h4" id="api-learnedskills-listchanges-methodsignature"><code>LearnedSkills.listChanges</code></Heading>

Lists the recorded changes to learned skills.

```ts
listChanges(request: ListSkillChangesRequest, options?: RequestOptions): Promise<ListSkillChangesResponse>;
```

Parameters:

- `request` (`ListSkillChangesRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListSkillChangesResponse>`

<Heading as="h4" id="api-learnedskills-reject-methodsignature"><code>LearnedSkills.reject</code></Heading>

Rejects a learned skill.

```ts
reject(request: MutateLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;
```

Parameters:

- `request` (`MutateLearnedSkillRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearnedSkillResponse>`

<Heading as="h4" id="api-learnedskills-rollback-methodsignature"><code>LearnedSkills.rollback</code></Heading>

Rolls a learned skill back to an earlier version.

```ts
rollback(request: RollbackLearnedSkillRequest, options?: RequestOptions): Promise<MutateLearnedSkillResponse>;
```

Parameters:

- `request` (`RollbackLearnedSkillRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearnedSkillResponse>`

<Heading as="h3" id="api-learningattempts-interface"><code>LearningAttempts</code></Heading>

Learning-attempt inventory and server-owned lifecycle operations.

```ts
export interface LearningAttempts
```

Callable members: [`abandon()`](#api-learningattempts-abandon-methodsignature), [`get()`](#api-learningattempts-get-methodsignature), [`list()`](#api-learningattempts-list-methodsignature), [`retry()`](#api-learningattempts-retry-methodsignature)

<Heading as="h4" id="api-learningattempts-abandon-methodsignature"><code>LearningAttempts.abandon</code></Heading>

Abandons an eligible learning attempt.

```ts
abandon(request: MutateLearningAttemptRequest, options?: RequestOptions): Promise<MutateLearningAttemptResponse>;
```

Parameters:

- `request` (`MutateLearningAttemptRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearningAttemptResponse>`

<Heading as="h4" id="api-learningattempts-get-methodsignature"><code>LearningAttempts.get</code></Heading>

Gets one learning attempt.

```ts
get(request: GetLearningAttemptRequest, options?: RequestOptions): Promise<GetLearningAttemptResponse>;
```

Parameters:

- `request` (`GetLearningAttemptRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetLearningAttemptResponse>`

<Heading as="h4" id="api-learningattempts-list-methodsignature"><code>LearningAttempts.list</code></Heading>

Lists learning attempts visible to the caller.

```ts
list(request: ListLearningAttemptsRequest, options?: RequestOptions): Promise<ListLearningAttemptsResponse>;
```

Parameters:

- `request` (`ListLearningAttemptsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListLearningAttemptsResponse>`

<Heading as="h4" id="api-learningattempts-retry-methodsignature"><code>LearningAttempts.retry</code></Heading>

Retries a failed learning attempt.

```ts
retry(request: MutateLearningAttemptRequest, options?: RequestOptions): Promise<MutateLearningAttemptResponse>;
```

Parameters:

- `request` (`MutateLearningAttemptRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<MutateLearningAttemptResponse>`

<Heading as="h3" id="api-learningproposals-interface"><code>LearningProposals</code></Heading>

Learning-proposal inventory and server-owned decision operations.

```ts
export interface LearningProposals
```

Callable members: [`decide()`](#api-learningproposals-decide-methodsignature), [`get()`](#api-learningproposals-get-methodsignature), [`list()`](#api-learningproposals-list-methodsignature), [`undoPromotion()`](#api-learningproposals-undopromotion-methodsignature)

<Heading as="h4" id="api-learningproposals-decide-methodsignature"><code>LearningProposals.decide</code></Heading>

Approves or rejects a staged learning proposal.

```ts
decide(request: DecideLearningProposalRequest, options?: RequestOptions): Promise<DecideLearningProposalResponse>;
```

Parameters:

- `request` (`DecideLearningProposalRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<DecideLearningProposalResponse>`

<Heading as="h4" id="api-learningproposals-get-methodsignature"><code>LearningProposals.get</code></Heading>

Gets one staged learning proposal.

```ts
get(request: GetLearningProposalRequest, options?: RequestOptions): Promise<GetLearningProposalResponse>;
```

Parameters:

- `request` (`GetLearningProposalRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetLearningProposalResponse>`

<Heading as="h4" id="api-learningproposals-list-methodsignature"><code>LearningProposals.list</code></Heading>

Lists staged learning proposals.

```ts
list(request: ListLearningProposalsRequest, options?: RequestOptions): Promise<ListLearningProposalsResponse>;
```

Parameters:

- `request` (`ListLearningProposalsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListLearningProposalsResponse>`

<Heading as="h4" id="api-learningproposals-undopromotion-methodsignature"><code>LearningProposals.undoPromotion</code></Heading>

Reverts an eligible learning promotion.

```ts
undoPromotion(request: UndoLearningPromotionRequest, options?: RequestOptions): Promise<UndoLearningPromotionResponse>;
```

Parameters:

- `request` (`UndoLearningPromotionRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<UndoLearningPromotionResponse>`

<Heading as="h3" id="api-manualdreamcapabilities-interface"><code>ManualDreamCapabilities</code></Heading>

Manual dream support for deployment-owned targets.

```ts
export interface ManualDreamCapabilities
```

<Heading as="h4" id="api-manualdreamcapabilities-projectmemory-propertysignature"><code>ManualDreamCapabilities.projectMemory</code></Heading>

```ts
readonly projectMemory?: DreamTargetCapability;
```

<Heading as="h4" id="api-manualdreamcapabilities-usermodel-propertysignature"><code>ManualDreamCapabilities.userModel</code></Heading>

```ts
readonly userModel?: DreamTargetCapability;
```

<Heading as="h3" id="api-mcpauthorization-interface"><code>McpAuthorization</code></Heading>

A reusable session-bound correlation handle for one server-owned authorization. Construction stores exact correlation only. It performs no I/O and makes no state or authority claim. The handle does not persist credentials or lifecycle truth. Every presentation lookup and control request receives automatic session affinity.

```ts
export interface McpAuthorization
```

Callable members: [`cancel()`](#api-mcpauthorization-cancel-methodsignature), [`presentation()`](#api-mcpauthorization-presentation-methodsignature), [`recheck()`](#api-mcpauthorization-recheck-methodsignature)

<Heading as="h4" id="api-mcpauthorization-authorizationid-propertysignature"><code>McpAuthorization.authorizationId</code></Heading>

```ts
readonly authorizationId: string;
```

<Heading as="h4" id="api-mcpauthorization-cancel-methodsignature"><code>McpAuthorization.cancel</code></Heading>

Creates one lazy authorization cancellation flow.

```ts
cancel(options?: McpAuthorizationFlowOptions, requestOptions?: RequestOptions): McpAuthorizationFlow;
```

Parameters:

- `options` (`McpAuthorizationFlowOptions`, optional): Permission handling for a possible continuation.
- `requestOptions` (`RequestOptions`, optional): Options used only when this flow starts.

Returns: `McpAuthorizationFlow`: A distinct, transport-lazy, single-consumption flow.

<Heading as="h4" id="api-mcpauthorization-presentation-methodsignature"><code>McpAuthorization.presentation</code></Heading>

Reads the live presentation URL for this authorization. The application owns display and browser policy. The SDK validates and returns the HTTP(S) string without opening, copying, caching, rendering, or persisting it.

```ts
presentation(requestOptions?: RequestOptions): Promise<string>;
```

Parameters:

- `requestOptions` (`RequestOptions`, optional): Request headers, callbacks, cancellation signal, and deadline.

Returns: `Promise<string>`: The server's current absolute HTTP(S) presentation URL.

Throws: `ProtocolError` when the response has no valid absolute HTTP(S) URL.

<Heading as="h4" id="api-mcpauthorization-recheck-methodsignature"><code>McpAuthorization.recheck</code></Heading>

Creates one lazy authorization recheck flow.

```ts
recheck(options?: McpAuthorizationFlowOptions, requestOptions?: RequestOptions): McpAuthorizationFlow;
```

Parameters:

- `options` (`McpAuthorizationFlowOptions`, optional): Permission handling for a possible continuation.
- `requestOptions` (`RequestOptions`, optional): Options used only when this flow starts.

Returns: `McpAuthorizationFlow`: A distinct, transport-lazy, single-consumption flow.

<Heading as="h4" id="api-mcpauthorization-sessionid-propertysignature"><code>McpAuthorization.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h3" id="api-mcpauthorizationflow-interface"><code>McpAuthorizationFlow</code></Heading>

One lazy, single-consumption authorization control and optional continuation. Calling `recheck()` or `cancel()` creates this flow without I/O. The first iterator `next()` or `result()` performs the one control request with exact session affinity. Iteration and `result()` are mutually exclusive. Request cancellation releases SDK-owned resources but does not determine whether the server committed the control. The SDK does not poll, retry, reconnect, or scan durable activity automatically.

```ts
export interface McpAuthorizationFlow extends AsyncIterable<Event>
```

Callable members: [`cancelContinuation()`](#api-mcpauthorizationflow-cancelcontinuation-methodsignature), [`resolveAsk()`](#api-mcpauthorizationflow-resolveask-methodsignature), [`result()`](#api-mcpauthorizationflow-result-methodsignature)

<Heading as="h4" id="api-mcpauthorizationflow-authorizationid-propertysignature"><code>McpAuthorizationFlow.authorizationId</code></Heading>

```ts
readonly authorizationId: string;
```

<Heading as="h4" id="api-mcpauthorizationflow-cancelcontinuation-methodsignature"><code>McpAuthorizationFlow.cancelContinuation</code></Heading>

Requests cancellation of the exact continuation run already observed by this flow.

```ts
cancelContinuation(requestOptions?: RequestOptions): Promise<void>;
```

Parameters:

- `requestOptions` (`RequestOptions`, optional): Options used only for this cancellation mutation.

Returns: `Promise<void>`: A promise that resolves after the server accepts the request.

Throws: `InvalidStateError` before a continuation run is observed.

Throws: `UnsupportedFeatureError` when the server lacks `prompt_free_controls`.

<Heading as="h4" id="api-mcpauthorizationflow-continuationrunid-propertysignature"><code>McpAuthorizationFlow.continuationRunId</code></Heading>

```ts
readonly continuationRunId: string | undefined;
```

<Heading as="h4" id="api-mcpauthorizationflow-operation-propertysignature"><code>McpAuthorizationFlow.operation</code></Heading>

```ts
readonly operation: McpAuthorizationOperation;
```

<Heading as="h4" id="api-mcpauthorizationflow-resolveask-methodsignature"><code>McpAuthorizationFlow.resolveAsk</code></Heading>

Resolves one observed ordinary permission ask on the exact continuation run.

```ts
resolveAsk(askId: string, verdict: PermissionVerdict, requestOptions?: RequestOptions): Promise<void>;
```

Parameters:

- `askId` (`string`): ID of a pending ask already observed on this flow.
- `verdict` (`PermissionVerdict`): Application-owned permission decision to send unchanged.
- `requestOptions` (`RequestOptions`, optional): Options used only for this permission mutation.

Returns: `Promise<void>`: A promise that resolves after the server accepts the decision.

Throws: `InvalidStateError` when the ask is unknown, no longer pending, or plan-originated.

Throws: `UnsupportedFeatureError` when the server lacks `prompt_free_controls`.

<Heading as="h4" id="api-mcpauthorizationflow-result-methodsignature"><code>McpAuthorizationFlow.result</code></Heading>

Starts and drains this flow as its single consumption mode.

```ts
result(): Promise<McpAuthorizationResult>;
```

Returns: `Promise<McpAuthorizationResult>`: A pending, settled, completed, or chained-authorization result.

Throws: `InvalidStateError` when the flow is already being consumed.

Throws: `ProtocolError` when the server stream violates lifecycle correlation or grammar.

<Heading as="h4" id="api-mcpauthorizationflow-sessionid-propertysignature"><code>McpAuthorizationFlow.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h3" id="api-mcpauthorizationflowoptions-interface"><code>McpAuthorizationFlowOptions</code></Heading>

Application-owned permission behavior for one authorization continuation. These options never choose an authorization status or browser policy. Automatic permission replies use only `permissionRequestOptions`, independently of the flow request options.

```ts
export interface McpAuthorizationFlowOptions
```

<Heading as="h4" id="api-mcpauthorizationflowoptions-onpermissionask-propertysignature"><code>McpAuthorizationFlowOptions.onPermissionAsk</code></Heading>

Automatically answers only ordinary permission asks observed on the continuation.

```ts
onPermissionAsk?: PermissionAskResponder;
```

<Heading as="h4" id="api-mcpauthorizationflowoptions-permissionrequestoptions-propertysignature"><code>McpAuthorizationFlowOptions.permissionRequestOptions</code></Heading>

Request options used only for automatic permission replies.

```ts
permissionRequestOptions?: RequestOptions;
```

<Heading as="h3" id="api-mcpconnectorinventory-interface"><code>McpConnectorInventory</code></Heading>

A current, nonhistorical snapshot of broker connector publication.

```ts
export interface McpConnectorInventory
```

<Heading as="h4" id="api-mcpconnectorinventory-availability-propertysignature"><code>McpConnectorInventory.availability</code></Heading>

Whether the process-local broker snapshot is available.

```ts
readonly availability: McpConnectorAvailability;
```

<Heading as="h4" id="api-mcpconnectorinventory-connectors-propertysignature"><code>McpConnectorInventory.connectors</code></Heading>

Ordered bounded connector display rows.

```ts
readonly connectors: readonly McpConnectorStatus[];
```

<Heading as="h4" id="api-mcpconnectorinventory-enrollmentstate-propertysignature"><code>McpConnectorInventory.enrollmentState</code></Heading>

Aggregate whole-bundle enrollment state.

```ts
readonly enrollmentState: McpConnectorEnrollmentState;
```

<Heading as="h4" id="api-mcpconnectorinventory-totalconnectors-propertysignature"><code>McpConnectorInventory.totalConnectors</code></Heading>

Total connector count before server-side truncation.

```ts
readonly totalConnectors: number;
```

<Heading as="h4" id="api-mcpconnectorinventory-truncated-propertysignature"><code>McpConnectorInventory.truncated</code></Heading>

Whether the connector rows omit entries because of the server bound.

```ts
readonly truncated: boolean;
```

<Heading as="h3" id="api-mcpconnectorstatus-interface"><code>McpConnectorStatus</code></Heading>

A bounded connector display row. It is not a routing handle.

```ts
export interface McpConnectorStatus
```

<Heading as="h4" id="api-mcpconnectorstatus-cataloguestate-propertysignature"><code>McpConnectorStatus.catalogueState</code></Heading>

Broker-local catalogue publication state.

```ts
readonly catalogueState: McpConnectorCatalogueState;
```

<Heading as="h4" id="api-mcpconnectorstatus-name-propertysignature"><code>McpConnectorStatus.name</code></Heading>

Display name supplied by the server.

```ts
readonly name: string;
```

<Heading as="h4" id="api-mcpconnectorstatus-toolcount-propertysignature"><code>McpConnectorStatus.toolCount</code></Heading>

Number of published tools when the catalogue state makes that count meaningful.

```ts
readonly toolCount: number;
```

<Heading as="h3" id="api-mcpinventory-interface"><code>McpInventory</code></Heading>

MCP resource, prompt, source, and ToolHive-group inventory operations.

```ts
export interface McpInventory
```

Callable members: [`getPrompt()`](#api-mcpinventory-getprompt-methodsignature), [`listPrompts()`](#api-mcpinventory-listprompts-methodsignature), [`listResources()`](#api-mcpinventory-listresources-methodsignature), [`listSources()`](#api-mcpinventory-listsources-methodsignature), [`listToolHiveGroups()`](#api-mcpinventory-listtoolhivegroups-methodsignature), [`readResource()`](#api-mcpinventory-readresource-methodsignature)

<Heading as="h4" id="api-mcpinventory-getprompt-methodsignature"><code>McpInventory.getPrompt</code></Heading>

Expands one MCP prompt into its rendered messages.

```ts
getPrompt(request: GetMcpPromptRequest, options?: RequestOptions): Promise<GetMcpPromptResponse>;
```

Parameters:

- `request` (`GetMcpPromptRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetMcpPromptResponse>`

<Heading as="h4" id="api-mcpinventory-listprompts-methodsignature"><code>McpInventory.listPrompts</code></Heading>

Lists the MCP prompts exposed by configured servers.

```ts
listPrompts(request: ListMcpPromptsRequest, options?: RequestOptions): Promise<ListMcpPromptsResponse>;
```

Parameters:

- `request` (`ListMcpPromptsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListMcpPromptsResponse>`

<Heading as="h4" id="api-mcpinventory-listresources-methodsignature"><code>McpInventory.listResources</code></Heading>

Lists the MCP resources exposed by configured servers.

```ts
listResources(request: ListMcpResourcesRequest, options?: RequestOptions): Promise<ListMcpResourcesResponse>;
```

Parameters:

- `request` (`ListMcpResourcesRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListMcpResourcesResponse>`

<Heading as="h4" id="api-mcpinventory-listsources-methodsignature"><code>McpInventory.listSources</code></Heading>

Lists configured MCP sources and their diagnostics.

```ts
listSources(request: ListMcpSourcesRequest, options?: RequestOptions): Promise<ListMcpSourcesResponse>;
```

Parameters:

- `request` (`ListMcpSourcesRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListMcpSourcesResponse>`

<Heading as="h4" id="api-mcpinventory-listtoolhivegroups-methodsignature"><code>McpInventory.listToolHiveGroups</code></Heading>

Lists ToolHive groups present in the resolved MCP inventory.

```ts
listToolHiveGroups(request: ListToolHiveGroupsRequest, options?: RequestOptions): Promise<ListToolHiveGroupsResponse>;
```

Parameters:

- `request` (`ListToolHiveGroupsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListToolHiveGroupsResponse>`

<Heading as="h4" id="api-mcpinventory-readresource-methodsignature"><code>McpInventory.readResource</code></Heading>

Reads one MCP resource by URI.

```ts
readResource(request: ReadMcpResourceRequest, options?: RequestOptions): Promise<ReadMcpResourceResponse>;
```

Parameters:

- `request` (`ReadMcpResourceRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ReadMcpResourceResponse>`

<Heading as="h3" id="api-mecatlerroroptions-interface"><code>MecatlErrorOptions</code></Heading>

Metadata attached to one MecatlError.

```ts
export interface MecatlErrorOptions
```

<Heading as="h4" id="api-mecatlerroroptions-cause-propertysignature"><code>MecatlErrorOptions.cause</code></Heading>

Original failure retained on the JavaScript Error instance.

```ts
cause?: unknown;
```

<Heading as="h4" id="api-mecatlerroroptions-code-propertysignature"><code>MecatlErrorOptions.code</code></Heading>

Stable machine-readable SDK or server error code.

```ts
code: MecatlErrorCode;
```

<Heading as="h4" id="api-mecatlerroroptions-requestid-propertysignature"><code>MecatlErrorOptions.requestId</code></Heading>

Server request ID, when the transport supplied one.

```ts
requestId?: string | undefined;
```

<Heading as="h4" id="api-mecatlerroroptions-status-propertysignature"><code>MecatlErrorOptions.status</code></Heading>

HTTP status, when the failure came from the HTTP transport.

```ts
status?: number | undefined;
```

<Heading as="h4" id="api-mecatlerroroptions-transport-propertysignature"><code>MecatlErrorOptions.transport</code></Heading>

Transport that observed the failure, or `local` for SDK validation.

```ts
transport: ErrorOrigin;
```

<Heading as="h3" id="api-mediapartoptions-interface"><code>MediaPartOptions</code></Heading>

Options accepted by imagePart() and audioPart().

```ts
export interface MediaPartOptions extends MediaPartSource
```

<Heading as="h4" id="api-mediapartoptions-mimetype-propertysignature"><code>MediaPartOptions.mimeType</code></Heading>

Media type beginning with `image/` or `audio/` for the selected helper.

```ts
mimeType: string;
```

<Heading as="h3" id="api-mediapartsource-interface"><code>MediaPartSource</code></Heading>

The source accepted by imagePart() and audioPart(). Exactly one field is required.

```ts
export interface MediaPartSource
```

<Heading as="h4" id="api-mediapartsource-bytes-propertysignature"><code>MediaPartSource.bytes</code></Heading>

Inline media bytes.

```ts
bytes?: Uint8Array;
```

<Heading as="h4" id="api-mediapartsource-url-propertysignature"><code>MediaPartSource.url</code></Heading>

Absolute HTTPS media URL.

```ts
url?: string;
```

<Heading as="h3" id="api-modelretryeventpayload-interface"><code>ModelRetryEventPayload</code></Heading>

The payload of a `model.retry` event.

```ts
export interface ModelRetryEventPayload
```

<Heading as="h4" id="api-modelretryeventpayload-retrydisposition-propertysignature"><code>ModelRetryEventPayload.retryDisposition</code></Heading>

```ts
readonly retryDisposition: RetryDisposition;
```

<Heading as="h4" id="api-modelretryeventpayload-streamprogress-propertysignature"><code>ModelRetryEventPayload.streamProgress</code></Heading>

```ts
readonly streamProgress: StreamProgress;
```

<Heading as="h3" id="api-models-interface"><code>Models</code></Heading>

Selectable model inventory operations.

```ts
export interface Models
```

Callable members: [`list()`](#api-models-list-methodsignature)

<Heading as="h4" id="api-models-list-methodsignature"><code>Models.list</code></Heading>

Lists selectable providers and models.

```ts
list(request: ListModelsRequest, options?: RequestOptions): Promise<ListModelsResponse>;
```

Parameters:

- `request` (`ListModelsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListModelsResponse>`

<Heading as="h3" id="api-paralleleventpayload-interface"><code>ParallelEventPayload</code></Heading>

The payload shared by `parallel.*` events.

```ts
export interface ParallelEventPayload
```

<Heading as="h4" id="api-paralleleventpayload-branchcount-propertysignature"><code>ParallelEventPayload.branchCount</code></Heading>

```ts
readonly branchCount: number;
```

<Heading as="h4" id="api-paralleleventpayload-branchindex-propertysignature"><code>ParallelEventPayload.branchIndex</code></Heading>

```ts
readonly branchIndex: number;
```

<Heading as="h4" id="api-paralleleventpayload-branchlabel-propertysignature"><code>ParallelEventPayload.branchLabel</code></Heading>

```ts
readonly branchLabel: string;
```

<Heading as="h4" id="api-paralleleventpayload-childid-propertysignature"><code>ParallelEventPayload.childId</code></Heading>

```ts
readonly childId: string;
```

<Heading as="h4" id="api-paralleleventpayload-detail-propertysignature"><code>ParallelEventPayload.detail</code></Heading>

```ts
readonly detail: string;
```

<Heading as="h4" id="api-paralleleventpayload-durationms-propertysignature"><code>ParallelEventPayload.durationMs</code></Heading>

```ts
readonly durationMs: bigint;
```

<Heading as="h4" id="api-paralleleventpayload-failed-propertysignature"><code>ParallelEventPayload.failed</code></Heading>

```ts
readonly failed: boolean;
```

<Heading as="h4" id="api-paralleleventpayload-goal-propertysignature"><code>ParallelEventPayload.goal</code></Heading>

```ts
readonly goal: string;
```

<Heading as="h4" id="api-paralleleventpayload-innerkind-propertysignature"><code>ParallelEventPayload.innerKind</code></Heading>

```ts
readonly innerKind: string;
```

<Heading as="h4" id="api-paralleleventpayload-iserror-propertysignature"><code>ParallelEventPayload.isError</code></Heading>

```ts
readonly isError: boolean;
```

<Heading as="h4" id="api-paralleleventpayload-join-propertysignature"><code>ParallelEventPayload.join</code></Heading>

```ts
readonly join: string;
```

<Heading as="h4" id="api-paralleleventpayload-kind-propertysignature"><code>ParallelEventPayload.kind</code></Heading>

```ts
readonly kind: string;
```

<Heading as="h4" id="api-paralleleventpayload-model-propertysignature"><code>ParallelEventPayload.model</code></Heading>

```ts
readonly model: string;
```

<Heading as="h4" id="api-paralleleventpayload-parentcallid-propertysignature"><code>ParallelEventPayload.parentCallId</code></Heading>

```ts
readonly parentCallId: string;
```

<Heading as="h4" id="api-paralleleventpayload-routedcategory-propertysignature"><code>ParallelEventPayload.routedCategory</code></Heading>

```ts
readonly routedCategory: string;
```

<Heading as="h4" id="api-paralleleventpayload-routedmodel-propertysignature"><code>ParallelEventPayload.routedModel</code></Heading>

```ts
readonly routedModel: string;
```

<Heading as="h4" id="api-paralleleventpayload-routingreason-propertysignature"><code>ParallelEventPayload.routingReason</code></Heading>

```ts
readonly routingReason: string;
```

<Heading as="h4" id="api-paralleleventpayload-stop-propertysignature"><code>ParallelEventPayload.stop</code></Heading>

```ts
readonly stop: string;
```

<Heading as="h4" id="api-paralleleventpayload-text-propertysignature"><code>ParallelEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-paralleleventpayload-toolcount-propertysignature"><code>ParallelEventPayload.toolCount</code></Heading>

```ts
readonly toolCount: number;
```

<Heading as="h4" id="api-paralleleventpayload-toolname-propertysignature"><code>ParallelEventPayload.toolName</code></Heading>

```ts
readonly toolName: string;
```

<Heading as="h4" id="api-paralleleventpayload-usage-propertysignature"><code>ParallelEventPayload.usage</code></Heading>

```ts
readonly usage?: EventUsage | undefined;
```

<Heading as="h4" id="api-paralleleventpayload-winner-propertysignature"><code>ParallelEventPayload.winner</code></Heading>

```ts
readonly winner: number;
```

<Heading as="h4" id="api-paralleleventpayload-winnerworkspace-propertysignature"><code>ParallelEventPayload.winnerWorkspace</code></Heading>

```ts
readonly winnerWorkspace: string;
```

<Heading as="h4" id="api-paralleleventpayload-workspace-propertysignature"><code>ParallelEventPayload.workspace</code></Heading>

```ts
readonly workspace: string;
```

<Heading as="h3" id="api-permissionaskeventpayload-interface"><code>PermissionAskEventPayload</code></Heading>

The payload shared by `permission.ask` and `permission.retract`.

```ts
export interface PermissionAskEventPayload
```

<Heading as="h4" id="api-permissionaskeventpayload-args-propertysignature"><code>PermissionAskEventPayload.args</code></Heading>

```ts
readonly args: string;
```

<Heading as="h4" id="api-permissionaskeventpayload-askid-propertysignature"><code>PermissionAskEventPayload.askId</code></Heading>

```ts
readonly askId: string;
```

<Heading as="h4" id="api-permissionaskeventpayload-reason-propertysignature"><code>PermissionAskEventPayload.reason</code></Heading>

```ts
readonly reason: string;
```

<Heading as="h4" id="api-permissionaskeventpayload-tool-propertysignature"><code>PermissionAskEventPayload.tool</code></Heading>

```ts
readonly tool: string;
```

<Heading as="h3" id="api-planresolution-interface"><code>PlanResolution</code></Heading>

One atomic, single-consumption resolution of a durably parked plan.

```ts
export interface PlanResolution extends AsyncIterable<Event>
```

Callable members: [`result()`](#api-planresolution-result-methodsignature)

<Heading as="h4" id="api-planresolution-result-methodsignature"><code>PlanResolution.result</code></Heading>

Drains the merged stream and returns the resumed and optional continuation outcomes.

```ts
result(): Promise<PlanResolutionResult>;
```

Returns: `Promise<PlanResolutionResult>`: The resumed run and any continuation run started by approval.

Throws: `InvalidStateError` when the resolution is already being consumed.

Throws: `PlanContinuationStartError` when an approved continuation cannot start.

<Heading as="h3" id="api-planresolutionresult-interface"><code>PlanResolutionResult</code></Heading>

The two ordered outcomes carried by one atomic plan-resolution stream.

```ts
export interface PlanResolutionResult
```

<Heading as="h4" id="api-planresolutionresult-continuation-propertysignature"><code>PlanResolutionResult.continuation</code></Heading>

```ts
readonly continuation?: RunResult;
```

<Heading as="h4" id="api-planresolutionresult-resumed-propertysignature"><code>PlanResolutionResult.resumed</code></Heading>

```ts
readonly resumed: RunResult;
```

<Heading as="h3" id="api-rawclient-interface"><code>RawClient</code></Heading>

Transport-neutral, descriptor-driven operations beneath Client/Session/Run.

```ts
export interface RawClient
```

Callable members: [`features()`](#api-rawclient-features-methodsignature), [`stream()`](#api-rawclient-stream-methodsignature), [`unary()`](#api-rawclient-unary-methodsignature)

<Heading as="h4" id="api-rawclient-features-methodsignature"><code>RawClient.features</code></Heading>

Returns the build features learned from the shared compatibility probe.

```ts
features(options?: CallOptions): Promise<ReadonlySet<string>>;
```

Parameters:

- `options` (`CallOptions`, optional)

Returns: `Promise<ReadonlySet<string>>`

<Heading as="h4" id="api-rawclient-stream-methodsignature"><code>RawClient.stream</code></Heading>

Invokes one streaming RPC after enforcing the SDK compatibility floor.

```ts
stream<I extends DescMessage, O extends DescMessage>(method: DescMethodStreaming<I, O>, input: AsyncIterable<MessageInitShape<I>>, options?: CallOptions): AsyncIterable<MessageShape<O>>;
```

Parameters:

- `method` (`DescMethodStreaming<I, O>`)
- `input` (`AsyncIterable<MessageInitShape<I>>`)
- `options` (`CallOptions`, optional)

Returns: `AsyncIterable<MessageShape<O>>`

<Heading as="h4" id="api-rawclient-unary-methodsignature"><code>RawClient.unary</code></Heading>

Invokes one unary RPC after enforcing the SDK compatibility floor.

```ts
unary<I extends DescMessage, O extends DescMessage>(method: DescMethodUnary<I, O>, input: MessageInitShape<I>, options?: CallOptions): Promise<MessageShape<O>>;
```

Parameters:

- `method` (`DescMethodUnary<I, O>`)
- `input` (`MessageInitShape<I>`)
- `options` (`CallOptions`, optional)

Returns: `Promise<MessageShape<O>>`

<Heading as="h3" id="api-rawclientoptions-interface"><code>RawClientOptions</code></Heading>

Options for constructing the transport-neutral raw client.

```ts
export interface RawClientOptions
```

<Heading as="h4" id="api-rawclientoptions-transport-propertysignature"><code>RawClientOptions.transport</code></Heading>

A caller-owned Connect-ES transport.

```ts
transport: Transport;
```

<Heading as="h4" id="api-rawclientoptions-transportkind-propertysignature"><code>RawClientOptions.transportKind</code></Heading>

Required only for an unregistered injected transport. Defaults to gRPC.

```ts
transportKind?: TransportKind;
```

<Heading as="h3" id="api-reflection-interface"><code>Reflection</code></Heading>

Session-reflection operations.

```ts
export interface Reflection
```

Callable members: [`reflect()`](#api-reflection-reflect-methodsignature)

<Heading as="h4" id="api-reflection-reflect-methodsignature"><code>Reflection.reflect</code></Heading>

Reflects one completed session into learning evidence.

```ts
reflect(request: ReflectSessionRequest, options?: RequestOptions): Promise<ReflectSessionResponse>;
```

Parameters:

- `request` (`ReflectSessionRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ReflectSessionResponse>`

<Heading as="h3" id="api-resulteventpayload-interface"><code>ResultEventPayload</code></Heading>

The payload of a terminal `result` event.

```ts
export interface ResultEventPayload
```

<Heading as="h4" id="api-resulteventpayload-error-propertysignature"><code>ResultEventPayload.error</code></Heading>

```ts
readonly error: string;
```

<Heading as="h4" id="api-resulteventpayload-permanent-propertysignature"><code>ResultEventPayload.permanent</code></Heading>

```ts
readonly permanent: boolean;
```

<Heading as="h4" id="api-resulteventpayload-retrydisposition-propertysignature"><code>ResultEventPayload.retryDisposition</code></Heading>

```ts
readonly retryDisposition?: RetryDisposition | undefined;
```

<Heading as="h4" id="api-resulteventpayload-stop-propertysignature"><code>ResultEventPayload.stop</code></Heading>

```ts
readonly stop: string;
```

<Heading as="h4" id="api-resulteventpayload-streamprogress-propertysignature"><code>ResultEventPayload.streamProgress</code></Heading>

```ts
readonly streamProgress?: StreamProgress | undefined;
```

<Heading as="h4" id="api-resulteventpayload-text-propertysignature"><code>ResultEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-resulteventpayload-usage-propertysignature"><code>ResultEventPayload.usage</code></Heading>

```ts
readonly usage?: EventUsage | undefined;
```

<Heading as="h3" id="api-run-interface"><code>Run</code></Heading>

One accepted server run and its single-consumption event stream.

```ts
export interface Run extends AsyncIterable<Event>
```

Callable members: [`approve()`](#api-run-approve-methodsignature), [`cancel()`](#api-run-cancel-methodsignature), [`outcome()`](#api-run-outcome-methodsignature), [`resolveAsk()`](#api-run-resolveask-methodsignature), [`result()`](#api-run-result-methodsignature), [`steer()`](#api-run-steer-methodsignature)

<Heading as="h4" id="api-run-approve-methodsignature"><code>Run.approve</code></Heading>

Sends a Boolean permission verdict for a `permission.ask` event.

```ts
approve(askId: string, allow: boolean): Promise<void>;
```

Parameters:

- `askId` (`string`): ID carried by the permission ask.
- `allow` (`boolean`): Whether to allow the call once.

Returns: `Promise<void>`: A promise that resolves after the verdict is sent.

Throws: `PermissionAskAlreadyResolvedError` when the ask is no longer pending.

<Heading as="h4" id="api-run-cancel-methodsignature"><code>Run.cancel</code></Heading>

Requests cancellation; consume the run normally to receive the cancelled outcome.

```ts
cancel(): Promise<void>;
```

Returns: `Promise<void>`: A promise that resolves after the cancellation request is sent.

<Heading as="h4" id="api-run-id-propertysignature"><code>Run.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-run-outcome-methodsignature"><code>Run.outcome</code></Heading>

Drains all remaining events and returns either completion or an authorization handoff.

```ts
outcome(): Promise<RunOutcome>;
```

Returns: `Promise<RunOutcome>`: The normal terminal outcome for this run.

Throws: `InvalidStateError` when the run is already being consumed.

<Heading as="h4" id="api-run-resolveask-methodsignature"><code>Run.resolveAsk</code></Heading>

Resolves one pending ask on this run with the server's string verdict vocabulary.

```ts
resolveAsk(askId: string, verdict: PermissionVerdict): Promise<void>;
```

Parameters:

- `askId` (`string`): ID carried by the permission ask.
- `verdict` (`PermissionVerdict`): Decision to apply to the pending ask.

Returns: `Promise<void>`: A promise that resolves after the server accepts the verdict.

Throws: `PermissionAskAlreadyResolvedError` when the ask is no longer pending.

Throws: `InvalidStateError` when used for a plan-approval ask.

<Heading as="h4" id="api-run-result-methodsignature"><code>Run.result</code></Heading>

Drains all remaining events and returns the completed terminal result.

```ts
result(): Promise<RunResult>;
```

Returns: `Promise<RunResult>`: The terminal result for this run.

Throws: `InvalidStateError` when the run is already being consumed.

Throws: `RunAuthorizationRequiredError` when the run parks on external authorization.

<Heading as="h4" id="api-run-sessionid-propertysignature"><code>Run.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h4" id="api-run-steer-methodsignature"><code>Run.steer</code></Heading>

Strictly steers this run. A late steer is refused and is never promoted.

```ts
steer(text: string): Promise<void>;
```

Parameters:

- `text` (`string`): Instruction to apply to the active run.

Returns: `Promise<void>`: A promise that resolves after the steering request is sent.

<Heading as="h3" id="api-runauthorizationrequiredoutcome-interface"><code>RunAuthorizationRequiredOutcome</code></Heading>

A run that handed off one pending external authorization. This detached value carries correlation only. The server retains lifecycle ownership.

```ts
export interface RunAuthorizationRequiredOutcome
```

<Heading as="h4" id="api-runauthorizationrequiredoutcome-authorization-propertysignature"><code>RunAuthorizationRequiredOutcome.authorization</code></Heading>

```ts
readonly authorization: EventOf<"authorization.required">;
```

<Heading as="h4" id="api-runauthorizationrequiredoutcome-outcome-propertysignature"><code>RunAuthorizationRequiredOutcome.outcome</code></Heading>

```ts
readonly outcome: "authorization_required";
```

<Heading as="h4" id="api-runauthorizationrequiredoutcome-runid-propertysignature"><code>RunAuthorizationRequiredOutcome.runId</code></Heading>

```ts
readonly runId: string;
```

<Heading as="h4" id="api-runauthorizationrequiredoutcome-sessionid-propertysignature"><code>RunAuthorizationRequiredOutcome.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h3" id="api-runcompletedoutcome-interface"><code>RunCompletedOutcome</code></Heading>

A normally completed run outcome returned by `Run.outcome()`.

```ts
export interface RunCompletedOutcome
```

<Heading as="h4" id="api-runcompletedoutcome-outcome-propertysignature"><code>RunCompletedOutcome.outcome</code></Heading>

```ts
readonly outcome: "completed";
```

<Heading as="h4" id="api-runcompletedoutcome-result-propertysignature"><code>RunCompletedOutcome.result</code></Heading>

```ts
readonly result: RunResult;
```

<Heading as="h3" id="api-runcontrols-interface"><code>RunControls</code></Heading>

Prompt-free controls bound to one exact session run. Construct this resource with `Session.controls`. It does not attach, subscribe, or keep a run alive. Every method requires the server's `prompt_free_controls` feature, addresses `runId` exactly, performs one unary request without automatic retry, and accepts ordinary `RequestOptions`. A server that lacks the feature raises `UnsupportedFeatureError` before a control RPC is sent. Ended, cancelling, replaced, or otherwise stale runs fail with the server's typed `stale_run_control` error. A transport failure, caller cancellation, or deadline after dispatch can reject the promise after the server accepted the operation. Reconcile that ambiguous case from the authoritative session or activity state before deciding whether to retry.

```ts
export interface RunControls
```

Callable members: [`cancel()`](#api-runcontrols-cancel-methodsignature), [`cancelSteer()`](#api-runcontrols-cancelsteer-methodsignature), [`resolveAsk()`](#api-runcontrols-resolveask-methodsignature), [`steer()`](#api-runcontrols-steer-methodsignature)

<Heading as="h4" id="api-runcontrols-cancel-methodsignature"><code>RunControls.cancel</code></Heading>

Requests cancellation of this exact live run.

```ts
cancel(requestOptions?: RequestOptions): Promise<void>;
```

Parameters:

- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<void>`

<Heading as="h4" id="api-runcontrols-cancelsteer-methodsignature"><code>RunControls.cancelSteer</code></Heading>

Retracts this exact live run's pending steer bundle.

```ts
cancelSteer(options?: RunSteerOptions, requestOptions?: RequestOptions): Promise<RunSteerCancellationAcknowledgement>;
```

Parameters:

- `options` (`RunSteerOptions`, optional): Optional message correlation for this retraction request.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<RunSteerCancellationAcknowledgement>`: Whether the server retracted a bundle or found none pending.

<Heading as="h4" id="api-runcontrols-resolveask-methodsignature"><code>RunControls.resolveAsk</code></Heading>

Resolves one ordinary permission ask on this exact run. Root and surfaced-child permission asks are supported, including an ordinary ask restored from a persisted awaiting run. Plan-originated asks require `Session.resolvePlan()` and fail with `plan_resolution_required`. Unknown or already resolved asks fail with `ask_not_pending`.

```ts
resolveAsk(askId: string, verdict: PermissionVerdict, requestOptions?: RequestOptions): Promise<void>;
```

Parameters:

- `askId` (`string`): Exact permission ask ID.
- `verdict` (`PermissionVerdict`): Ordinary permission verdict to apply.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<void>`

<Heading as="h4" id="api-runcontrols-runid-propertysignature"><code>RunControls.runId</code></Heading>

Exact durable run addressed by every operation.

```ts
readonly runId: string;
```

<Heading as="h4" id="api-runcontrols-sessionid-propertysignature"><code>RunControls.sessionId</code></Heading>

Session that owns the addressed run.

```ts
readonly sessionId: string;
```

<Heading as="h4" id="api-runcontrols-steer-methodsignature"><code>RunControls.steer</code></Heading>

Injects text or ordered media into this exact live run. Structured prompt text fragments are joined with a newline, and media parts retain their order relative to other media. An empty prompt is rejected locally. A late steer fails as stale and never creates a successor run.

```ts
steer(prompt: PromptInput, options?: RunSteerOptions, requestOptions?: RequestOptions): Promise<RunSteerAcknowledgement>;
```

Parameters:

- `prompt` (`PromptInput`): Text, image, audio, or a structured prompt to inject.
- `options` (`RunSteerOptions`, optional): Optional message correlation.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<RunSteerAcknowledgement>`: The server's accepted-or-appended acknowledgement.

<Heading as="h3" id="api-runoptions-interface"><code>RunOptions</code></Heading>

Options applied to one run.

```ts
export interface RunOptions
```

<Heading as="h4" id="api-runoptions-onpermissionask-propertysignature"><code>RunOptions.onPermissionAsk</code></Heading>

Automatically answers ordinary permission asks.

```ts
onPermissionAsk?: PermissionAskResponder;
```

<Heading as="h4" id="api-runoptions-onplanapproval-propertysignature"><code>RunOptions.onPlanApproval</code></Heading>

Automatically answers only plan-originated PresentPlan asks.

```ts
onPlanApproval?: PlanApprovalResponder;
```

<Heading as="h3" id="api-runresult-interface"><code>RunResult</code></Heading>

The terminal outcome of a consumed run. Server-declared stops are values, not errors.

```ts
export interface RunResult
```

<Heading as="h4" id="api-runresult-content-propertysignature"><code>RunResult.content</code></Heading>

```ts
readonly content: string;
```

<Heading as="h4" id="api-runresult-rawevent-propertysignature"><code>RunResult.rawEvent</code></Heading>

The terminal event from the same discriminated union exposed by iteration.

```ts
readonly rawEvent: EventOf<"result">;
```

<Heading as="h4" id="api-runresult-runid-propertysignature"><code>RunResult.runId</code></Heading>

```ts
readonly runId: string;
```

<Heading as="h4" id="api-runresult-sessionid-propertysignature"><code>RunResult.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h4" id="api-runresult-stopreason-propertysignature"><code>RunResult.stopReason</code></Heading>

```ts
readonly stopReason: string;
```

<Heading as="h4" id="api-runresult-text-propertysignature"><code>RunResult.text</code></Heading>

Final text, mirrored as `content` for content-oriented consumers.

```ts
readonly text: string;
```

<Heading as="h4" id="api-runresult-usage-propertysignature"><code>RunResult.usage</code></Heading>

```ts
readonly usage: EventUsage | undefined;
```

<Heading as="h3" id="api-runsteeracknowledgement-interface"><code>RunSteerAcknowledgement</code></Heading>

The authoritative acknowledgement for a strict steer request. `accepted` means the steer created a pending bundle. `appended` means the steer was merged into the bundle that was already pending. The run and message IDs echo the addressed run and the request correlation.

```ts
export interface RunSteerAcknowledgement
```

<Heading as="h4" id="api-runsteeracknowledgement-messageid-propertysignature"><code>RunSteerAcknowledgement.messageId</code></Heading>

Request correlation ID, or an empty string when none was supplied.

```ts
readonly messageId: string;
```

<Heading as="h4" id="api-runsteeracknowledgement-outcome-propertysignature"><code>RunSteerAcknowledgement.outcome</code></Heading>

Whether the steer created or joined the pending bundle.

```ts
readonly outcome: "accepted" | "appended";
```

<Heading as="h4" id="api-runsteeracknowledgement-runid-propertysignature"><code>RunSteerAcknowledgement.runId</code></Heading>

Exact run ID addressed by the request.

```ts
readonly runId: string;
```

<Heading as="h3" id="api-runsteercancellationacknowledgement-interface"><code>RunSteerCancellationAcknowledgement</code></Heading>

The authoritative acknowledgement for strict steer retraction. `retracted` means the pending bundle was removed. `none_pending` means the exact live run had no pending bundle at the transition point. The run and message IDs echo the addressed run and the request correlation.

```ts
export interface RunSteerCancellationAcknowledgement
```

<Heading as="h4" id="api-runsteercancellationacknowledgement-messageid-propertysignature"><code>RunSteerCancellationAcknowledgement.messageId</code></Heading>

Request correlation ID, or an empty string when none was supplied.

```ts
readonly messageId: string;
```

<Heading as="h4" id="api-runsteercancellationacknowledgement-outcome-propertysignature"><code>RunSteerCancellationAcknowledgement.outcome</code></Heading>

Whether a pending steer bundle was removed.

```ts
readonly outcome: "retracted" | "none_pending";
```

<Heading as="h4" id="api-runsteercancellationacknowledgement-runid-propertysignature"><code>RunSteerCancellationAcknowledgement.runId</code></Heading>

Exact run ID addressed by the request.

```ts
readonly runId: string;
```

<Heading as="h3" id="api-runsteeroptions-interface"><code>RunSteerOptions</code></Heading>

Optional application correlation for a strict steer or retraction request. The server accepts at most 64 Unicode code points and echoes the supplied ID in the operation's acknowledgement. Omission sends an empty correlation ID.

```ts
export interface RunSteerOptions
```

<Heading as="h4" id="api-runsteeroptions-messageid-propertysignature"><code>RunSteerOptions.messageId</code></Heading>

Client-authored correlation ID echoed by the server.

```ts
messageId?: string;
```

<Heading as="h3" id="api-scheduleeventpayload-interface"><code>ScheduleEventPayload</code></Heading>

The payload shared by `schedule.*` events.

```ts
export interface ScheduleEventPayload
```

<Heading as="h4" id="api-scheduleeventpayload-err-propertysignature"><code>ScheduleEventPayload.err</code></Heading>

```ts
readonly err: string;
```

<Heading as="h4" id="api-scheduleeventpayload-fireid-propertysignature"><code>ScheduleEventPayload.fireId</code></Heading>

```ts
readonly fireId: string;
```

<Heading as="h4" id="api-scheduleeventpayload-kind-propertysignature"><code>ScheduleEventPayload.kind</code></Heading>

```ts
readonly kind: string;
```

<Heading as="h4" id="api-scheduleeventpayload-schedulename-propertysignature"><code>ScheduleEventPayload.scheduleName</code></Heading>

```ts
readonly scheduleName: string;
```

<Heading as="h4" id="api-scheduleeventpayload-sessionid-propertysignature"><code>ScheduleEventPayload.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h4" id="api-scheduleeventpayload-stop-propertysignature"><code>ScheduleEventPayload.stop</code></Heading>

```ts
readonly stop: string;
```

<Heading as="h3" id="api-schedules-interface"><code>Schedules</code></Heading>

Schedule and fire inventory plus server-owned lifecycle operations.

```ts
export interface Schedules
```

Callable members: [`create()`](#api-schedules-create-methodsignature), [`delete()`](#api-schedules-delete-methodsignature), [`fireNow()`](#api-schedules-firenow-methodsignature), [`get()`](#api-schedules-get-methodsignature), [`getFire()`](#api-schedules-getfire-methodsignature), [`list()`](#api-schedules-list-methodsignature), [`listFires()`](#api-schedules-listfires-methodsignature), [`pause()`](#api-schedules-pause-methodsignature), [`resume()`](#api-schedules-resume-methodsignature), [`update()`](#api-schedules-update-methodsignature)

<Heading as="h4" id="api-schedules-create-methodsignature"><code>Schedules.create</code></Heading>

Creates a recurring schedule.

```ts
create(request: CreateScheduleRequest, options?: RequestOptions): Promise<CreateScheduleResponse>;
```

Parameters:

- `request` (`CreateScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<CreateScheduleResponse>`

<Heading as="h4" id="api-schedules-delete-methodsignature"><code>Schedules.delete</code></Heading>

Deletes one schedule.

```ts
delete(request: DeleteScheduleRequest, options?: RequestOptions): Promise<DeleteScheduleResponse>;
```

Parameters:

- `request` (`DeleteScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<DeleteScheduleResponse>`

<Heading as="h4" id="api-schedules-firenow-methodsignature"><code>Schedules.fireNow</code></Heading>

Requests an immediate schedule fire.

```ts
fireNow(request: FireNowRequest, options?: RequestOptions): Promise<FireNowResponse>;
```

Parameters:

- `request` (`FireNowRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<FireNowResponse>`

<Heading as="h4" id="api-schedules-get-methodsignature"><code>Schedules.get</code></Heading>

Gets one schedule.

```ts
get(request: GetScheduleRequest, options?: RequestOptions): Promise<GetScheduleResponse>;
```

Parameters:

- `request` (`GetScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetScheduleResponse>`

<Heading as="h4" id="api-schedules-getfire-methodsignature"><code>Schedules.getFire</code></Heading>

Gets one schedule fire.

```ts
getFire(request: GetFireRequest, options?: RequestOptions): Promise<GetFireResponse>;
```

Parameters:

- `request` (`GetFireRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetFireResponse>`

<Heading as="h4" id="api-schedules-list-methodsignature"><code>Schedules.list</code></Heading>

Lists schedules visible to the caller.

```ts
list(request: ListSchedulesRequest, options?: RequestOptions): Promise<ListSchedulesResponse>;
```

Parameters:

- `request` (`ListSchedulesRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListSchedulesResponse>`

<Heading as="h4" id="api-schedules-listfires-methodsignature"><code>Schedules.listFires</code></Heading>

Lists fires for a schedule.

```ts
listFires(request: ListFiresRequest, options?: RequestOptions): Promise<ListFiresResponse>;
```

Parameters:

- `request` (`ListFiresRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListFiresResponse>`

<Heading as="h4" id="api-schedules-pause-methodsignature"><code>Schedules.pause</code></Heading>

Pauses one schedule.

```ts
pause(request: PauseScheduleRequest, options?: RequestOptions): Promise<PauseScheduleResponse>;
```

Parameters:

- `request` (`PauseScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<PauseScheduleResponse>`

<Heading as="h4" id="api-schedules-resume-methodsignature"><code>Schedules.resume</code></Heading>

Resumes one paused schedule.

```ts
resume(request: ResumeScheduleRequest, options?: RequestOptions): Promise<ResumeScheduleResponse>;
```

Parameters:

- `request` (`ResumeScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ResumeScheduleResponse>`

<Heading as="h4" id="api-schedules-update-methodsignature"><code>Schedules.update</code></Heading>

Updates one schedule.

```ts
update(request: UpdateScheduleRequest, options?: RequestOptions): Promise<UpdateScheduleResponse>;
```

Parameters:

- `request` (`UpdateScheduleRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<UpdateScheduleResponse>`

<Heading as="h3" id="api-server-interface"><code>Server</code></Heading>

Pre-session compatibility and safe server-identity operations.

```ts
export interface Server
```

Callable members: [`compatibility()`](#api-server-compatibility-methodsignature), [`info()`](#api-server-info-methodsignature)

<Heading as="h4" id="api-server-compatibility-methodsignature"><code>Server.compatibility</code></Heading>

Starts a fresh compatibility negotiation and makes it the generation shared by subsequent ordinary operations.

```ts
compatibility(options?: RequestOptions): Promise<ServerCompatibility>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<ServerCompatibility>`: A detached compatibility projection.

<Heading as="h4" id="api-server-info-methodsignature"><code>Server.info</code></Heading>

Reads safe server identity after an ordinary cached compatibility preflight.

```ts
info(options?: ServerInfoOptions, requestOptions?: RequestOptions): Promise<ServerInfo>;
```

Parameters:

- `options` (`ServerInfoOptions`, optional): Optional exact provider selector.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<ServerInfo>`: Detached display-only server identity.

<Heading as="h3" id="api-servercapabilities-interface"><code>ServerCapabilities</code></Heading>

Optional server features captured with a session snapshot.

```ts
export interface ServerCapabilities
```

<Heading as="h4" id="api-servercapabilities-agents-propertysignature"><code>ServerCapabilities.agents</code></Heading>

```ts
readonly agents: boolean;
```

<Heading as="h4" id="api-servercapabilities-audio-propertysignature"><code>ServerCapabilities.audio</code></Heading>

```ts
readonly audio: boolean;
```

<Heading as="h4" id="api-servercapabilities-bash-propertysignature"><code>ServerCapabilities.bash</code></Heading>

```ts
readonly bash: boolean;
```

<Heading as="h4" id="api-servercapabilities-debugmcp-propertysignature"><code>ServerCapabilities.debugMcp</code></Heading>

```ts
readonly debugMcp: boolean;
```

<Heading as="h4" id="api-servercapabilities-image-propertysignature"><code>ServerCapabilities.image</code></Heading>

```ts
readonly image: boolean;
```

<Heading as="h4" id="api-servercapabilities-learnedskills-propertysignature"><code>ServerCapabilities.learnedSkills</code></Heading>

```ts
readonly learnedSkills: boolean;
```

<Heading as="h4" id="api-servercapabilities-learningproposals-propertysignature"><code>ServerCapabilities.learningProposals</code></Heading>

```ts
readonly learningProposals: boolean;
```

<Heading as="h4" id="api-servercapabilities-manualcompaction-propertysignature"><code>ServerCapabilities.manualCompaction</code></Heading>

```ts
readonly manualCompaction: boolean;
```

<Heading as="h4" id="api-servercapabilities-manualdream-propertysignature"><code>ServerCapabilities.manualDream</code></Heading>

```ts
readonly manualDream?: ManualDreamCapabilities;
```

<Heading as="h4" id="api-servercapabilities-mcp-propertysignature"><code>ServerCapabilities.mcp</code></Heading>

```ts
readonly mcp: boolean;
```

<Heading as="h4" id="api-servercapabilities-mcpconnectorstatus-propertysignature"><code>ServerCapabilities.mcpConnectorStatus</code></Heading>

```ts
readonly mcpConnectorStatus: boolean;
```

<Heading as="h4" id="api-servercapabilities-memory-propertysignature"><code>ServerCapabilities.memory</code></Heading>

```ts
readonly memory: boolean;
```

<Heading as="h4" id="api-servercapabilities-modelselection-propertysignature"><code>ServerCapabilities.modelSelection</code></Heading>

```ts
readonly modelSelection: boolean;
```

<Heading as="h4" id="api-servercapabilities-posture-propertysignature"><code>ServerCapabilities.posture</code></Heading>

```ts
readonly posture: string;
```

<Heading as="h4" id="api-servercapabilities-reflection-propertysignature"><code>ServerCapabilities.reflection</code></Heading>

```ts
readonly reflection: boolean;
```

<Heading as="h4" id="api-servercapabilities-scheduling-propertysignature"><code>ServerCapabilities.scheduling</code></Heading>

```ts
readonly scheduling: boolean;
```

<Heading as="h4" id="api-servercapabilities-sessiondebug-propertysignature"><code>ServerCapabilities.sessionDebug</code></Heading>

```ts
readonly sessionDebug: boolean;
```

<Heading as="h4" id="api-servercapabilities-skills-propertysignature"><code>ServerCapabilities.skills</code></Heading>

```ts
readonly skills: boolean;
```

<Heading as="h4" id="api-servercapabilities-slashcommands-propertysignature"><code>ServerCapabilities.slashCommands</code></Heading>

```ts
readonly slashCommands: boolean;
```

<Heading as="h4" id="api-servercapabilities-soul-propertysignature"><code>ServerCapabilities.soul</code></Heading>

```ts
readonly soul: boolean;
```

<Heading as="h4" id="api-servercapabilities-steer-propertysignature"><code>ServerCapabilities.steer</code></Heading>

```ts
readonly steer: boolean;
```

<Heading as="h4" id="api-servercapabilities-storagecleanup-propertysignature"><code>ServerCapabilities.storageCleanup</code></Heading>

```ts
readonly storageCleanup: boolean;
```

<Heading as="h4" id="api-servercapabilities-storagehealth-propertysignature"><code>ServerCapabilities.storageHealth</code></Heading>

```ts
readonly storageHealth: boolean;
```

<Heading as="h4" id="api-servercapabilities-storagemigration-propertysignature"><code>ServerCapabilities.storageMigration</code></Heading>

```ts
readonly storageMigration: boolean;
```

<Heading as="h4" id="api-servercapabilities-teams-propertysignature"><code>ServerCapabilities.teams</code></Heading>

```ts
readonly teams: boolean;
```

<Heading as="h4" id="api-servercapabilities-usermodel-propertysignature"><code>ServerCapabilities.userModel</code></Heading>

```ts
readonly userModel: boolean;
```

<Heading as="h4" id="api-servercapabilities-workspaceenrollment-propertysignature"><code>ServerCapabilities.workspaceEnrollment</code></Heading>

```ts
readonly workspaceEnrollment: boolean;
```

<Heading as="h4" id="api-servercapabilities-worktrees-propertysignature"><code>ServerCapabilities.worktrees</code></Heading>

```ts
readonly worktrees: boolean;
```

<Heading as="h3" id="api-servercompatibility-interface"><code>ServerCompatibility</code></Heading>

A detached view of one server compatibility negotiation.

```ts
export interface ServerCompatibility
```

<Heading as="h4" id="api-servercompatibility-apimajor-propertysignature"><code>ServerCompatibility.apiMajor</code></Heading>

The wire-contract major supported by this SDK.

```ts
readonly apiMajor: typeof SUPPORTED_API_MAJOR;
```

<Heading as="h4" id="api-servercompatibility-capabilities-propertysignature"><code>ServerCompatibility.capabilities</code></Heading>

Deployment capabilities currently enabled by the operator.

```ts
readonly capabilities: ServerCapabilities;
```

<Heading as="h4" id="api-servercompatibility-deployment-propertysignature"><code>ServerCompatibility.deployment</code></Heading>

Optional operator-authored deployment label.

```ts
readonly deployment?: string;
```

<Heading as="h4" id="api-servercompatibility-features-propertysignature"><code>ServerCompatibility.features</code></Heading>

Open build-feature identifiers advertised on this listener.

```ts
readonly features: ReadonlySet<string>;
```

<Heading as="h3" id="api-serverinfo-interface"><code>ServerInfo</code></Heading>

Safe, display-only identity information for the connected server.

```ts
export interface ServerInfo
```

<Heading as="h4" id="api-serverinfo-buildid-propertysignature"><code>ServerInfo.buildId</code></Heading>

Linker-stamped server build identity.

```ts
readonly buildId: string;
```

<Heading as="h4" id="api-serverinfo-llmproviderdisplayendpoint-propertysignature"><code>ServerInfo.llmProviderDisplayEndpoint</code></Heading>

Sanitized provider endpoint for diagnostics, never connection configuration.

```ts
readonly llmProviderDisplayEndpoint?: string;
```

<Heading as="h4" id="api-serverinfo-serverimplementation-propertysignature"><code>ServerInfo.serverImplementation</code></Heading>

Stable server composition family, or `unknown`.

```ts
readonly serverImplementation: string;
```

<Heading as="h3" id="api-serverinfooptions-interface"><code>ServerInfoOptions</code></Heading>

Selector accepted by `Server.info`.

```ts
export interface ServerInfoOptions
```

<Heading as="h4" id="api-serverinfooptions-providerid-propertysignature"><code>ServerInfoOptions.providerId</code></Heading>

Already-known provider ID to select for the diagnostic endpoint projection.

```ts
readonly providerId?: string;
```

<Heading as="h3" id="api-session-interface"><code>Session</code></Heading>

A durable Mecatl session handle.

```ts
export interface Session
```

Callable members: [`activity()`](#api-session-activity-methodsignature), [`attach()`](#api-session-attach-methodsignature), [`cancelWorkspaceEnrollment()`](#api-session-cancelworkspaceenrollment-methodsignature), [`clear()`](#api-session-clear-methodsignature), [`close()`](#api-session-close-methodsignature), [`compact()`](#api-session-compact-methodsignature), [`connectWorkspaceServices()`](#api-session-connectworkspaceservices-methodsignature), [`controls()`](#api-session-controls-methodsignature), [`delete()`](#api-session-delete-methodsignature), [`listMcpConnectors()`](#api-session-listmcpconnectors-methodsignature), [`mcpAuthorization()`](#api-session-mcpauthorization-methodsignature), [`rename()`](#api-session-rename-methodsignature), [`resolvePlan()`](#api-session-resolveplan-methodsignature), [`retry()`](#api-session-retry-methodsignature), [`retryWorkspaceEnrollment()`](#api-session-retryworkspaceenrollment-methodsignature), [`run()`](#api-session-run-methodsignature), [`setMode()`](#api-session-setmode-methodsignature), [`snapshot()`](#api-session-snapshot-methodsignature), [`transcript()`](#api-session-transcript-methodsignature)

<Heading as="h4" id="api-session-activity-methodsignature"><code>Session.activity</code></Heading>

Opens the durable cross-run activity stream for this session.

```ts
activity(options?: AttachOptions): Promise<SessionActivity>;
```

Parameters:

- `options` (`AttachOptions`, optional): Replay position, event filtering, and cancellation options.

Returns: `Promise<SessionActivity>`: A single-consumption stream of session activity.

Throws: `CursorScopeError` when a cursor would widen its original filter.

<Heading as="h4" id="api-session-attach-methodsignature"><code>Session.attach</code></Heading>

Attaches to an explicit run, or selects the newest run in the durable log.

```ts
attach(runId?: string, options?: AttachOptions): Promise<AttachedRun>;
```

Parameters:

- `runId` (`string`, optional): Run ID to follow. Omit it to select the newest run.
- `options` (`AttachOptions`, optional): Replay position, event filtering, and cancellation options.

Returns: `Promise<AttachedRun>`: A single-consumption durable stream bound to the selected run.

Throws: `NoRunsError` when no run can be selected.

Throws: `CursorScopeError` when a cursor would widen its original filter.

<Heading as="h4" id="api-session-cancelworkspaceenrollment-methodsignature"><code>Session.cancelWorkspaceEnrollment</code></Heading>

Cancels one exact workspace-enrollment correlation. The SDK accepts terminal server outcomes and leaves a future `unknown` value uninterpreted. It sends no follow-up request.

```ts
cancelWorkspaceEnrollment(enrollmentId: string, options?: RequestOptions): Promise<WorkspaceEnrollment>;
```

Parameters:

- `enrollmentId` (`string`): Exact enrollment correlation to cancel.
- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<WorkspaceEnrollment>`: The terminal server result, or `unknown` for a future result state.

Throws: `ProtocolError` when the returned correlation differs or a known result is pending.

<Heading as="h4" id="api-session-clear-methodsignature"><code>Session.clear</code></Heading>

Creates an empty-history successor without changing this handle.

```ts
clear(options?: ClearSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
```

Parameters:

- `options` (`ClearSessionOptions`, optional): Optional opaque worktree selector.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Session>`: A distinct session handle for the successor.

<Heading as="h4" id="api-session-close-methodsignature"><code>Session.close</code></Heading>

Releases runtime resources without removing the durable session.

```ts
close(options?: RequestOptions): Promise<void>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<void>`: A promise that resolves after local session resources are released.

<Heading as="h4" id="api-session-compact-methodsignature"><code>Session.compact</code></Heading>

Requests one out-of-band compaction pass.

```ts
compact(options?: RequestOptions): Promise<boolean>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<boolean>`: Whether the server reduced the model-visible history.

<Heading as="h4" id="api-session-connectworkspaceservices-methodsignature"><code>Session.connectWorkspaceServices</code></Heading>

Starts or observes this session's whole-bundle workspace enrollment. Each invocation performs one target request. The SDK does not poll, retry, open a browser, or retain the returned presentation URL.

```ts
connectWorkspaceServices(options?: RequestOptions): Promise<WorkspaceEnrollment>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<WorkspaceEnrollment>`: The immediate enrollment state and an ephemeral URL while pending.

Throws: `ProtocolError` when the successful response is structurally malformed.

<Heading as="h4" id="api-session-controls-methodsignature"><code>Session.controls</code></Heading>

Creates prompt-free controls bound to one exact run without opening a watch.

```ts
controls(runId: string): RunControls;
```

Parameters:

- `runId` (`string`): Exact durable run ID to address.

Returns: `RunControls`: A synchronous lightweight control resource.

<Heading as="h4" id="api-session-delete-methodsignature"><code>Session.delete</code></Heading>

Permanently removes the durable session and its sidecars.

```ts
delete(options?: RequestOptions): Promise<void>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<void>`: A promise that resolves after the server removes the session.

<Heading as="h4" id="api-session-id-propertysignature"><code>Session.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-session-listmcpconnectors-methodsignature"><code>Session.listMcpConnectors</code></Heading>

Reads the current broker connector inventory for this session. The inventory describes broker-local publication rather than connector health or enrollment-attempt history. This method performs one target request and never starts enrollment or a direct MCP operation.

```ts
listMcpConnectors(options?: RequestOptions): Promise<McpConnectorInventory>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<McpConnectorInventory>`: A detached SDK-owned connector inventory projection.

<Heading as="h4" id="api-session-mcpauthorization-methodsignature"><code>Session.mcpAuthorization</code></Heading>

Binds one external authorization ID to this session without performing I/O.

```ts
mcpAuthorization(authorizationId: string): McpAuthorization;
```

Parameters:

- `authorizationId` (`string`): Exact non-empty ID from an authorization event.

Returns: `McpAuthorization`: A reusable correlation handle that makes no authorization-state assertion.

<Heading as="h4" id="api-session-rename-methodsignature"><code>Session.rename</code></Heading>

Replaces the title of an eligible session.

```ts
rename(title: string, options?: RequestOptions): Promise<SessionSnapshot>;
```

Parameters:

- `title` (`string`): New human-readable title.
- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<SessionSnapshot>`: The resulting authoritative snapshot.

<Heading as="h4" id="api-session-resolveplan-methodsignature"><code>Session.resolvePlan</code></Heading>

Atomically resolves a durably parked plan and streams its resumed and continuation runs.

```ts
resolvePlan(verdict?: PlanApprovalVerdict): PlanResolution;
```

Parameters:

- `verdict` (`PlanApprovalVerdict`, optional): Plan decision. Defaults to `approve`.

Returns: `PlanResolution`: A single-consumption plan-resolution stream.

Throws: `ServerError` when the session has no parked plan awaiting approval.

<Heading as="h4" id="api-session-retry-methodsignature"><code>Session.retry</code></Heading>

Retries the server-selected eligible failed model step.

```ts
retry(options?: RunOptions, requestOptions?: RequestOptions): Promise<Run>;
```

Parameters:

- `options` (`RunOptions`, optional): Automatic permission and plan-approval responders.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Run>`: The same single-consumption run lifecycle returned by run().

Throws: `SessionBusyError` when the session already has an active run.

<Heading as="h4" id="api-session-retryworkspaceenrollment-methodsignature"><code>Session.retryWorkspaceEnrollment</code></Heading>

Replaces one exact pending workspace-enrollment correlation. Use this explicit operation when the application retained a pending correlation but lost its presentation URL. The SDK performs no automatic recovery after an ambiguous unary result.

```ts
retryWorkspaceEnrollment(enrollmentId: string, options?: RequestOptions): Promise<WorkspaceEnrollment>;
```

Parameters:

- `enrollmentId` (`string`): Exact prior enrollment correlation to replace.
- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<WorkspaceEnrollment>`: A replacement enrollment with a distinct correlation.

Throws: `ProtocolError` when the replacement correlation is missing or unchanged.

<Heading as="h4" id="api-session-run-methodsignature"><code>Session.run</code></Heading>

Starts a run and resolves once its first run-ID-bearing event arrives.

```ts
run(prompt: PromptInput, options?: RunOptions, requestOptions?: RequestOptions): Promise<Run>;
```

Parameters:

- `prompt` (`PromptInput`): Text or ordered text, image, and audio parts for the run.
- `options` (`RunOptions`, optional): Automatic permission and plan-approval responders.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Run>`: A single-consumption handle for the accepted run.

Throws: `PromptValidationError` when the prompt is invalid or unsupported.

Throws: `SessionBusyError` when the session already has an active run.

<Heading as="h4" id="api-session-setmode-methodsignature"><code>Session.setMode</code></Heading>

Changes the permission posture of an eligible session.

```ts
setMode(mode: SessionMode, options?: RequestOptions): Promise<SessionSnapshot>;
```

Parameters:

- `mode` (`SessionMode`): New SDK permission mode.
- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<SessionSnapshot>`: The resulting authoritative snapshot.

<Heading as="h4" id="api-session-snapshot-methodsignature"><code>Session.snapshot</code></Heading>

Reads the authoritative current session snapshot.

```ts
snapshot(options?: RequestOptions): Promise<SessionSnapshot>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<SessionSnapshot>`: A detached SDK-owned projection of the session aggregate.

Throws: `ProtocolError` when the server response is missing or mismatched.

<Heading as="h4" id="api-session-transcript-methodsignature"><code>Session.transcript</code></Heading>

Reads the authoritative model-visible conversation.

```ts
transcript(options?: RequestOptions): Promise<SessionTranscript>;
```

Parameters:

- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<SessionTranscript>`: The ordered transcript without provider-private replay fields.

Throws: `ProtocolError` when the server response is missing or mismatched.

<Heading as="h3" id="api-sessionactivity-interface"><code>SessionActivity</code></Heading>

A durable, cross-run session activity stream.

```ts
export interface SessionActivity extends AsyncIterable<WatchEnvelope>, AsyncDisposable
```

Callable members: [`close()`](#api-sessionactivity-close-methodsignature)

<Heading as="h4" id="api-sessionactivity-close-methodsignature"><code>SessionActivity.close</code></Heading>

Detaches from the watch without cancelling a run.

```ts
close(): Promise<void>;
```

Returns: `Promise<void>`

<Heading as="h4" id="api-sessionactivity-cursor-propertysignature"><code>SessionActivity.cursor</code></Heading>

```ts
readonly cursor: SdkCursor;
```

<Heading as="h3" id="api-sessionactivityreplaystatus-interface"><code>SessionActivityReplayStatus</code></Heading>

Availability and completeness of the separate activity replay plane.

```ts
export interface SessionActivityReplayStatus
```

<Heading as="h4" id="api-sessionactivityreplaystatus-authoritative-propertysignature"><code>SessionActivityReplayStatus.authoritative</code></Heading>

```ts
readonly authoritative: boolean;
```

<Heading as="h4" id="api-sessionactivityreplaystatus-available-propertysignature"><code>SessionActivityReplayStatus.available</code></Heading>

```ts
readonly available: boolean;
```

<Heading as="h4" id="api-sessionactivityreplaystatus-complete-propertysignature"><code>SessionActivityReplayStatus.complete</code></Heading>

```ts
readonly complete: boolean;
```

<Heading as="h3" id="api-sessioncapabilities-interface"><code>SessionCapabilities</code></Heading>

Media input support for the provider and model bound to a session.

```ts
export interface SessionCapabilities
```

<Heading as="h4" id="api-sessioncapabilities-audio-propertysignature"><code>SessionCapabilities.audio</code></Heading>

```ts
readonly audio: boolean;
```

<Heading as="h4" id="api-sessioncapabilities-image-propertysignature"><code>SessionCapabilities.image</code></Heading>

```ts
readonly image: boolean;
```

<Heading as="h3" id="api-sessionlimits-interface"><code>SessionLimits</code></Heading>

Optional stop conditions for a newly created session.

```ts
export interface SessionLimits
```

<Heading as="h4" id="api-sessionlimits-maxconsecutivefailures-propertysignature"><code>SessionLimits.maxConsecutiveFailures</code></Heading>

Maximum consecutive tool failures; zero disables this limit.

```ts
maxConsecutiveFailures?: number;
```

<Heading as="h4" id="api-sessionlimits-maxtoolcalls-propertysignature"><code>SessionLimits.maxToolCalls</code></Heading>

Maximum tool calls; zero disables this limit.

```ts
maxToolCalls?: number;
```

<Heading as="h4" id="api-sessionlimits-maxturns-propertysignature"><code>SessionLimits.maxTurns</code></Heading>

Maximum model turns; zero disables this limit.

```ts
maxTurns?: number;
```

<Heading as="h3" id="api-sessionmcpserver-interface"><code>SessionMcpServer</code></Heading>

A client-provided streaming-HTTP MCP server.

```ts
export interface SessionMcpServer
```

<Heading as="h4" id="api-sessionmcpserver-command-propertysignature"><code>SessionMcpServer.command</code></Heading>

Command-shaped value used only to reject unsupported stdio configurations.

```ts
command?: string;
```

<Heading as="h4" id="api-sessionmcpserver-headers-propertysignature"><code>SessionMcpServer.headers</code></Heading>

HTTP headers sent to the MCP server. Treat their values as secrets.

```ts
headers?: Record<string, string>;
```

<Heading as="h4" id="api-sessionmcpserver-name-propertysignature"><code>SessionMcpServer.name</code></Heading>

Stable server name used in namespaced MCP tool names.

```ts
name?: string;
```

<Heading as="h4" id="api-sessionmcpserver-type-propertysignature"><code>SessionMcpServer.type</code></Heading>

Transport type. The server accepts `http` or an empty value with a URL.

```ts
type?: string;
```

<Heading as="h4" id="api-sessionmcpserver-url-propertysignature"><code>SessionMcpServer.url</code></Heading>

Absolute HTTPS endpoint, or an HTTP endpoint on an explicit loopback host.

```ts
url?: string;
```

<Heading as="h3" id="api-sessionplacement-interface"><code>SessionPlacement</code></Heading>

Bounded display metadata for a session placement.

```ts
export interface SessionPlacement
```

<Heading as="h4" id="api-sessionplacement-branch-propertysignature"><code>SessionPlacement.branch</code></Heading>

```ts
readonly branch: string;
```

<Heading as="h4" id="api-sessionplacement-kind-propertysignature"><code>SessionPlacement.kind</code></Heading>

```ts
readonly kind: string;
```

<Heading as="h4" id="api-sessionplacement-label-propertysignature"><code>SessionPlacement.label</code></Heading>

```ts
readonly label: string;
```

<Heading as="h4" id="api-sessionplacement-revision-propertysignature"><code>SessionPlacement.revision</code></Heading>

```ts
readonly revision: string;
```

<Heading as="h3" id="api-sessionrelationship-interface"><code>SessionRelationship</code></Heading>

Durable links between a session and its parent resource.

```ts
export interface SessionRelationship
```

<Heading as="h4" id="api-sessionrelationship-branchindex-propertysignature"><code>SessionRelationship.branchIndex</code></Heading>

```ts
readonly branchIndex?: number;
```

<Heading as="h4" id="api-sessionrelationship-callid-propertysignature"><code>SessionRelationship.callId</code></Heading>

```ts
readonly callId?: string;
```

<Heading as="h4" id="api-sessionrelationship-debugtargetsessionid-propertysignature"><code>SessionRelationship.debugTargetSessionId</code></Heading>

```ts
readonly debugTargetSessionId?: string;
```

<Heading as="h4" id="api-sessionrelationship-membername-propertysignature"><code>SessionRelationship.memberName</code></Heading>

```ts
readonly memberName?: string;
```

<Heading as="h4" id="api-sessionrelationship-originsessionid-propertysignature"><code>SessionRelationship.originSessionId</code></Heading>

```ts
readonly originSessionId?: string;
```

<Heading as="h4" id="api-sessionrelationship-parentsessionid-propertysignature"><code>SessionRelationship.parentSessionId</code></Heading>

```ts
readonly parentSessionId?: string;
```

<Heading as="h4" id="api-sessionrelationship-schedulename-propertysignature"><code>SessionRelationship.scheduleName</code></Heading>

```ts
readonly scheduleName?: string;
```

<Heading as="h4" id="api-sessionrelationship-teamid-propertysignature"><code>SessionRelationship.teamId</code></Heading>

```ts
readonly teamId?: string;
```

<Heading as="h3" id="api-sessionresolvedmodel-interface"><code>SessionResolvedModel</code></Heading>

The effective provider and model reported for a session.

```ts
export interface SessionResolvedModel
```

<Heading as="h4" id="api-sessionresolvedmodel-contextwindow-propertysignature"><code>SessionResolvedModel.contextWindow</code></Heading>

```ts
readonly contextWindow: bigint;
```

<Heading as="h4" id="api-sessionresolvedmodel-modelid-propertysignature"><code>SessionResolvedModel.modelId</code></Heading>

```ts
readonly modelId: string;
```

<Heading as="h4" id="api-sessionresolvedmodel-providerid-propertysignature"><code>SessionResolvedModel.providerId</code></Heading>

```ts
readonly providerId: string;
```

<Heading as="h4" id="api-sessionresolvedmodel-reasoningeffort-propertysignature"><code>SessionResolvedModel.reasoningEffort</code></Heading>

```ts
readonly reasoningEffort?: string;
```

<Heading as="h3" id="api-sessions-interface"><code>Sessions</code></Heading>

Session lifecycle operations exposed by a Client.

```ts
export interface Sessions
```

Callable members: [`create()`](#api-sessions-create-methodsignature), [`fork()`](#api-sessions-fork-methodsignature), [`get()`](#api-sessions-get-methodsignature), [`list()`](#api-sessions-list-methodsignature)

<Heading as="h4" id="api-sessions-create-methodsignature"><code>Sessions.create</code></Heading>

Creates a session and returns its handle.

```ts
create(options: CreateSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
```

Parameters:

- `options` (`CreateSessionOptions`): Session configuration fields.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Session>`: A handle for the newly created session.

<Heading as="h4" id="api-sessions-fork-methodsignature"><code>Sessions.fork</code></Heading>

Forks an existing session into a distinct successor.

```ts
fork(sourceSessionId: string, options?: ForkSessionOptions, requestOptions?: RequestOptions): Promise<Session>;
```

Parameters:

- `sourceSessionId` (`string`): Session whose conversation will be copied.
- `options` (`ForkSessionOptions`, optional): Optional title, model, reasoning, and worktree overrides.
- `requestOptions` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Session>`: A handle for the forked successor session.

<Heading as="h4" id="api-sessions-get-methodsignature"><code>Sessions.get</code></Heading>

Loads an existing session by ID.

```ts
get(sessionId: string, options?: RequestOptions): Promise<Session>;
```

Parameters:

- `sessionId` (`string`): Durable session ID to load.
- `options` (`RequestOptions`, optional): Request headers, cancellation signal, and deadline.

Returns: `Promise<Session>`: A handle bound to the requested session.

<Heading as="h4" id="api-sessions-list-methodsignature"><code>Sessions.list</code></Heading>

Lists the sessions visible to the authenticated caller.

```ts
list(request: ListSessionsRequest, options?: RequestOptions): Promise<ListSessionsResponse>;
```

Parameters:

- `request` (`ListSessionsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListSessionsResponse>`

<Heading as="h3" id="api-sessionsnapshot-interface"><code>SessionSnapshot</code></Heading>

An authoritative, detached view of one durable session.

```ts
export interface SessionSnapshot
```

<Heading as="h4" id="api-sessionsnapshot-capabilities-propertysignature"><code>SessionSnapshot.capabilities</code></Heading>

```ts
readonly capabilities?: ServerCapabilities;
```

<Heading as="h4" id="api-sessionsnapshot-createdatunix-propertysignature"><code>SessionSnapshot.createdAtUnix</code></Heading>

```ts
readonly createdAtUnix: bigint;
```

<Heading as="h4" id="api-sessionsnapshot-debugmcpservers-propertysignature"><code>SessionSnapshot.debugMcpServers</code></Heading>

```ts
readonly debugMcpServers: readonly string[];
```

<Heading as="h4" id="api-sessionsnapshot-debugmcptools-propertysignature"><code>SessionSnapshot.debugMcpTools</code></Heading>

```ts
readonly debugMcpTools: readonly string[];
```

<Heading as="h4" id="api-sessionsnapshot-kind-propertysignature"><code>SessionSnapshot.kind</code></Heading>

```ts
readonly kind: string;
```

<Heading as="h4" id="api-sessionsnapshot-limits-propertysignature"><code>SessionSnapshot.limits</code></Heading>

```ts
readonly limits?: SessionSnapshotLimits;
```

<Heading as="h4" id="api-sessionsnapshot-mode-propertysignature"><code>SessionSnapshot.mode</code></Heading>

```ts
readonly mode: SessionMode;
```

<Heading as="h4" id="api-sessionsnapshot-placement-propertysignature"><code>SessionSnapshot.placement</code></Heading>

```ts
readonly placement?: SessionPlacement;
```

<Heading as="h4" id="api-sessionsnapshot-relationship-propertysignature"><code>SessionSnapshot.relationship</code></Heading>

```ts
readonly relationship?: SessionRelationship;
```

<Heading as="h4" id="api-sessionsnapshot-resolvedmodel-propertysignature"><code>SessionSnapshot.resolvedModel</code></Heading>

```ts
readonly resolvedModel?: SessionResolvedModel;
```

<Heading as="h4" id="api-sessionsnapshot-sessioncapabilities-propertysignature"><code>SessionSnapshot.sessionCapabilities</code></Heading>

```ts
readonly sessionCapabilities?: SessionCapabilities;
```

<Heading as="h4" id="api-sessionsnapshot-sessionid-propertysignature"><code>SessionSnapshot.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h4" id="api-sessionsnapshot-state-propertysignature"><code>SessionSnapshot.state</code></Heading>

```ts
readonly state: string;
```

<Heading as="h4" id="api-sessionsnapshot-title-propertysignature"><code>SessionSnapshot.title</code></Heading>

```ts
readonly title?: SessionTitle;
```

<Heading as="h4" id="api-sessionsnapshot-tokenusage-propertysignature"><code>SessionSnapshot.tokenUsage</code></Heading>

```ts
readonly tokenUsage: Readonly<Record<string, SessionTokenUsage>>;
```

<Heading as="h4" id="api-sessionsnapshot-toolcalls-propertysignature"><code>SessionSnapshot.toolCalls</code></Heading>

```ts
readonly toolCalls: number;
```

<Heading as="h4" id="api-sessionsnapshot-turns-propertysignature"><code>SessionSnapshot.turns</code></Heading>

```ts
readonly turns: number;
```

<Heading as="h3" id="api-sessionsnapshotlimits-interface"><code>SessionSnapshotLimits</code></Heading>

Stop conditions reported by a session snapshot.

```ts
export interface SessionSnapshotLimits
```

<Heading as="h4" id="api-sessionsnapshotlimits-maxconsecutivefailures-propertysignature"><code>SessionSnapshotLimits.maxConsecutiveFailures</code></Heading>

```ts
readonly maxConsecutiveFailures: number;
```

<Heading as="h4" id="api-sessionsnapshotlimits-maxtoolcalls-propertysignature"><code>SessionSnapshotLimits.maxToolCalls</code></Heading>

```ts
readonly maxToolCalls: number;
```

<Heading as="h4" id="api-sessionsnapshotlimits-maxturns-propertysignature"><code>SessionSnapshotLimits.maxTurns</code></Heading>

```ts
readonly maxTurns: number;
```

<Heading as="h3" id="api-sessiontitle-interface"><code>SessionTitle</code></Heading>

Normalized session title metadata.

```ts
export interface SessionTitle
```

<Heading as="h4" id="api-sessiontitle-generationstate-propertysignature"><code>SessionTitle.generationState</code></Heading>

```ts
readonly generationState?: string;
```

<Heading as="h4" id="api-sessiontitle-latestattempt-propertysignature"><code>SessionTitle.latestAttempt</code></Heading>

```ts
readonly latestAttempt?: SessionTitleAttempt;
```

<Heading as="h4" id="api-sessiontitle-provenance-propertysignature"><code>SessionTitle.provenance</code></Heading>

```ts
readonly provenance: string;
```

<Heading as="h4" id="api-sessiontitle-revision-propertysignature"><code>SessionTitle.revision</code></Heading>

```ts
readonly revision?: bigint;
```

<Heading as="h4" id="api-sessiontitle-value-propertysignature"><code>SessionTitle.value</code></Heading>

```ts
readonly value: string;
```

<Heading as="h3" id="api-sessiontitleattempt-interface"><code>SessionTitleAttempt</code></Heading>

The latest bounded title-generation attempt.

```ts
export interface SessionTitleAttempt
```

<Heading as="h4" id="api-sessiontitleattempt-id-propertysignature"><code>SessionTitleAttempt.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-sessiontitleattempt-outcome-propertysignature"><code>SessionTitleAttempt.outcome</code></Heading>

```ts
readonly outcome: string;
```

<Heading as="h3" id="api-sessiontitleeventpayload-interface"><code>SessionTitleEventPayload</code></Heading>

The source-free payload of a `session.title` event.

```ts
export interface SessionTitleEventPayload
```

<Heading as="h4" id="api-sessiontitleeventpayload-generationstate-propertysignature"><code>SessionTitleEventPayload.generationState</code></Heading>

```ts
readonly generationState: string;
```

<Heading as="h4" id="api-sessiontitleeventpayload-latestattempt-propertysignature"><code>SessionTitleEventPayload.latestAttempt</code></Heading>

```ts
readonly latestAttempt?: TitleAttemptEventPayload | undefined;
```

<Heading as="h4" id="api-sessiontitleeventpayload-provenance-propertysignature"><code>SessionTitleEventPayload.provenance</code></Heading>

```ts
readonly provenance: string;
```

<Heading as="h4" id="api-sessiontitleeventpayload-revision-propertysignature"><code>SessionTitleEventPayload.revision</code></Heading>

```ts
readonly revision: bigint;
```

<Heading as="h4" id="api-sessiontitleeventpayload-title-propertysignature"><code>SessionTitleEventPayload.title</code></Heading>

```ts
readonly title: string;
```

<Heading as="h3" id="api-sessiontokenusage-interface"><code>SessionTokenUsage</code></Heading>

One durable session usage bucket.

```ts
export interface SessionTokenUsage
```

<Heading as="h4" id="api-sessiontokenusage-models-propertysignature"><code>SessionTokenUsage.models</code></Heading>

```ts
readonly models: Readonly<Record<string, EventUsage>>;
```

<Heading as="h4" id="api-sessiontokenusage-total-propertysignature"><code>SessionTokenUsage.total</code></Heading>

```ts
readonly total?: EventUsage;
```

<Heading as="h3" id="api-sessiontranscript-interface"><code>SessionTranscript</code></Heading>

The authoritative, ordered conversation for one session.

```ts
export interface SessionTranscript
```

<Heading as="h4" id="api-sessiontranscript-activity-propertysignature"><code>SessionTranscript.activity</code></Heading>

```ts
readonly activity?: SessionActivityReplayStatus;
```

<Heading as="h4" id="api-sessiontranscript-complete-propertysignature"><code>SessionTranscript.complete</code></Heading>

```ts
readonly complete: boolean;
```

<Heading as="h4" id="api-sessiontranscript-kind-propertysignature"><code>SessionTranscript.kind</code></Heading>

```ts
readonly kind: string;
```

<Heading as="h4" id="api-sessiontranscript-messages-propertysignature"><code>SessionTranscript.messages</code></Heading>

```ts
readonly messages: readonly SessionTranscriptMessage[];
```

<Heading as="h4" id="api-sessiontranscript-relationship-propertysignature"><code>SessionTranscript.relationship</code></Heading>

```ts
readonly relationship?: SessionRelationship;
```

<Heading as="h4" id="api-sessiontranscript-sessionid-propertysignature"><code>SessionTranscript.sessionId</code></Heading>

```ts
readonly sessionId: string;
```

<Heading as="h3" id="api-sessiontranscriptmessage-interface"><code>SessionTranscriptMessage</code></Heading>

One human-displayable message in the authoritative session transcript.

```ts
export interface SessionTranscriptMessage
```

<Heading as="h4" id="api-sessiontranscriptmessage-parts-propertysignature"><code>SessionTranscriptMessage.parts</code></Heading>

```ts
readonly parts: readonly EventContent[];
```

<Heading as="h4" id="api-sessiontranscriptmessage-role-propertysignature"><code>SessionTranscriptMessage.role</code></Heading>

```ts
readonly role: string;
```

<Heading as="h4" id="api-sessiontranscriptmessage-text-propertysignature"><code>SessionTranscriptMessage.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-sessiontranscriptmessage-toolcalls-propertysignature"><code>SessionTranscriptMessage.toolCalls</code></Heading>

```ts
readonly toolCalls: readonly ToolCallEventPayload[];
```

<Heading as="h4" id="api-sessiontranscriptmessage-toolresult-propertysignature"><code>SessionTranscriptMessage.toolResult</code></Heading>

```ts
readonly toolResult?: ToolResultEventPayload;
```

<Heading as="h3" id="api-skills-interface"><code>Skills</code></Heading>

Configured skill inventory operations.

```ts
export interface Skills
```

Callable members: [`list()`](#api-skills-list-methodsignature)

<Heading as="h4" id="api-skills-list-methodsignature"><code>Skills.list</code></Heading>

Lists the configured skills visible to the server.

```ts
list(request: ListSkillsRequest, options?: RequestOptions): Promise<ListSkillsResponse>;
```

Parameters:

- `request` (`ListSkillsRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListSkillsResponse>`

<Heading as="h3" id="api-soul-interface"><code>Soul</code></Heading>

Resolved soul inspection operations.

```ts
export interface Soul
```

Callable members: [`get()`](#api-soul-get-methodsignature)

<Heading as="h4" id="api-soul-get-methodsignature"><code>Soul.get</code></Heading>

Gets the server's resolved soul snapshot.

```ts
get(request: GetSoulRequest, options?: RequestOptions): Promise<GetSoulResponse>;
```

Parameters:

- `request` (`GetSoulRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetSoulResponse>`

<Heading as="h3" id="api-steereventpayload-interface"><code>SteerEventPayload</code></Heading>

The payload of a committed `steer` event.

```ts
export interface SteerEventPayload
```

<Heading as="h4" id="api-steereventpayload-messageid-propertysignature"><code>SteerEventPayload.messageId</code></Heading>

```ts
readonly messageId: string;
```

<Heading as="h4" id="api-steereventpayload-parts-propertysignature"><code>SteerEventPayload.parts</code></Heading>

```ts
readonly parts: readonly EventContent[];
```

<Heading as="h4" id="api-steereventpayload-text-propertysignature"><code>SteerEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h3" id="api-steeroutcomeeventpayload-interface"><code>SteerOutcomeEventPayload</code></Heading>

The payload of a gRPC-only `steer.outcome` event.

```ts
export interface SteerOutcomeEventPayload
```

<Heading as="h4" id="api-steeroutcomeeventpayload-messageid-propertysignature"><code>SteerOutcomeEventPayload.messageId</code></Heading>

```ts
readonly messageId: string;
```

<Heading as="h4" id="api-steeroutcomeeventpayload-outcome-propertysignature"><code>SteerOutcomeEventPayload.outcome</code></Heading>

```ts
readonly outcome: 0 | 1 | 2 | 3 | 4 | 5;
```

<Heading as="h4" id="api-steeroutcomeeventpayload-promoted-propertysignature"><code>SteerOutcomeEventPayload.promoted</code></Heading>

```ts
readonly promoted: boolean;
```

<Heading as="h4" id="api-steeroutcomeeventpayload-text-propertysignature"><code>SteerOutcomeEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h3" id="api-storage-interface"><code>Storage</code></Heading>

Storage health, migration, and cleanup operations owned by the server.

```ts
export interface Storage
```

Callable members: [`applyCleanup()`](#api-storage-applycleanup-methodsignature), [`applyMigration()`](#api-storage-applymigration-methodsignature), [`cancelCleanup()`](#api-storage-cancelcleanup-methodsignature), [`cancelMigration()`](#api-storage-cancelmigration-methodsignature), [`getCleanupJob()`](#api-storage-getcleanupjob-methodsignature), [`getHealth()`](#api-storage-gethealth-methodsignature), [`getMigrationJob()`](#api-storage-getmigrationjob-methodsignature), [`planCleanup()`](#api-storage-plancleanup-methodsignature), [`planMigration()`](#api-storage-planmigration-methodsignature), [`resumeMigration()`](#api-storage-resumemigration-methodsignature)

<Heading as="h4" id="api-storage-applycleanup-methodsignature"><code>Storage.applyCleanup</code></Heading>

Starts a planned session-storage cleanup.

```ts
applyCleanup(request: ApplySessionCleanupRequest, options?: RequestOptions): Promise<CleanupJob>;
```

Parameters:

- `request` (`ApplySessionCleanupRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<CleanupJob>`

<Heading as="h4" id="api-storage-applymigration-methodsignature"><code>Storage.applyMigration</code></Heading>

Starts a planned session-storage migration.

```ts
applyMigration(request: ApplySessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;
```

Parameters:

- `request` (`ApplySessionMigrationRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SessionMigrationJob>`

<Heading as="h4" id="api-storage-cancelcleanup-methodsignature"><code>Storage.cancelCleanup</code></Heading>

Cancels a session-storage cleanup.

```ts
cancelCleanup(request: CancelSessionCleanupRequest, options?: RequestOptions): Promise<CleanupJob>;
```

Parameters:

- `request` (`CancelSessionCleanupRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<CleanupJob>`

<Heading as="h4" id="api-storage-cancelmigration-methodsignature"><code>Storage.cancelMigration</code></Heading>

Cancels a session-storage migration.

```ts
cancelMigration(request: CancelSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;
```

Parameters:

- `request` (`CancelSessionMigrationRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SessionMigrationJob>`

<Heading as="h4" id="api-storage-getcleanupjob-methodsignature"><code>Storage.getCleanupJob</code></Heading>

Gets one session-storage cleanup job.

```ts
getCleanupJob(request: GetSessionCleanupJobRequest, options?: RequestOptions): Promise<CleanupJob>;
```

Parameters:

- `request` (`GetSessionCleanupJobRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<CleanupJob>`

<Heading as="h4" id="api-storage-gethealth-methodsignature"><code>Storage.getHealth</code></Heading>

Gets the configured session-storage health.

```ts
getHealth(request: GetStorageHealthRequest, options?: RequestOptions): Promise<GetStorageHealthResponse>;
```

Parameters:

- `request` (`GetStorageHealthRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetStorageHealthResponse>`

<Heading as="h4" id="api-storage-getmigrationjob-methodsignature"><code>Storage.getMigrationJob</code></Heading>

Gets one session-storage migration job.

```ts
getMigrationJob(request: GetSessionMigrationJobRequest, options?: RequestOptions): Promise<SessionMigrationJob>;
```

Parameters:

- `request` (`GetSessionMigrationJobRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SessionMigrationJob>`

<Heading as="h4" id="api-storage-plancleanup-methodsignature"><code>Storage.planCleanup</code></Heading>

Previews a session-storage cleanup.

```ts
planCleanup(request: PlanSessionCleanupRequest, options?: RequestOptions): Promise<PlanSessionCleanupResponse>;
```

Parameters:

- `request` (`PlanSessionCleanupRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<PlanSessionCleanupResponse>`

<Heading as="h4" id="api-storage-planmigration-methodsignature"><code>Storage.planMigration</code></Heading>

Previews a session-storage migration.

```ts
planMigration(request: PlanSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationPlan>;
```

Parameters:

- `request` (`PlanSessionMigrationRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SessionMigrationPlan>`

<Heading as="h4" id="api-storage-resumemigration-methodsignature"><code>Storage.resumeMigration</code></Heading>

Resumes an interrupted session-storage migration.

```ts
resumeMigration(request: ResumeSessionMigrationRequest, options?: RequestOptions): Promise<SessionMigrationJob>;
```

Parameters:

- `request` (`ResumeSessionMigrationRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SessionMigrationJob>`

<Heading as="h3" id="api-subagenteventpayload-interface"><code>SubagentEventPayload</code></Heading>

The payload shared by `subagent.*` events.

```ts
export interface SubagentEventPayload
```

<Heading as="h4" id="api-subagenteventpayload-background-propertysignature"><code>SubagentEventPayload.background</code></Heading>

```ts
readonly background: boolean;
```

<Heading as="h4" id="api-subagenteventpayload-cause-propertysignature"><code>SubagentEventPayload.cause</code></Heading>

```ts
readonly cause: string;
```

<Heading as="h4" id="api-subagenteventpayload-childid-propertysignature"><code>SubagentEventPayload.childId</code></Heading>

```ts
readonly childId: string;
```

<Heading as="h4" id="api-subagenteventpayload-detail-propertysignature"><code>SubagentEventPayload.detail</code></Heading>

```ts
readonly detail: string;
```

<Heading as="h4" id="api-subagenteventpayload-durationms-propertysignature"><code>SubagentEventPayload.durationMs</code></Heading>

```ts
readonly durationMs: bigint;
```

<Heading as="h4" id="api-subagenteventpayload-goal-propertysignature"><code>SubagentEventPayload.goal</code></Heading>

```ts
readonly goal: string;
```

<Heading as="h4" id="api-subagenteventpayload-innerkind-propertysignature"><code>SubagentEventPayload.innerKind</code></Heading>

```ts
readonly innerKind: string;
```

<Heading as="h4" id="api-subagenteventpayload-iserror-propertysignature"><code>SubagentEventPayload.isError</code></Heading>

```ts
readonly isError: boolean;
```

<Heading as="h4" id="api-subagenteventpayload-model-propertysignature"><code>SubagentEventPayload.model</code></Heading>

```ts
readonly model: string;
```

<Heading as="h4" id="api-subagenteventpayload-parentcallid-propertysignature"><code>SubagentEventPayload.parentCallId</code></Heading>

```ts
readonly parentCallId: string;
```

<Heading as="h4" id="api-subagenteventpayload-routedcategory-propertysignature"><code>SubagentEventPayload.routedCategory</code></Heading>

```ts
readonly routedCategory: string;
```

<Heading as="h4" id="api-subagenteventpayload-routedmodel-propertysignature"><code>SubagentEventPayload.routedModel</code></Heading>

```ts
readonly routedModel: string;
```

<Heading as="h4" id="api-subagenteventpayload-routingreason-propertysignature"><code>SubagentEventPayload.routingReason</code></Heading>

```ts
readonly routingReason: string;
```

<Heading as="h4" id="api-subagenteventpayload-stop-propertysignature"><code>SubagentEventPayload.stop</code></Heading>

```ts
readonly stop: string;
```

<Heading as="h4" id="api-subagenteventpayload-text-propertysignature"><code>SubagentEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-subagenteventpayload-toolcount-propertysignature"><code>SubagentEventPayload.toolCount</code></Heading>

```ts
readonly toolCount: number;
```

<Heading as="h4" id="api-subagenteventpayload-toolname-propertysignature"><code>SubagentEventPayload.toolName</code></Heading>

```ts
readonly toolName: string;
```

<Heading as="h4" id="api-subagenteventpayload-usage-propertysignature"><code>SubagentEventPayload.usage</code></Heading>

```ts
readonly usage?: EventUsage | undefined;
```

<Heading as="h3" id="api-team-interface"><code>Team</code></Heading>

A handle for direct team operations.

```ts
export interface Team
```

Callable members: [`cancel()`](#api-team-cancel-methodsignature), [`cleanup()`](#api-team-cleanup-methodsignature), [`list()`](#api-team-list-methodsignature), [`message()`](#api-team-message-methodsignature), [`run()`](#api-team-run-methodsignature), [`spawn()`](#api-team-spawn-methodsignature)

<Heading as="h4" id="api-team-cancel-methodsignature"><code>Team.cancel</code></Heading>

Cancels one team member.

```ts
cancel(member: string, options?: RequestOptions): Promise<CancelTeammateResponse>;
```

Parameters:

- `member` (`string`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<CancelTeammateResponse>`

<Heading as="h4" id="api-team-cleanup-methodsignature"><code>Team.cleanup</code></Heading>

Permanently removes the server-owned team.

```ts
cleanup(options?: RequestOptions): Promise<CleanupTeamResponse>;
```

Parameters:

- `options` (`RequestOptions`, optional)

Returns: `Promise<CleanupTeamResponse>`

<Heading as="h4" id="api-team-id-propertysignature"><code>Team.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-team-initialmembers-propertysignature"><code>Team.initialMembers</code></Heading>

The typed initial roster returned atomically by CreateTeam. This is not a live view.

```ts
readonly initialMembers: readonly TeamMember[];
```

<Heading as="h4" id="api-team-list-methodsignature"><code>Team.list</code></Heading>

Returns the current team roster and state.

```ts
list(options?: RequestOptions): Promise<ListTeamResponse>;
```

Parameters:

- `options` (`RequestOptions`, optional)

Returns: `Promise<ListTeamResponse>`

<Heading as="h4" id="api-team-message-methodsignature"><code>Team.message</code></Heading>

Sends a message to a team member.

```ts
message(message: TeamMessageOptions, options?: RequestOptions): Promise<SendTeammateMessageResponse>;
```

Parameters:

- `message` (`TeamMessageOptions`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SendTeammateMessageResponse>`

<Heading as="h4" id="api-team-run-methodsignature"><code>Team.run</code></Heading>

Starts a single-consumption team run.

```ts
run(options?: RequestOptions): TeamRun;
```

Parameters:

- `options` (`RequestOptions`, optional)

Returns: `TeamRun`

<Heading as="h4" id="api-team-spawn-methodsignature"><code>Team.spawn</code></Heading>

Adds one member to the team.

```ts
spawn(member: TeamMemberOptions, options?: RequestOptions): Promise<SpawnTeammateResponse>;
```

Parameters:

- `member` (`TeamMemberOptions`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<SpawnTeammateResponse>`

<Heading as="h3" id="api-teameventpayload-interface"><code>TeamEventPayload</code></Heading>

The payload shared by `team.*` events.

```ts
export interface TeamEventPayload
```

<Heading as="h4" id="api-teameventpayload-cause-propertysignature"><code>TeamEventPayload.cause</code></Heading>

```ts
readonly cause: string;
```

<Heading as="h4" id="api-teameventpayload-contextused-propertysignature"><code>TeamEventPayload.contextUsed</code></Heading>

```ts
readonly contextUsed: bigint;
```

<Heading as="h4" id="api-teameventpayload-contextwindow-propertysignature"><code>TeamEventPayload.contextWindow</code></Heading>

```ts
readonly contextWindow: bigint;
```

<Heading as="h4" id="api-teameventpayload-detail-propertysignature"><code>TeamEventPayload.detail</code></Heading>

```ts
readonly detail: string;
```

<Heading as="h4" id="api-teameventpayload-dispositions-propertysignature"><code>TeamEventPayload.dispositions</code></Heading>

```ts
readonly dispositions: readonly TeamMemberDispositionEventPayload[];
```

<Heading as="h4" id="api-teameventpayload-findings-propertysignature"><code>TeamEventPayload.findings</code></Heading>

```ts
readonly findings: readonly TeamFindingEventPayload[];
```

<Heading as="h4" id="api-teameventpayload-innerkind-propertysignature"><code>TeamEventPayload.innerKind</code></Heading>

```ts
readonly innerKind: string;
```

<Heading as="h4" id="api-teameventpayload-iserror-propertysignature"><code>TeamEventPayload.isError</code></Heading>

```ts
readonly isError: boolean;
```

<Heading as="h4" id="api-teameventpayload-member-propertysignature"><code>TeamEventPayload.member</code></Heading>

```ts
readonly member: string;
```

<Heading as="h4" id="api-teameventpayload-membersessionid-propertysignature"><code>TeamEventPayload.memberSessionId</code></Heading>

```ts
readonly memberSessionId: string;
```

<Heading as="h4" id="api-teameventpayload-parentcallid-propertysignature"><code>TeamEventPayload.parentCallId</code></Heading>

```ts
readonly parentCallId: string;
```

<Heading as="h4" id="api-teameventpayload-roster-propertysignature"><code>TeamEventPayload.roster</code></Heading>

```ts
readonly roster: readonly TeamMemberSpecEventPayload[];
```

<Heading as="h4" id="api-teameventpayload-rounds-propertysignature"><code>TeamEventPayload.rounds</code></Heading>

```ts
readonly rounds: number;
```

<Heading as="h4" id="api-teameventpayload-stop-propertysignature"><code>TeamEventPayload.stop</code></Heading>

```ts
readonly stop: string;
```

<Heading as="h4" id="api-teameventpayload-tasks-propertysignature"><code>TeamEventPayload.tasks</code></Heading>

```ts
readonly tasks: readonly TeamTaskEventPayload[];
```

<Heading as="h4" id="api-teameventpayload-teamid-propertysignature"><code>TeamEventPayload.teamId</code></Heading>

```ts
readonly teamId: string;
```

<Heading as="h4" id="api-teameventpayload-text-propertysignature"><code>TeamEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h4" id="api-teameventpayload-toolname-propertysignature"><code>TeamEventPayload.toolName</code></Heading>

```ts
readonly toolName: string;
```

<Heading as="h4" id="api-teameventpayload-usage-propertysignature"><code>TeamEventPayload.usage</code></Heading>

```ts
readonly usage?: EventUsage | undefined;
```

<Heading as="h3" id="api-teamfindingeventpayload-interface"><code>TeamFindingEventPayload</code></Heading>

One finding in a team event snapshot.

```ts
export interface TeamFindingEventPayload
```

<Heading as="h4" id="api-teamfindingeventpayload-body-propertysignature"><code>TeamFindingEventPayload.body</code></Heading>

```ts
readonly body: string;
```

<Heading as="h4" id="api-teamfindingeventpayload-member-propertysignature"><code>TeamFindingEventPayload.member</code></Heading>

```ts
readonly member: string;
```

<Heading as="h3" id="api-teammemberdispositioneventpayload-interface"><code>TeamMemberDispositionEventPayload</code></Heading>

One terminal member disposition in a `team.end` payload.

```ts
export interface TeamMemberDispositionEventPayload
```

<Heading as="h4" id="api-teammemberdispositioneventpayload-errorrounds-propertysignature"><code>TeamMemberDispositionEventPayload.errorRounds</code></Heading>

```ts
readonly errorRounds: number;
```

<Heading as="h4" id="api-teammemberdispositioneventpayload-name-propertysignature"><code>TeamMemberDispositionEventPayload.name</code></Heading>

```ts
readonly name: string;
```

<Heading as="h4" id="api-teammemberdispositioneventpayload-reason-propertysignature"><code>TeamMemberDispositionEventPayload.reason</code></Heading>

```ts
readonly reason: 0 | 1 | 2 | 3;
```

<Heading as="h4" id="api-teammemberdispositioneventpayload-stopped-propertysignature"><code>TeamMemberDispositionEventPayload.stopped</code></Heading>

```ts
readonly stopped: boolean;
```

<Heading as="h3" id="api-teammemberoptions-interface"><code>TeamMemberOptions</code></Heading>

One initial or incrementally spawned team member.

```ts
export interface TeamMemberOptions
```

<Heading as="h4" id="api-teammemberoptions-agenttype-propertysignature"><code>TeamMemberOptions.agentType</code></Heading>

Agent-definition name adopted by this member.

```ts
agentType?: string;
```

<Heading as="h4" id="api-teammemberoptions-initialprompt-propertysignature"><code>TeamMemberOptions.initialPrompt</code></Heading>

First-turn prompt for this member.

```ts
initialPrompt?: string;
```

<Heading as="h4" id="api-teammemberoptions-lead-propertysignature"><code>TeamMemberOptions.lead</code></Heading>

Marks this member as the team coordinator.

```ts
lead?: boolean;
```

<Heading as="h4" id="api-teammemberoptions-mutating-propertysignature"><code>TeamMemberOptions.mutating</code></Heading>

Requests an isolated workspace with mutating tools.

```ts
mutating?: boolean;
```

<Heading as="h4" id="api-teammemberoptions-name-propertysignature"><code>TeamMemberOptions.name</code></Heading>

Unique handle used to address this member.

```ts
name: string;
```

<Heading as="h3" id="api-teammemberspeceventpayload-interface"><code>TeamMemberSpecEventPayload</code></Heading>

One member in a `team.start` roster.

```ts
export interface TeamMemberSpecEventPayload
```

<Heading as="h4" id="api-teammemberspeceventpayload-lead-propertysignature"><code>TeamMemberSpecEventPayload.lead</code></Heading>

```ts
readonly lead: boolean;
```

<Heading as="h4" id="api-teammemberspeceventpayload-model-propertysignature"><code>TeamMemberSpecEventPayload.model</code></Heading>

```ts
readonly model: string;
```

<Heading as="h4" id="api-teammemberspeceventpayload-mutating-propertysignature"><code>TeamMemberSpecEventPayload.mutating</code></Heading>

```ts
readonly mutating: boolean;
```

<Heading as="h4" id="api-teammemberspeceventpayload-name-propertysignature"><code>TeamMemberSpecEventPayload.name</code></Heading>

```ts
readonly name: string;
```

<Heading as="h4" id="api-teammemberspeceventpayload-role-propertysignature"><code>TeamMemberSpecEventPayload.role</code></Heading>

```ts
readonly role: string;
```

<Heading as="h4" id="api-teammemberspeceventpayload-routedcategory-propertysignature"><code>TeamMemberSpecEventPayload.routedCategory</code></Heading>

```ts
readonly routedCategory: string;
```

<Heading as="h4" id="api-teammemberspeceventpayload-routedmodel-propertysignature"><code>TeamMemberSpecEventPayload.routedModel</code></Heading>

```ts
readonly routedModel: string;
```

<Heading as="h4" id="api-teammemberspeceventpayload-routingreason-propertysignature"><code>TeamMemberSpecEventPayload.routingReason</code></Heading>

```ts
readonly routingReason: string;
```

<Heading as="h3" id="api-teammessageoptions-interface"><code>TeamMessageOptions</code></Heading>

One operator message sent to a team member.

```ts
export interface TeamMessageOptions
```

<Heading as="h4" id="api-teammessageoptions-body-propertysignature"><code>TeamMessageOptions.body</code></Heading>

Message body delivered to the member.

```ts
body: string;
```

<Heading as="h4" id="api-teammessageoptions-from-propertysignature"><code>TeamMessageOptions.from</code></Heading>

Sender label recorded with the message.

```ts
from?: string;
```

<Heading as="h4" id="api-teammessageoptions-to-propertysignature"><code>TeamMessageOptions.to</code></Heading>

Recipient member handle.

```ts
to: string;
```

<Heading as="h3" id="api-teamoutcomerunevent-interface"><code>TeamOutcomeRunEvent</code></Heading>

The one terminal outcome from a direct team run.

```ts
export interface TeamOutcomeRunEvent
```

<Heading as="h4" id="api-teamoutcomerunevent-kind-propertysignature"><code>TeamOutcomeRunEvent.kind</code></Heading>

```ts
readonly kind: "outcome";
```

<Heading as="h4" id="api-teamoutcomerunevent-outcome-propertysignature"><code>TeamOutcomeRunEvent.outcome</code></Heading>

```ts
readonly outcome: TeamOutcome;
```

<Heading as="h3" id="api-teamrun-interface"><code>TeamRun</code></Heading>

One single-consumption direct team run.

```ts
export interface TeamRun extends AsyncIterable<TeamRunEvent>
```

Callable members: [`result()`](#api-teamrun-result-methodsignature)

<Heading as="h4" id="api-teamrun-result-methodsignature"><code>TeamRun.result</code></Heading>

Drains the stream and returns its one required terminal outcome.

```ts
result(): Promise<TeamOutcome>;
```

Returns: `Promise<TeamOutcome>`

<Heading as="h4" id="api-teamrun-teamid-propertysignature"><code>TeamRun.teamId</code></Heading>

```ts
readonly teamId: string;
```

<Heading as="h3" id="api-teams-interface"><code>Teams</code></Heading>

Direct team creation operations exposed by a Client.

```ts
export interface Teams
```

Callable members: [`create()`](#api-teams-create-methodsignature)

<Heading as="h4" id="api-teams-create-methodsignature"><code>Teams.create</code></Heading>

Creates a server-owned team bound to a session.

```ts
create(request: CreateTeamOptions, options?: RequestOptions): Promise<Team>;
```

Parameters:

- `request` (`CreateTeamOptions`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<Team>`

<Heading as="h3" id="api-teamtaskeventpayload-interface"><code>TeamTaskEventPayload</code></Heading>

One task in a team event snapshot.

```ts
export interface TeamTaskEventPayload
```

<Heading as="h4" id="api-teamtaskeventpayload-assignee-propertysignature"><code>TeamTaskEventPayload.assignee</code></Heading>

```ts
readonly assignee: string;
```

<Heading as="h4" id="api-teamtaskeventpayload-deps-propertysignature"><code>TeamTaskEventPayload.deps</code></Heading>

```ts
readonly deps: readonly string[];
```

<Heading as="h4" id="api-teamtaskeventpayload-description-propertysignature"><code>TeamTaskEventPayload.description</code></Heading>

```ts
readonly description: string;
```

<Heading as="h4" id="api-teamtaskeventpayload-id-propertysignature"><code>TeamTaskEventPayload.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-teamtaskeventpayload-state-propertysignature"><code>TeamTaskEventPayload.state</code></Heading>

```ts
readonly state: string;
```

<Heading as="h3" id="api-textpromptpart-interface"><code>TextPromptPart</code></Heading>

A text segment in a structured prompt.

```ts
export interface TextPromptPart
```

<Heading as="h4" id="api-textpromptpart-kind-propertysignature"><code>TextPromptPart.kind</code></Heading>

```ts
readonly kind: "text";
```

<Heading as="h4" id="api-textpromptpart-text-propertysignature"><code>TextPromptPart.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h3" id="api-titleattempteventpayload-interface"><code>TitleAttemptEventPayload</code></Heading>

One title-generation attempt projected by a `session.title` event.

```ts
export interface TitleAttemptEventPayload
```

<Heading as="h4" id="api-titleattempteventpayload-id-propertysignature"><code>TitleAttemptEventPayload.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-titleattempteventpayload-outcome-propertysignature"><code>TitleAttemptEventPayload.outcome</code></Heading>

```ts
readonly outcome: string;
```

<Heading as="h3" id="api-toolcalleventpayload-interface"><code>ToolCallEventPayload</code></Heading>

The payload of a `tool.call` event.

```ts
export interface ToolCallEventPayload
```

<Heading as="h4" id="api-toolcalleventpayload-args-propertysignature"><code>ToolCallEventPayload.args</code></Heading>

```ts
readonly args: string;
```

<Heading as="h4" id="api-toolcalleventpayload-id-propertysignature"><code>ToolCallEventPayload.id</code></Heading>

```ts
readonly id: string;
```

<Heading as="h4" id="api-toolcalleventpayload-name-propertysignature"><code>ToolCallEventPayload.name</code></Heading>

```ts
readonly name: string;
```

<Heading as="h3" id="api-toolresulteventpayload-interface"><code>ToolResultEventPayload</code></Heading>

The text, structured data, and content blocks from a `tool.result` event.

```ts
export interface ToolResultEventPayload
```

<Heading as="h4" id="api-toolresulteventpayload-blocks-propertysignature"><code>ToolResultEventPayload.blocks</code></Heading>

```ts
readonly blocks: readonly EventContentBlock[];
```

<Heading as="h4" id="api-toolresulteventpayload-callid-propertysignature"><code>ToolResultEventPayload.callId</code></Heading>

```ts
readonly callId: string;
```

<Heading as="h4" id="api-toolresulteventpayload-content-propertysignature"><code>ToolResultEventPayload.content</code></Heading>

```ts
readonly content: string;
```

<Heading as="h4" id="api-toolresulteventpayload-iserror-propertysignature"><code>ToolResultEventPayload.isError</code></Heading>

```ts
readonly isError: boolean;
```

<Heading as="h4" id="api-toolresulteventpayload-structuredcontent-propertysignature"><code>ToolResultEventPayload.structuredContent</code></Heading>

```ts
readonly structuredContent: string;
```

<Heading as="h3" id="api-turnendeventpayload-interface"><code>TurnEndEventPayload</code></Heading>

The payload of a `turn.end` event.

```ts
export interface TurnEndEventPayload
```

<Heading as="h4" id="api-turnendeventpayload-durationms-propertysignature"><code>TurnEndEventPayload.durationMs</code></Heading>

```ts
readonly durationMs: bigint;
```

<Heading as="h4" id="api-turnendeventpayload-usage-propertysignature"><code>TurnEndEventPayload.usage</code></Heading>

```ts
readonly usage?: EventUsage | undefined;
```

<Heading as="h3" id="api-unknowngrpcevent-interface"><code>UnknownGrpcEvent</code></Heading>

An unknown event received over a protobuf transport.

```ts
export interface UnknownGrpcEvent extends EventCommon
```

<Heading as="h4" id="api-unknowngrpcevent-kind-propertysignature"><code>UnknownGrpcEvent.kind</code></Heading>

```ts
readonly kind: "unknown";
```

<Heading as="h4" id="api-unknowngrpcevent-rawdata-propertysignature"><code>UnknownGrpcEvent.rawData</code></Heading>

The protobuf unknown fields, preserving their wire order and payload bytes.

```ts
readonly rawData: Uint8Array;
```

<Heading as="h4" id="api-unknowngrpcevent-transport-propertysignature"><code>UnknownGrpcEvent.transport</code></Heading>

```ts
readonly transport: "grpc";
```

<Heading as="h4" id="api-unknowngrpcevent-wirekind-propertysignature"><code>UnknownGrpcEvent.wireKind</code></Heading>

```ts
readonly wireKind: string;
```

<Heading as="h3" id="api-unknownhttpevent-interface"><code>UnknownHttpEvent</code></Heading>

An unknown event received over the HTTP JSON/SSE transport.

```ts
export interface UnknownHttpEvent extends EventCommon
```

<Heading as="h4" id="api-unknownhttpevent-kind-propertysignature"><code>UnknownHttpEvent.kind</code></Heading>

```ts
readonly kind: "unknown";
```

<Heading as="h4" id="api-unknownhttpevent-rawdata-propertysignature"><code>UnknownHttpEvent.rawData</code></Heading>

The exact parsed JSON object received in the SSE frame.

```ts
readonly rawData: JsonValue;
```

<Heading as="h4" id="api-unknownhttpevent-transport-propertysignature"><code>UnknownHttpEvent.transport</code></Heading>

```ts
readonly transport: "http";
```

<Heading as="h4" id="api-unknownhttpevent-wirekind-propertysignature"><code>UnknownHttpEvent.wireKind</code></Heading>

```ts
readonly wireKind: string;
```

<Heading as="h3" id="api-unknownwatchenvelope-interface"><code>UnknownWatchEnvelope</code></Heading>

A future watch phase preserved for forward compatibility.

```ts
export interface UnknownWatchEnvelope
```

<Heading as="h4" id="api-unknownwatchenvelope-cursor-propertysignature"><code>UnknownWatchEnvelope.cursor</code></Heading>

```ts
readonly cursor: SdkCursor;
```

<Heading as="h4" id="api-unknownwatchenvelope-event-propertysignature"><code>UnknownWatchEnvelope.event</code></Heading>

```ts
readonly event?: Event;
```

<Heading as="h4" id="api-unknownwatchenvelope-kind-propertysignature"><code>UnknownWatchEnvelope.kind</code></Heading>

```ts
readonly kind: "unknown";
```

<Heading as="h4" id="api-unknownwatchenvelope-phase-propertysignature"><code>UnknownWatchEnvelope.phase</code></Heading>

```ts
readonly phase: string;
```

<Heading as="h3" id="api-usermodel-interface"><code>UserModel</code></Heading>

Resolved user-model inspection operations.

```ts
export interface UserModel
```

Callable members: [`get()`](#api-usermodel-get-methodsignature)

<Heading as="h4" id="api-usermodel-get-methodsignature"><code>UserModel.get</code></Heading>

Gets the caller's bounded user-model index or one detail entry.

```ts
get(request: GetUserModelRequest, options?: RequestOptions): Promise<GetUserModelResponse>;
```

Parameters:

- `request` (`GetUserModelRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<GetUserModelResponse>`

<Heading as="h3" id="api-userprompteventpayload-interface"><code>UserPromptEventPayload</code></Heading>

The payload shared by `user_prompt` replay events.

```ts
export interface UserPromptEventPayload
```

<Heading as="h4" id="api-userprompteventpayload-parts-propertysignature"><code>UserPromptEventPayload.parts</code></Heading>

```ts
readonly parts: readonly EventContent[];
```

<Heading as="h4" id="api-userprompteventpayload-text-propertysignature"><code>UserPromptEventPayload.text</code></Heading>

```ts
readonly text: string;
```

<Heading as="h3" id="api-watchboundaryenvelope-interface"><code>WatchBoundaryEnvelope</code></Heading>

The single replay-to-live transition marker.

```ts
export interface WatchBoundaryEnvelope
```

<Heading as="h4" id="api-watchboundaryenvelope-cursor-propertysignature"><code>WatchBoundaryEnvelope.cursor</code></Heading>

```ts
readonly cursor: SdkCursor;
```

<Heading as="h4" id="api-watchboundaryenvelope-kind-propertysignature"><code>WatchBoundaryEnvelope.kind</code></Heading>

```ts
readonly kind: "boundary";
```

<Heading as="h4" id="api-watchboundaryenvelope-phase-propertysignature"><code>WatchBoundaryEnvelope.phase</code></Heading>

```ts
readonly phase: "live";
```

<Heading as="h3" id="api-watcheventenvelope-interface"><code>WatchEventEnvelope</code></Heading>

A replayed or live durable event.

```ts
export interface WatchEventEnvelope
```

<Heading as="h4" id="api-watcheventenvelope-cursor-propertysignature"><code>WatchEventEnvelope.cursor</code></Heading>

```ts
readonly cursor: SdkCursor;
```

<Heading as="h4" id="api-watcheventenvelope-event-propertysignature"><code>WatchEventEnvelope.event</code></Heading>

```ts
readonly event: Event;
```

<Heading as="h4" id="api-watcheventenvelope-kind-propertysignature"><code>WatchEventEnvelope.kind</code></Heading>

```ts
readonly kind: "event";
```

<Heading as="h4" id="api-watcheventenvelope-phase-propertysignature"><code>WatchEventEnvelope.phase</code></Heading>

```ts
readonly phase: "live" | "replay";
```

<Heading as="h3" id="api-watchgapenvelope-interface"><code>WatchGapEnvelope</code></Heading>

A known hole in durable delivery. It deliberately exposes no cursor.

```ts
export interface WatchGapEnvelope
```

<Heading as="h4" id="api-watchgapenvelope-kind-propertysignature"><code>WatchGapEnvelope.kind</code></Heading>

```ts
readonly kind: "gap";
```

<Heading as="h4" id="api-watchgapenvelope-phase-propertysignature"><code>WatchGapEnvelope.phase</code></Heading>

```ts
readonly phase: "gap";
```

<Heading as="h3" id="api-workspaceenrollment-interface"><code>WorkspaceEnrollment</code></Heading>

The immediate result of one whole-bundle workspace-enrollment operation.

```ts
export interface WorkspaceEnrollment
```

<Heading as="h4" id="api-workspaceenrollment-enrollmentid-propertysignature"><code>WorkspaceEnrollment.enrollmentId</code></Heading>

Opaque correlation for this enrollment attempt.

```ts
readonly enrollmentId: string;
```

<Heading as="h4" id="api-workspaceenrollment-presentationurl-propertysignature"><code>WorkspaceEnrollment.presentationUrl</code></Heading>

Ephemeral application-facing HTTP(S) launch URL, present only while pending.

```ts
readonly presentationUrl?: string;
```

<Heading as="h4" id="api-workspaceenrollment-requiredservices-propertysignature"><code>WorkspaceEnrollment.requiredServices</code></Heading>

Number of services that the whole bundle requires.

```ts
readonly requiredServices: number;
```

<Heading as="h4" id="api-workspaceenrollment-status-propertysignature"><code>WorkspaceEnrollment.status</code></Heading>

Current operation state, or `unknown` for a future wire value.

```ts
readonly status: WorkspaceEnrollmentStatus;
```

<Heading as="h3" id="api-worktrees-interface"><code>Worktrees</code></Heading>

Session-scoped worktree inventory operations.

```ts
export interface Worktrees
```

Callable members: [`list()`](#api-worktrees-list-methodsignature)

<Heading as="h4" id="api-worktrees-list-methodsignature"><code>Worktrees.list</code></Heading>

Lists worktrees eligible for a session fork or clear operation.

```ts
list(request: ListWorktreesRequest, options?: RequestOptions): Promise<ListWorktreesResponse>;
```

Parameters:

- `request` (`ListWorktreesRequest`)
- `options` (`RequestOptions`, optional)

Returns: `Promise<ListWorktreesResponse>`

## Type aliases

<Heading as="h3" id="api-agentevent-typealias"><code>AgentEvent</code></Heading>

The agent-lifecycle portion of the known event union.

```ts
export type AgentEvent = Exclude<KnownEvent, {
    readonly kind: `team.${string}`;
}>;
```

<Heading as="h3" id="api-connectionstatus-typealias"><code>ConnectionStatus</code></Heading>

The complete connection-state vocabulary exposed by the SDK.

```ts
export type ConnectionStatus = "connecting" | "online" | "reconnecting" | "offline" | "unauthorized" | "incompatible";
```

<Heading as="h3" id="api-connectionstatuslistener-typealias"><code>ConnectionStatusListener</code></Heading>

A callback notified whenever connection status changes.

```ts
export type ConnectionStatusListener = (status: ConnectionStatus) => void;
```

<Heading as="h3" id="api-connectoptions-typealias"><code>ConnectOptions</code></Heading>

Options accepted by the isomorphic connect() entry point.

```ts
export type ConnectOptions = HttpTransportOptions | InjectedTransportOptions;
```

<Heading as="h3" id="api-credentialprovider-typealias"><code>CredentialProvider</code></Heading>

Resolves request headers immediately before each SDK request.

```ts
export type CredentialProvider = () => HeadersInit | Promise<HeadersInit>;
```

<Heading as="h3" id="api-diagnosticfieldvalue-typealias"><code>DiagnosticFieldValue</code></Heading>

Values carried by the structured fields of an SDK-local diagnostic.

```ts
export type DiagnosticFieldValue = boolean | number | string | null;
```

<Heading as="h3" id="api-diagnosticlevel-typealias"><code>DiagnosticLevel</code></Heading>

Severity attached to one SDK-local diagnostic record.

```ts
export type DiagnosticLevel = "debug" | "error" | "info" | "warn";
```

<Heading as="h3" id="api-diagnosticssink-typealias"><code>DiagnosticsSink</code></Heading>

Optional client-level receiver for SDK-local diagnostics.

```ts
export type DiagnosticsSink = (record: DiagnosticRecord) => void;
```

<Heading as="h3" id="api-errororigin-typealias"><code>ErrorOrigin</code></Heading>

The request transport, or `local` when validation failed before transport selection.

```ts
export type ErrorOrigin = TransportKind | "local";
```

<Heading as="h3" id="api-event-typealias"><code>Event</code></Heading>

A decoded agent or team event.

```ts
export type Event = KnownEvent | UnknownEvent;
```

<Heading as="h3" id="api-eventof-typealias"><code>EventOf</code></Heading>

Selects one known event variant by its literal kind.

```ts
export type EventOf<Kind extends KnownEventKind> = Extract<KnownEvent, {
    readonly kind: Kind;
}>;
```

<Heading as="h3" id="api-knownevent-typealias"><code>KnownEvent</code></Heading>

All currently known agent and team event variants.

```ts
export type KnownEvent = {
    [Kind in KnownEventKind]: EventCommon & {
        readonly kind: Kind;
        readonly payload: EventPayloads[Kind];
    };
}[KnownEventKind];
```

<Heading as="h3" id="api-knowneventkind-typealias"><code>KnownEventKind</code></Heading>

A wire event kind currently understood by this SDK.

```ts
export type KnownEventKind = (typeof MECATL_EVENT_KINDS)[number];
```

<Heading as="h3" id="api-mcpauthorizationoperation-typealias"><code>McpAuthorizationOperation</code></Heading>

The one-shot server transition requested by an authorization flow.

```ts
export type McpAuthorizationOperation = "recheck" | "cancel";
```

<Heading as="h3" id="api-mcpauthorizationresult-typealias"><code>McpAuthorizationResult</code></Heading>

The authoritative result of one authorization recheck or cancellation. `pending` and `settled` have no continuation. `completed` carries one ordinary run result. `authorization_required` hands off a different authorization parked by the continuation.

```ts
export type McpAuthorizationResult = {
    readonly outcome: "pending";
    readonly status: "pending";
    readonly authorization: EventOf<"authorization.required">;
} | {
    readonly outcome: "settled";
    readonly status: Exclude<McpAuthorizationStatus, "pending">;
    readonly authorization: EventOf<"authorization.resolved">;
} | {
    readonly outcome: "completed";
    readonly status: Exclude<McpAuthorizationStatus, "pending">;
    readonly authorization: EventOf<"authorization.resolved">;
    readonly continuationRunId: string;
    readonly continuation: RunResult;
} | {
    readonly outcome: "authorization_required";
    readonly status: Exclude<McpAuthorizationStatus, "pending">;
    readonly authorization: EventOf<"authorization.resolved">;
    readonly continuationRunId: string;
    readonly nextAuthorization: EventOf<"authorization.required">;
};
```

<Heading as="h3" id="api-mcpauthorizationstatus-typealias"><code>McpAuthorizationStatus</code></Heading>

The closed authorization status vocabulary interpreted by the lifecycle helper. The server remains authoritative for every status. An unknown value is a protocol error in this lifecycle even though the general event union keeps raw status strings open.

```ts
export type McpAuthorizationStatus = "pending" | "granted" | "denied" | "cancelled" | "expired" | "interrupted" | "failed" | "closed";
```

<Heading as="h3" id="api-mcpconnectoravailability-typealias"><code>McpConnectorAvailability</code></Heading>

One broker-snapshot availability value.

```ts
export type McpConnectorAvailability = (typeof McpConnectorAvailability)[keyof typeof McpConnectorAvailability];
```

<Heading as="h3" id="api-mcpconnectorcataloguestate-typealias"><code>McpConnectorCatalogueState</code></Heading>

One connector catalogue-publication value.

```ts
export type McpConnectorCatalogueState = (typeof McpConnectorCatalogueState)[keyof typeof McpConnectorCatalogueState];
```

<Heading as="h3" id="api-mcpconnectorenrollmentstate-typealias"><code>McpConnectorEnrollmentState</code></Heading>

One aggregate connector-enrollment value.

```ts
export type McpConnectorEnrollmentState = (typeof McpConnectorEnrollmentState)[keyof typeof McpConnectorEnrollmentState];
```

<Heading as="h3" id="api-mecatlerrorcode-typealias"><code>MecatlErrorCode</code></Heading>

Every machine-readable error code exposed by the SDK.

```ts
export type MecatlErrorCode = ServerErrorCode | SDKErrorCode;
```

<Heading as="h3" id="api-permissionaskresponder-typealias"><code>PermissionAskResponder</code></Heading>

An optional automatic responder invoked for each permission ask on a run.

```ts
export type PermissionAskResponder = (ask: PermissionAskEventPayload, signal: AbortSignal) => PermissionVerdict | undefined | Promise<PermissionVerdict | undefined>;
```

<Heading as="h3" id="api-permissionverdict-typealias"><code>PermissionVerdict</code></Heading>

A server permission verdict accepted by run.resolveAsk().

```ts
export type PermissionVerdict = "allow_once" | "allow_always" | "deny";
```

<Heading as="h3" id="api-planapprovalresponder-typealias"><code>PlanApprovalResponder</code></Heading>

An automatic responder invoked only for a PresentPlan approval ask.

```ts
export type PlanApprovalResponder = (ask: PermissionAskEventPayload, signal: AbortSignal) => PlanApprovalVerdict | undefined | Promise<PlanApprovalVerdict | undefined>;
```

<Heading as="h3" id="api-planapprovalverdict-typealias"><code>PlanApprovalVerdict</code></Heading>

The plan-specific decisions accepted by session.resolvePlan() and onPlanApproval.

```ts
export type PlanApprovalVerdict = "approve" | "accept_edits" | "iterate";
```

<Heading as="h3" id="api-promptinput-typealias"><code>PromptInput</code></Heading>

A backwards-compatible string prompt or structured text/media parts.

```ts
export type PromptInput = string | readonly PromptPart[];
```

<Heading as="h3" id="api-promptpart-typealias"><code>PromptPart</code></Heading>

One segment accepted by Session.run().

```ts
export type PromptPart = TextPromptPart | ImagePromptPart | AudioPromptPart;
```

<Heading as="h3" id="api-promptvalidationreason-typealias"><code>PromptValidationReason</code></Heading>

Stable reasons reported by PromptValidationError.

```ts
export type PromptValidationReason = "capability" | "mime_type" | "prompt" | "size" | "source_xor" | "url";
```

<Heading as="h3" id="api-requestoptions-typealias"><code>RequestOptions</code></Heading>

Request controls shared by all thin typed namespaces.

```ts
export type RequestOptions = CallOptions;
```

<Heading as="h3" id="api-retrydisposition-typealias"><code>RetryDisposition</code></Heading>

Retry classification fields carried by model-retry and result payloads.

```ts
export type RetryDisposition = 0 | 1 | 2 | 3;
```

<Heading as="h3" id="api-runoutcome-typealias"><code>RunOutcome</code></Heading>

The closed set of completion and authorization-park outcomes from `Run.outcome()`.

```ts
export type RunOutcome = RunCompletedOutcome | RunAuthorizationRequiredOutcome;
```

<Heading as="h3" id="api-sdkcursor-typealias"><code>SdkCursor</code></Heading>

A serializable cursor issued by a durable SDK attachment.

```ts
export type SdkCursor = string;
```

<Heading as="h3" id="api-sdkerrorcode-typealias"><code>SDKErrorCode</code></Heading>

Error codes produced locally by the SDK.

```ts
export type SDKErrorCode = "authentication" | "cursor_scope" | "incompatible_server" | "invalid_prompt" | "invalid_state" | "no_runs" | "plan_continuation_start" | "protocol" | "readiness_timeout" | "spawn_failed" | "tool_registration" | "transport" | "unsupported_platform" | "unsupported_feature";
```

<Heading as="h3" id="api-servererrorcode-typealias"><code>ServerErrorCode</code></Heading>

Error codes returned by the Mecatl server, plus `unknown` for future codes.

```ts
export type ServerErrorCode = (typeof MECATL_ERROR_CODES)[number] | "unknown";
```

<Heading as="h3" id="api-serverfeature-typealias"><code>ServerFeature</code></Heading>

One known server feature identifier.

```ts
export type ServerFeature = (typeof ServerFeature)[keyof typeof ServerFeature];
```

<Heading as="h3" id="api-serverposture-typealias"><code>ServerPosture</code></Heading>

One known server posture value.

```ts
export type ServerPosture = (typeof ServerPosture)[keyof typeof ServerPosture];
```

<Heading as="h3" id="api-sessionmode-typealias"><code>SessionMode</code></Heading>

One SDK permission-mode value.

```ts
export type SessionMode = (typeof SessionMode)[keyof typeof SessionMode];
```

<Heading as="h3" id="api-streamprogress-typealias"><code>StreamProgress</code></Heading>

Stream-progress classification carried by model-retry and result payloads.

```ts
export type StreamProgress = 0 | 1 | 2 | 3 | 4;
```

<Heading as="h3" id="api-teamevent-typealias"><code>TeamEvent</code></Heading>

Team lifecycle events projected onto an agent run.

```ts
export type TeamEvent = Extract<KnownEvent, {
    readonly kind: `team.${string}`;
}>;
```

<Heading as="h3" id="api-teammemberrunevent-typealias"><code>TeamMemberRunEvent</code></Heading>

A run event tagged with the team member that produced it.

```ts
export type TeamMemberRunEvent = Event & {
    readonly member: string;
};
```

<Heading as="h3" id="api-teamrunevent-typealias"><code>TeamRunEvent</code></Heading>

A decoded direct-team stream frame.

```ts
export type TeamRunEvent = TeamMemberRunEvent | TeamOutcomeRunEvent;
```

<Heading as="h3" id="api-transportkind-typealias"><code>TransportKind</code></Heading>

Transport implementations supported by the SDK.

```ts
export type TransportKind = "grpc" | "http";
```

<Heading as="h3" id="api-unknownevent-typealias"><code>UnknownEvent</code></Heading>

A future wire event that this SDK does not yet type.

```ts
export type UnknownEvent = UnknownHttpEvent | UnknownGrpcEvent;
```

<Heading as="h3" id="api-watchenvelope-typealias"><code>WatchEnvelope</code></Heading>

One decoded durable-watch delivery envelope.

```ts
export type WatchEnvelope = WatchEventEnvelope | WatchBoundaryEnvelope | WatchGapEnvelope | UnknownWatchEnvelope;
```

<Heading as="h3" id="api-workspaceenrollmentstatus-typealias"><code>WorkspaceEnrollmentStatus</code></Heading>

One workspace-enrollment operation state.

```ts
export type WorkspaceEnrollmentStatus = (typeof WorkspaceEnrollmentStatus)[keyof typeof WorkspaceEnrollmentStatus];
```

## Variables

<Heading as="h3" id="api-max-media-part-bytes-variable"><code>MAX_MEDIA_PART_BYTES</code></Heading>

Maximum inline bytes in one image or audio part.

```ts
MAX_MEDIA_PART_BYTES: number
```

<Heading as="h3" id="api-max-prompt-media-bytes-variable"><code>MAX_PROMPT_MEDIA_BYTES</code></Heading>

Maximum inline media bytes in one prompt.

```ts
MAX_PROMPT_MEDIA_BYTES: number
```

<Heading as="h3" id="api-max-prompt-media-parts-variable"><code>MAX_PROMPT_MEDIA_PARTS</code></Heading>

Maximum image and audio parts in one prompt.

```ts
MAX_PROMPT_MEDIA_PARTS = 16
```

<Heading as="h3" id="api-mcpconnectoravailability-variable"><code>McpConnectorAvailability</code></Heading>

Availability of the process-local broker snapshot for one session.

```ts
McpConnectorAvailability: {
    readonly Available: "available";
    readonly Unavailable: "unavailable";
    readonly Unknown: "unknown";
}
```

<Heading as="h3" id="api-mcpconnectorcataloguestate-variable"><code>McpConnectorCatalogueState</code></Heading>

Broker-local catalogue publication state for one connector.

```ts
McpConnectorCatalogueState: {
    readonly Hidden: "hidden";
    readonly Declared: "declared";
    readonly Discovered: "discovered";
    readonly Unknown: "unknown";
}
```

<Heading as="h3" id="api-mcpconnectorenrollmentstate-variable"><code>McpConnectorEnrollmentState</code></Heading>

Aggregate workspace-enrollment state reported by connector inventory.

```ts
McpConnectorEnrollmentState: {
    readonly NotRequired: "not_required";
    readonly NotStarted: "not_started";
    readonly Pending: "pending";
    readonly Completed: "completed";
    readonly Unknown: "unknown";
}
```

<Heading as="h3" id="api-mecatl-attach-filtered-kinds-variable"><code>MECATL_ATTACH_FILTERED_KINDS</code></Heading>

Event kinds omitted by high-level attachment views unless requested.

```ts
MECATL_ATTACH_FILTERED_KINDS: readonly ["approval", "compaction.archive", "network.attempt", "request.manifest", "user_prompt"]
```

<Heading as="h3" id="api-mecatl-error-codes-variable"><code>MECATL_ERROR_CODES</code></Heading>

Stable server error codes, kept in parity with the Go registry.

```ts
MECATL_ERROR_CODES: readonly ["activity_gap", "ask_not_pending", "attempt_live_claim_conflict", "attempt_terminal_conflict", "attempt_version_conflict", "child_not_found", "cleanup_backend", "cleanup_plan_stale", "cleanup_unsupported", "client_mcp_unreachable", "client_mcp_unsupported", "conflict", "context_window_unavailable", "cursor_expired", "cursor_malformed", "draining", "dream_apply_failed", "dream_capacity", "dream_conflict", "dream_deadline", "dream_generate_failed", "dream_in_progress", "dream_not_found", "dream_request_failed", "dream_terminal_conflict", "dream_unavailable", "failed_precondition", "failed_step_retry_ineligible", "fire_now_overlap", "internal", "invalid_argument", "learning_unavailable", "management_unauthorized", "mcp_connector_unavailable", "migration_backend", "migration_conflict", "migration_unsupported", "mcp_authorization_pending", "no_active_run", "no_event_log", "no_mcp_provider", "no_schedule_store", "not_awaiting_plan", "not_found", "placement_binding_invalid", "placement_changed", "placement_selector_invalid", "placement_selector_not_found", "placement_selector_stale", "placement_unavailable", "plan_resolution_required", "proposal_conflict", "reflection_cancelled", "reflection_deadline", "reflection_failed", "reflection_queue_full", "request_too_large", "resource_exhausted", "schedule_disabled", "schedule_exhausted", "schedule_not_found", "schedule_not_leader", "schedule_unsupported", "scheduler_not_running", "session_delete_unsupported", "session_leased_elsewhere", "session_metadata_cursor_restart", "session_metadata_paging_unsupported", "session_not_found", "stale_run_control", "storage_health_backend", "team_not_found", "team_not_running", "team_running", "teams_disabled", "too_many_session_engines", "too_many_teams", "unauthenticated", "unimplemented", "watch_capacity", "watch_lagging", "watch_unsupported"]
```

<Heading as="h3" id="api-mecatl-event-kinds-variable"><code>MECATL_EVENT_KINDS</code></Heading>

Stable event kinds, kept in parity with the Go server vocabulary.

```ts
MECATL_EVENT_KINDS: readonly ["approval", "authorization.required", "authorization.resolved", "compaction", "compaction.archive", "hook", "message.delta", "model.retry", "network.attempt", "no_progress", "parallel.branch", "parallel.end", "parallel.start", "permission.ask", "permission.retract", "provider.route", "reasoning.delta", "recover_notice", "request.manifest", "result", "schedule.failed", "schedule.fired", "schedule.skipped", "session.init", "session.title", "steer", "steer.outcome", "subagent.end", "subagent.start", "subagent.tool", "team.end", "team.findings", "team.member", "team.start", "team.tasks", "tool.call", "tool.progress", "tool.result", "turn.end", "turn.start", "user_prompt"]
```

<Heading as="h3" id="api-mecatl-watch-phases-variable"><code>MECATL_WATCH_PHASES</code></Heading>

Watch phases this SDK understands.

```ts
MECATL_WATCH_PHASES: readonly ["gap", "live", "replay"]
```

<Heading as="h3" id="api-serverfeature-variable"><code>ServerFeature</code></Heading>

Known server feature identifiers. Unknown identifiers remain observable.

```ts
ServerFeature: {
    readonly HttpSteer: "http_steer";
    readonly McpServersOnCreate: "mcp_servers_on_create";
    readonly PromptFreeControls: "prompt_free_controls";
    readonly ServerInfo: "server_info";
    readonly SessionActivityInventory: "session_activity_inventory";
    readonly WatchSessionEvents: "watch_session_events";
}
```

<Heading as="h3" id="api-serverposture-variable"><code>ServerPosture</code></Heading>

Known server posture values. Unknown capability values remain observable.

```ts
ServerPosture: {
    readonly Strict: "strict";
    readonly Trusted: "trusted";
    readonly Auto: "auto";
    readonly Yolo: "yolo";
}
```

<Heading as="h3" id="api-session-id-header-name-variable"><code>SESSION_ID_HEADER_NAME</code></Heading>

Canonical routing hint for session-bound Mecatl requests. It grants no authority.

```ts
SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID"
```

<Heading as="h3" id="api-sessionmode-variable"><code>SessionMode</code></Heading>

SDK permission modes accepted by session creation and mutation operations.

```ts
SessionMode: {
    readonly Unspecified: 0;
    readonly Default: 1;
    readonly Plan: 2;
    readonly AcceptEdits: 3;
}
```

<Heading as="h3" id="api-supported-api-major-variable"><code>SUPPORTED_API_MAJOR</code></Heading>

The API major implemented by this SDK.

```ts
SUPPORTED_API_MAJOR = 1
```

<Heading as="h3" id="api-watch-session-events-feature-variable"><code>WATCH_SESSION_EVENTS_FEATURE</code></Heading>

Known watch-session-events feature identifier.

```ts
WATCH_SESSION_EVENTS_FEATURE: "watch_session_events"
```

<Heading as="h3" id="api-workspaceenrollmentstatus-variable"><code>WorkspaceEnrollmentStatus</code></Heading>

State returned by a whole-bundle workspace-enrollment operation.

```ts
WorkspaceEnrollmentStatus: {
    readonly Pending: "pending";
    readonly Connected: "connected";
    readonly Denied: "denied";
    readonly Cancelled: "cancelled";
    readonly Expired: "expired";
    readonly Failed: "failed";
    readonly Unknown: "unknown";
}
```
