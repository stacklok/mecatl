// SPDX-License-Identifier: Apache-2.0

import type {
  DiscussWriterResponse,
  ObserveWriterResponse,
} from "@mecatl-studio/contracts/generated";

type Document = { revision: number; content: string };
export type Observation = {
  id: string;
  revision: number;
  text: string;
  status: "active" | "dismissed" | "discussed";
  timestamp: number;
};
type Entry = { role: "user" | "assistant"; text: string };
type Context = {
  document: Document;
  model?: { id: string; providerId: string };
  checkpoint?: Document;
  observations: Array<Pick<Observation, "revision" | "status" | "text"> & { selected?: boolean }>;
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

export class WriterCore {
  document: Document = { revision: 0, content: "" };
  checkpoint: Document = { revision: 0, content: "" };
  observations: Observation[] = [];
  discussion: Entry[] = [];
  selectedId?: string;
  paused = false;
  available = true;
  model?: { id: string; providerId: string };
  lastChecked?: { revision: number; silent: boolean };
  get status(): string {
    if (!this.available)
      return "Writer is unavailable. Requests are suspended; your draft remains in this tab.";
    if (this.busy === "discuss") return "Discussing…";
    if (this.paused) return "Observations paused";
    if (this.busy === "observe") return `Analyzing revision ${this.checkingRevision}…`;
    if (this.error) return "Analysis needs manual retry";
    if (this.document.content.length > 100_000) return "Document too long for analysis";
    if (this.timer) {
      return Date.now() < this.cooldownUntil
        ? "Cooling down before the next analysis"
        : "Waiting for a pause in typing";
    }
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
  private cooldownUntil = 0;
  private lastEdit = 0;
  private disposed = false;
  private generation = 0;
  private recent: string[] = [];

  constructor(
    private transport: WriterTransport,
    private changed: () => void,
    private idleMs = 1500,
    private cooldownMs = 60_000,
  ) {}

  private notify() {
    if (!this.disposed) this.changed();
  }

  edit(content: string) {
    if (this.disposed || content === this.document.content) return;
    this.document = { revision: this.document.revision + 1, content };
    this.lastEdit = Date.now();
    this.schedule();
    this.notify();
  }

  private context(): Context {
    const recent = this.observations.slice(-12);
    const selected = this.observations.find((item) => item.id === this.selectedId);
    if (selected && !recent.includes(selected)) recent.splice(0, 1, selected);
    return {
      document: { ...this.document },
      ...(this.model ? { model: this.model } : {}),
      checkpoint: { ...this.checkpoint },
      observations: recent.map((observation) => ({
        revision: observation.revision,
        status: observation.status,
        text: observation.text,
        ...(observation.id === this.selectedId ? { selected: true } : {}),
      })),
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
      this.error ||
      this.busy ||
      this.document.content.length > 100_000 ||
      this.document.content === this.checkpoint.content
    )
      return;
    this.timer = setTimeout(
      () => {
        this.timer = undefined;
        void this.observe();
      },
      Math.max(0, this.lastEdit + this.idleMs - Date.now(), this.cooldownUntil - Date.now()),
    );
  }

  private async observe() {
    if (this.busy || !this.available || this.paused || this.disposed || this.error) return;
    const context = this.context();
    const generation = this.generation;
    const controller = new AbortController();
    this.controller = controller;
    this.busy = "observe";
    this.checkingRevision = context.document.revision;
    this.notify();
    const task = (async () => {
      try {
        const result = await this.transport.observe(context, controller.signal);
        if (this.disposed || controller.signal.aborted || generation !== this.generation) return;
        this.checkpoint = context.document;
        this.lastChecked = {
          revision: context.document.revision,
          silent: result.status === "silent",
        };
        if (
          result.status === "observe" &&
          !this.recent.some((text) => similar(text, result.text))
        ) {
          this.cooldownUntil = Date.now() + this.cooldownMs;
          this.observations.push({
            id: `observation-${context.document.revision}`,
            revision: context.document.revision,
            text: result.text,
            status: "active",
            timestamp: Date.now(),
          });
          this.recent = [...this.recent, result.text].slice(-20);
        }
      } catch {
        if (!this.disposed && !controller.signal.aborted && generation === this.generation)
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
        !this.available ||
        generation !== this.generation ||
        selectionGeneration !== this.selectionGeneration
      )
        return false;
      const entries: Entry[] = [
        ...this.discussion,
        { role: "user", text: message },
        { role: "assistant", text: response.text },
      ];
      this.discussion = entries.slice(-12);
      const selected = this.observations.find((item) => item.id === selectedId);
      if (selected) selected.status = "discussed";
      this.cooldownUntil = Date.now() + this.cooldownMs;
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

  select(id: string) {
    if (!this.observations.some((item) => item.id === id)) return;
    if (this.selectedId !== id) {
      this.selectionGeneration++;
      if (this.busy === "discuss") this.controller?.abort();
      this.discussion = [];
    }
    this.selectedId = id;
    this.notify();
  }

  dismiss(id: string) {
    const item = this.observations.find((observation) => observation.id === id);
    if (item) item.status = "dismissed";
    if (this.selectedId === id) {
      this.selectionGeneration++;
      if (this.busy === "discuss") this.controller?.abort();
      this.selectedId = undefined;
      this.discussion = [];
    }
    this.notify();
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

  retry() {
    this.error = "";
    this.cooldownUntil = 0;
    this.schedule();
    this.notify();
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
