// SPDX-License-Identifier: Apache-2.0

import type {
  DiscussWriterResponse,
  ObserveWriterResponse,
} from "@mecatl-studio/contracts/generated";

type Document = { revision: number; content: string };
export type Entry = { role: "user" | "assistant"; text: string };
export type Observation = {
  id: string;
  revision: number;
  text: string;
  quote?: string;
  quotes?: string[];
  status: "open" | "addressed" | "not-relevant";
  decision?: string;
  discussion: Entry[];
  timestamp: number;
};
type Context = {
  document: Document;
  model?: { id: string; providerId: string };
  checkpoint?: Document;
  brief?: string;
  observations: Array<
    Pick<Observation, "revision" | "status" | "text"> & { decision?: string; selected?: boolean }
  >;
  decisions?: Array<{ text: string; decision: string }>;
  discussion: Entry[];
};
export type WriterTransport = {
  observe: (body: Context, signal: AbortSignal) => Promise<ObserveWriterResponse>;
  discuss: (
    body: Context & { message: string },
    signal: AbortSignal,
  ) => Promise<DiscussWriterResponse>;
};

function similar(a: string, b: string): boolean {
  const tokens = (text: string) => new Set(text.toLowerCase().match(/[\p{L}\p{N}]+/gu) ?? []);
  const left = tokens(a);
  const right = tokens(b);
  const intersection = [...left].filter((token) => right.has(token)).length;
  return (2 * intersection) / (left.size + right.size || 1) >= 0.8;
}

export type WriterSnapshot = {
  document: Document;
  brief: string;
  observations: Observation[];
  generalDiscussion: Entry[];
};

export function locateQuote(
  content: string,
  quote: string,
): { from: number; to: number } | undefined {
  if (!quote) return undefined;
  const from = content.indexOf(quote);
  if (from < 0 || content.indexOf(quote, from + 1) >= 0) return undefined;
  return { from, to: from + quote.length };
}

export class WriterCore {
  document: Document = { revision: 0, content: "" };
  checkpoint: Document = { revision: 0, content: "" };
  observations: Observation[] = [];
  generalDiscussion: Entry[] = [];
  brief = "";
  selectedId?: string;
  paused = false;
  available = true;
  model?: { id: string; providerId: string };
  lastChecked?: { revision: number; silent: boolean };
  get discussion(): Entry[] {
    return (
      this.observations.find((item) => item.id === this.selectedId)?.discussion ??
      this.generalDiscussion
    );
  }
  get status(): string {
    if (!this.available)
      return "Writer is unavailable. Requests are suspended; your draft remains in this tab.";
    if (this.busy === "discuss") return "Discussing…";
    if (this.busy === "observe") return `Analyzing revision ${this.checkingRevision}…`;
    if (this.paused)
      return this.checkedBriefGeneration !== this.briefGeneration
        ? "Automatic checks paused · brief not checked"
        : "Automatic checks paused";
    if (this.error) return "Analysis needs manual retry";
    if (
      this.checkedBriefGeneration !== this.briefGeneration &&
      !this.automaticEligible &&
      this.lastChecked
    )
      return "Brief changed · read this now or keep writing";
    if (this.document.content.length > 100_000) return "Document too long for analysis";
    if (this.timer)
      return Date.now() < this.nextAutomaticAt
        ? "Waiting for the next quiet check"
        : "Waiting for a pause in typing";
    if (this.document.content && !this.automaticEligible && !this.lastChecked)
      return "Draft restored · read this now or keep writing";
    return this.lastChecked
      ? `Last checked revision ${this.lastChecked.revision}${this.lastChecked.silent ? " · no observation" : ""}`
      : "Start writing to receive occasional observations";
  }
  private checkingRevision?: number;
  private selectionGeneration = 0;
  busy: "observe" | "discuss" | undefined;
  error = "";
  private timer?: ReturnType<typeof setTimeout>;
  private controller?: AbortController;
  private inFlight?: Promise<void>;
  private nextAutomaticAt = 0;
  private lastEdit = 0;
  private firstUnsentEdit = 0;
  private disposed = false;
  private generation = 0;
  private briefGeneration = 0;
  private checkedBriefGeneration = 0;
  private automaticEligible = true;
  private recent: string[] = [];

  constructor(
    private transport: WriterTransport,
    private changed: () => void,
    private idleMs = 1500,
    private intervalMs = 30_000,
    private maxWaitMs = 45_000,
  ) {}

  private notify() {
    if (!this.disposed) this.changed();
  }

  edit(content: string) {
    if (this.disposed || content === this.document.content) return;
    if (!this.firstUnsentEdit) this.firstUnsentEdit = Date.now();
    this.document = { revision: this.document.revision + 1, content };
    this.automaticEligible = true;
    this.lastEdit = Date.now();
    this.schedule();
    this.notify();
  }

