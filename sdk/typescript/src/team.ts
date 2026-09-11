import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";

import { InvalidStateError, ProtocolError, type TransportKind } from "./errors.js";
import { decodeEvent, type Event } from "./events.js";
import type {
  CancelTeammateResponse,
  CleanupTeamResponse,
  CreateTeamResponse,
  ListTeamResponse,
  TeamEvent as ProtoTeamEvent,
  SendTeammateMessageResponse,
  SpawnTeammateResponse,
  TeamMember,
  TeamOutcome,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RequestOptions } from "./namespaces-core.js";
import { RPC_CATALOG } from "./rpc-catalog.js";

/** One initial or incrementally spawned team member. @public */
export interface TeamMemberOptions {
  /** Unique handle used to address this member. */
  name: string;
  /** Agent-definition name adopted by this member. */
  agentType?: string;
  /** Marks this member as the team coordinator. */
  lead?: boolean;
  /** Requests an isolated workspace with mutating tools. */
  mutating?: boolean;
  /** First-turn prompt for this member. */
  initialPrompt?: string;
}

/** Options used to create a server-owned team. @public */
export interface CreateTeamOptions {
  /** Session that owns the team. */
  sessionId: string;
  /** Optional human-readable team label. */
  name?: string;
  /** Initial members enrolled atomically. */
  members?: readonly TeamMemberOptions[];
  /** Objective supplied to the coordinating member. */
  goal?: string;
  /**
   * Optional team-wide token limit. The daemon applies the lower of this value
   * and its configured cap. Omit it to use the daemon's cap.
   */
  maxTeamTokens?: number;
}

/** One operator message sent to a team member. @public */
export interface TeamMessageOptions {
  /** Recipient member handle. */
  to: string;
  /** Sender label recorded with the message. */
  from?: string;
  /** Message body delivered to the member. */
  body: string;
}

/** A run event tagged with the team member that produced it. @public */
export type TeamMemberRunEvent = Event & {
  readonly member: string;
};

/** The one terminal outcome from a direct team run. @public */
export interface TeamOutcomeRunEvent {
  readonly kind: "outcome";
  readonly outcome: TeamOutcome;
}

/** A decoded direct-team stream frame. @public */
export type TeamRunEvent = TeamMemberRunEvent | TeamOutcomeRunEvent;

/** One single-consumption direct team run. @public */
export interface TeamRun extends AsyncIterable<TeamRunEvent> {
  readonly teamId: string;
  /** Drains the stream and returns its one required terminal outcome. */
  result(): Promise<TeamOutcome>;
}

/** A handle for direct team operations. @public */
export interface Team {
  readonly id: string;
  /** The typed initial roster returned atomically by CreateTeam. This is not a live view. */
  readonly initialMembers: readonly TeamMember[];
  /** Adds one member to the team. */
  spawn(member: TeamMemberOptions, options?: RequestOptions): Promise<SpawnTeammateResponse>;
  /** Sends a message to a team member. */
  message(
    message: TeamMessageOptions,
    options?: RequestOptions,
  ): Promise<SendTeammateMessageResponse>;
  /** Cancels one team member. */
  cancel(member: string, options?: RequestOptions): Promise<CancelTeammateResponse>;
  /** Starts a single-consumption team run. */
  run(options?: RequestOptions): TeamRun;
  /** Returns the current team roster and state. */
  list(options?: RequestOptions): Promise<ListTeamResponse>;
  /** Permanently removes the server-owned team. */
  cleanup(options?: RequestOptions): Promise<CleanupTeamResponse>;
}

/** Direct team creation operations exposed by a Client. @public */
export interface Teams {
  /** Creates a server-owned team bound to a session. */
  create(request: CreateTeamOptions, options?: RequestOptions): Promise<Team>;
}

interface TeamOperations {
  assertOpen(): void;
  readonly transportKind: TransportKind;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: RequestOptions,
  ): AsyncIterable<MessageShape<O>>;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: RequestOptions,
  ): Promise<MessageShape<O>>;
}

type ConsumptionMode = "events" | "result";

class TeamRunImpl implements TeamRun {
  readonly teamId: string;

  readonly #events: AsyncIterator<ProtoTeamEvent>;
  readonly #operations: TeamOperations;
  #consumption: ConsumptionMode | undefined;
  #outcome: TeamOutcome | undefined;

  constructor(teamId: string, events: AsyncIterable<ProtoTeamEvent>, operations: TeamOperations) {
    this.teamId = teamId;
    this.#events = events[Symbol.asyncIterator]();
    this.#operations = operations;
  }

  [Symbol.asyncIterator](): AsyncIterator<TeamRunEvent> {
    this.#claim("events");
    return {
      next: () => this.#next(),
      return: async () => {
        await this.#events.return?.();
        return { done: true, value: undefined };
      },
    };
  }