  setBrief(brief: string) {
    if (brief.length > 2_000 || brief === this.brief) return false;
    this.brief = brief;
    this.briefGeneration++;
    this.automaticEligible = false;
    // Old-brief results cannot advance the checkpoint or surface as current.
    if (this.busy === "observe") this.controller?.abort();
    this.schedule();
    this.notify();
    return true;
  }

  snapshot(): WriterSnapshot {
    return {
      document: { ...this.document },
      brief: this.brief,
      observations: this.observations.map((item) => ({
        ...item,
        discussion: [...item.discussion],
        quotes: item.quotes && [...item.quotes],
      })),
      generalDiscussion: [...this.generalDiscussion],
    };
  }

  restore(snapshot: WriterSnapshot) {
    this.generation++;
    this.controller?.abort();
    this.document = { ...snapshot.document };
    this.brief = snapshot.brief;
    this.briefGeneration++;
    this.checkedBriefGeneration = this.briefGeneration;
    this.observations = snapshot.observations.map((item) => ({
      ...item,
      discussion: [...item.discussion],
    }));
    this.generalDiscussion = [...snapshot.generalDiscussion];
    this.recent = this.observations.slice(-20).map((item) => item.text);
    this.checkpoint = { revision: 0, content: "" };
    this.automaticEligible = false;
    this.firstUnsentEdit = Date.now();
    this.lastEdit = Date.now();
    this.schedule();
    this.notify();
  }

  private context(): Context {
    const recent = this.observations.slice(-12);
    const selected = this.observations.find((item) => item.id === this.selectedId);
    if (selected && !recent.includes(selected)) recent.splice(0, 1, selected);
    const decisions = this.observations
      .filter((item): item is Observation & { decision: string } => !!item.decision)
      .map(({ text, decision }) => ({ text, decision }));
    return {
      document: { ...this.document },
      ...(this.model ? { model: this.model } : {}),
      ...(this.brief ? { brief: this.brief } : {}),
      ...(this.checkedBriefGeneration === this.briefGeneration
        ? { checkpoint: { ...this.checkpoint } }
        : {}),
      observations: recent.map((item) => ({
        revision: item.revision,
        status: item.status,
        text: item.text,
        ...(item.decision ? { decision: item.decision } : {}),
        ...(item.id === this.selectedId ? { selected: true } : {}),
      })),
      ...(decisions.length ? { decisions } : {}),
      discussion: this.discussion.slice(-12),
    };
  }

  setModel(model?: { id: string; providerId: string }) {
    this.model = model;
    this.notify();
  }

  private schedule() {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (
      this.disposed ||
      !this.available ||
      this.paused ||
      !this.automaticEligible ||
      this.error ||
      this.busy ||
      this.document.content.length > 100_000 ||
      this.document.content === this.checkpoint.content
    )
      return;
    const delta = Math.abs(this.document.content.length - this.checkpoint.content.length);
    const meaningful =
      delta >= 20 ||
      /[.!?\n]$/.test(this.document.content) ||
      (this.checkpoint.content === "" && this.document.content.length >= 5);
    // A small edit eventually gets checked, even if the author keeps typing.
    const due = this.firstUnsentEdit + this.maxWaitMs;
    this.timer = setTimeout(
      () => {
        this.timer = undefined;
        void this.observe(false);
      },
      Math.max(
        0,
        this.nextAutomaticAt - Date.now(),
        (meaningful ? Math.min(this.lastEdit + this.idleMs, due) : due) - Date.now(),
      ),
    );
  }

  async readNow(): Promise<void> {
    if (this.disposed || !this.available || this.document.content.length > 100_000) return;
    clearTimeout(this.timer);
    const generation = ++this.generation;
    this.controller?.abort();
    if (this.inFlight) await this.inFlight;
    if (generation !== this.generation || this.disposed || !this.available) return;
    await this.observe(true);
  }

  private async observe(explicit: boolean) {
    if (this.busy || !this.available || this.disposed || (!explicit && (this.paused || this.error)))
      return;
    const context = this.context();
    const generation = this.generation;
    const briefGeneration = this.briefGeneration;
    const controller = new AbortController();
    this.controller = controller;
    this.busy = "observe";
    this.checkingRevision = context.document.revision;
    this.error = "";
    this.notify();
    const task = (async () => {
      try {
        const result = await this.transport.observe(
          explicit ? { ...context, checkpoint: undefined } : context,
          controller.signal,
        );
        if (
          this.disposed ||
          controller.signal.aborted ||
          generation !== this.generation ||
          briefGeneration !== this.briefGeneration
        )
          return;
        this.checkpoint = context.document;
        this.checkedBriefGeneration = briefGeneration;
        this.lastChecked = {
          revision: context.document.revision,
          silent: result.status === "silent",
        };
        this.nextAutomaticAt = Date.now() + this.intervalMs;
        if (
          result.status === "observe" &&
          !this.recent.some((text) => similar(text, result.text))
        ) {
          this.observations.push({
            id: `observation-${context.document.revision}-${Date.now()}`,
            revision: context.document.revision,
            text: result.text,
            ...(result.quote ? { quote: result.quote } : {}),
            ...(result.quotes ? { quotes: result.quotes } : {}),
            status: "open",
            discussion: [],
            timestamp: Date.now(),
          });
          this.recent = [...this.recent, result.text].slice(-20);
        }
        this.firstUnsentEdit = this.document.content === this.checkpoint.content ? 0 : Date.now();
      } catch {
        if (
          !this.disposed &&
          !controller.signal.aborted &&
          generation === this.generation &&
          briefGeneration === this.briefGeneration
        )
          this.error = "Analysis failed. Retry when ready.";
      } finally {
        this.busy = undefined;
        this.controller = undefined;
        this.inFlight = undefined;
        this.schedule();
        this.notify();
      }
    })();
    this.inFlight = task;
    await task;
  }

  async discuss(message: string): Promise<boolean> {
    if (this.disposed || !this.available || this.busy === "discuss") return false;
    const selectedId = this.selectedId;
    const selectionGeneration = this.selectionGeneration;
    if (!message.trim() || message.length > 2000 || this.document.content.length > 100_000) {
      this.error = !message.trim()
        ? "Enter a message before sending."
        : message.length > 2000
          ? "Message exceeds 2,000 characters."
          : "Document exceeds 100,000 characters.";
      this.notify();
      return false;
    }
    clearTimeout(this.timer);
    const generation = ++this.generation;
    this.controller?.abort();
    if (this.inFlight) await this.inFlight;
    if (
      this.disposed ||
      !this.available ||
      generation !== this.generation ||
      selectionGeneration !== this.selectionGeneration
    )
      return false;
    const context = this.context();
    const controller = new AbortController();
    this.controller = controller;
    this.busy = "discuss";
    this.error = "";
    this.notify();
    try {
      const response = await this.transport.discuss({ ...context, message }, controller.signal);
      if (
        this.disposed ||
        controller.signal.aborted ||
        !this.available ||
        generation !== this.generation ||
        selectionGeneration !== this.selectionGeneration
      )
        return false;
      const entries = [
        ...this.discussion,
        { role: "user" as const, text: message },
        { role: "assistant" as const, text: response.text },
      ];
      const selected = this.observations.find((item) => item.id === selectedId);
      if (selected) selected.discussion = entries;
      else this.generalDiscussion = entries;
      return true;
    } catch {
      if (
        !this.disposed &&
        !controller.signal.aborted &&
        generation === this.generation &&
        selectionGeneration === this.selectionGeneration
      )
        this.error = "Discussion failed. Try sending again.";
      return false;
    } finally {
      this.busy = undefined;
      this.controller = undefined;
      this.schedule();
      this.notify();
    }
  }

  select(id?: string) {
    if (id && !this.observations.some((item) => item.id === id)) return;
    if (this.selectedId !== id) {
      this.selectionGeneration++;
      if (this.busy === "discuss") this.controller?.abort();
    }
    this.selectedId = id;
    this.notify();
  }

  setObservationStatus(id: string, status: Observation["status"]) {
    const item = this.observations.find((observation) => observation.id === id);
    if (item) item.status = status;
    this.notify();
  }

  confirmDecision(id: string, decision: string) {
    if (decision.length > 500) return false;
    const item = this.observations.find((observation) => observation.id === id);
    if (!item) return false;
    if (
      decision.trim() &&
      !item.decision &&
      this.observations.filter((observation) => observation.decision).length >= 100
    ) {
      this.error = "Decision limit reached (100). Clear a decision before confirming another.";
      this.notify();
      return false;
    }
    item.decision = decision.trim() || undefined;
    if (this.error.startsWith("Decision limit reached")) this.error = "";
    this.notify();
    return true;
  }

  setPaused(paused: boolean) {
    this.paused = paused;
    clearTimeout(this.timer);
    if (paused && this.busy === "observe") this.controller?.abort();
    this.schedule();
    this.notify();
  }

  setAvailable(available: boolean) {
    if (this.available === available) return;
    this.available = available;
    if (!available) {
      this.generation++;
      this.controller?.abort();
    }
    this.schedule();
    this.notify();
  }

  async retry() {
    await this.readNow();
  }

  revive() {
    this.disposed = false;
    this.schedule();
  }

  dispose() {
    this.disposed = true;
    this.generation++;
    clearTimeout(this.timer);
    this.controller?.abort();
  }
}