  async result(): Promise<TeamOutcome> {
    this.#claim("result");
    for (;;) {
      const next = await this.#next();
      if (next.done) break;
    }
    const outcome = this.#outcome;
    if (outcome === undefined) {
      throw this.#protocol("The RunTeam stream ended without a terminal outcome");
    }
    return outcome;
  }

  #claim(mode: ConsumptionMode): void {
    this.#operations.assertOpen();
    if (this.#consumption !== undefined) {
      throw new InvalidStateError(
        `Team run events are already being consumed through ${this.#consumption}`,
        { transport: this.#operations.transportKind },
      );
    }
    this.#consumption = mode;
  }

  async #next(): Promise<IteratorResult<TeamRunEvent>> {
    this.#operations.assertOpen();
    const next = await this.#events.next();
    if (next.done) {
      if (this.#outcome === undefined) {
        throw this.#protocol("The RunTeam stream ended without a terminal outcome");
      }
      return { done: true, value: undefined };
    }

    const frame = next.value;
    if (frame.outcome !== undefined) {
      if (this.#outcome !== undefined) {
        throw this.#protocol("The RunTeam stream returned more than one terminal outcome");
      }
      if (frame.event !== undefined || frame.member !== "") {
        throw this.#protocol("The RunTeam terminal outcome frame was not terminal-only");
      }
      this.#outcome = frame.outcome;
      return { done: false, value: { kind: "outcome", outcome: frame.outcome } };
    }

    if (this.#outcome !== undefined) {
      throw this.#protocol("The RunTeam stream returned an event after its terminal outcome");
    }
    if (frame.event === undefined || frame.member === "") {
      throw this.#protocol("The RunTeam stream returned a member frame without member and event");
    }
    return {
      done: false,
      value: {
        ...decodeEvent(frame.event, this.#operations.transportKind),
        member: frame.member,
      },
    };
  }

  #protocol(message: string): ProtocolError {
    return new ProtocolError(message, { transport: this.#operations.transportKind });
  }
}

class TeamImpl implements Team {
  readonly id: string;
  readonly initialMembers: readonly TeamMember[];

  readonly #operations: TeamOperations;

  constructor(id: string, initialMembers: readonly TeamMember[], operations: TeamOperations) {
    this.id = id;
    this.initialMembers = initialMembers;
    this.#operations = operations;
  }

  spawn(member: TeamMemberOptions, options?: RequestOptions): Promise<SpawnTeammateResponse> {
    return this.#operations.unary(
      RPC_CATALOG["HarnessService.SpawnTeammate"].grpc.descriptor,
      { ...member, teamId: this.id },
      options,
    );
  }

  message(
    message: TeamMessageOptions,
    options?: RequestOptions,
  ): Promise<SendTeammateMessageResponse> {
    return this.#operations.unary(
      RPC_CATALOG["HarnessService.SendTeammateMessage"].grpc.descriptor,
      { ...message, teamId: this.id },
      options,
    );
  }

  cancel(member: string, options?: RequestOptions): Promise<CancelTeammateResponse> {
    return this.#operations.unary(
      RPC_CATALOG["HarnessService.CancelTeammate"].grpc.descriptor,
      { member, teamId: this.id },
      options,
    );
  }

  run(options?: RequestOptions): TeamRun {
    this.#operations.assertOpen();
    const events = this.#operations.stream(
      RPC_CATALOG["HarnessService.RunTeam"].grpc.descriptor,
      singleValue({ teamId: this.id }),
      options,
    );
    return new TeamRunImpl(this.id, events, this.#operations);
  }

  list(options?: RequestOptions): Promise<ListTeamResponse> {
    return this.#operations.unary(
      RPC_CATALOG["HarnessService.ListTeam"].grpc.descriptor,
      { teamId: this.id },
      options,
    );
  }

  cleanup(options?: RequestOptions): Promise<CleanupTeamResponse> {
    return this.#operations.unary(
      RPC_CATALOG["HarnessService.CleanupTeam"].grpc.descriptor,
      { teamId: this.id },
      options,
    );
  }
}

/** Internal construction seam used by ClientImpl. */
export function createTeams(operations: TeamOperations): Teams {
  return {
    create: async (request, options) => {
      const { members, ...requestWithoutMembers } = request;
      const response: CreateTeamResponse = await operations.unary(
        RPC_CATALOG["HarnessService.CreateTeam"].grpc.descriptor,
        members === undefined
          ? requestWithoutMembers
          : { ...requestWithoutMembers, members: members.map((member) => ({ ...member })) },
        options,
      );
      if (response.teamId === "") {
        throw new ProtocolError("CreateTeam returned no team id", {
          transport: operations.transportKind,
        });
      }
      return new TeamImpl(response.teamId, response.members, operations);
    },
  };
}

async function* singleValue<T>(value: T): AsyncGenerator<T> {
  yield value;
}
