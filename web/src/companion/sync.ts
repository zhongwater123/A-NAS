import type {
  CompanionAction,
  CompanionClaim,
  CompanionClientKind,
  CompanionHolder,
  CompanionState,
} from "../api.generated";

export interface CompanionTransport {
  read(signal?: AbortSignal): Promise<CompanionState>;
  claim(body: CompanionClaim, signal?: AbortSignal): Promise<CompanionState>;
  heartbeat(body: CompanionHolder, signal?: AbortSignal): Promise<CompanionState>;
  release(body: CompanionHolder, signal?: AbortSignal): Promise<CompanionState>;
  act(body: CompanionAction, signal?: AbortSignal): Promise<CompanionState>;
}
export interface SyncSnapshot {
  connection: "connecting" | "online" | "offline" | "unavailable";
  state: CompanionState | null;
  here: boolean;
  waiting: boolean;
  message: string;
}
export const initialSync: SyncSnapshot = {
  connection: "connecting",
  state: null,
  here: false,
  waiting: false,
  message: "正在连接 NAS",
};

// The renderer does not grant itself a lease. Its deadline starts BEFORE the
// request (not at response arrival), avoiding clock skew and delayed replies.
export class CompanionSync {
  private snapshot: SyncSnapshot = initialSync;
  private listeners = new Set<() => void>();
  private poll: ReturnType<typeof setTimeout> | undefined;
  private expiry: ReturnType<typeof setTimeout> | undefined;
  private deadline = 0;
  private generation = 0;
  private running = false;
  private abort = new AbortController();
  private suppressed = false;
  private queue: Promise<unknown> = Promise.resolve();

  constructor(
    readonly id: string,
    readonly kind: CompanionClientKind,
    private transport: CompanionTransport,
    private now: () => number = () => performance.now(),
    private pollMS = 1500,
  ) {}

  get = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<SyncSnapshot>) {
    this.snapshot = { ...this.snapshot, ...change };
    for (const listener of this.listeners) listener();
  }
  private failure(error: unknown) {
    this.deadline = 0;
    clearTimeout(this.expiry);
    const status = error && typeof error === "object" && "status" in error ? error.status : 0;
    this.publish({
      here: false,
      connection: status === 409 ? "online" : status === 404 || status === 503 ? "unavailable" : "offline",
      message:
        status === 409
          ? "伙伴状态已变化，请重新叫过来"
          : status === 404 || status === 503
            ? "当前 NAS 尚未提供伙伴接续"
            : "NAS 连接中断，互动已暂停",
    });
  }
  private accept(state: CompanionState, started: number) {
    const previous = this.snapshot.state;
    if (previous && state.revision < previous.revision) return;
    const mine = state.active?.clientId === this.id;
    const transferring = Boolean(mine && state.pending && state.pending.clientId !== this.id);
    const sameEpoch = previous?.active?.epoch === state.active?.epoch;
    this.deadline =
      mine && !transferring && !this.suppressed
        ? Math.max(sameEpoch ? this.deadline : 0, started + (state.active?.remainingMs ?? 0))
        : 0;
    const here = this.deadline > this.now();
    const waiting = state.pending?.clientId === this.id;
    this.publish({
      state,
      here,
      waiting,
      connection: "online",
      message: transferring
        ? "正在交给另一处"
        : here
          ? "正在这里陪你"
          : waiting
            ? "等另一处收好东西，马上过来"
            : state.active
              ? `伙伴正在${state.active.kind === "desktop" ? "电脑桌宠" : "网页小窝"}`
              : "伙伴在小窝，随时可以叫来",
    });
    clearTimeout(this.expiry);
    if (here)
      this.expiry = setTimeout(
        () => {
          this.publish({ here: false, message: "刚才断开了，再叫我过来吧" });
        },
        Math.max(0, this.deadline - this.now()),
      );
  }
  private async receive(request: (signal: AbortSignal) => Promise<CompanionState>, generation = this.generation) {
    if (!this.running || generation !== this.generation) throw new DOMException("stopped", "AbortError");
    const started = this.now();
    const state = await request(this.abort.signal);
    if (this.running && generation === this.generation) this.accept(state, started);
    return state;
  }
  start() {
    if (this.running) return;
    this.running = true;
    this.abort = new AbortController();
    this.suppressed = false;
    const generation = ++this.generation;
    this.publish({ connection: "connecting", here: false, message: "正在连接 NAS" });
    void this.tick(generation);
  }
  stop() {
    this.running = false;
    this.abort.abort();
    this.generation++;
    clearTimeout(this.poll);
    clearTimeout(this.expiry);
    this.deadline = 0;
    this.publish({ here: false });
  }
  private async tick(generation: number) {
    try {
      const state = await this.receive((signal) => this.transport.read(signal), generation);
      if (!this.running || generation !== this.generation || state.revision < (this.snapshot.state?.revision ?? 0))
        return;
      if (state.active?.clientId === this.id && state.pending && state.pending.clientId !== this.id) {
        // Stop before acknowledging. The candidate remains inactive until the
        // server atomically transfers the epoch, or the old lease expires.
        this.suppressed = true;
        this.publish({ here: false });
        await this.receive(
          (signal) => this.transport.release({ clientId: this.id, epoch: state.active!.epoch }, signal),
          generation,
        );
      } else if (!state.active && state.pending?.clientId === this.id) {
        await this.receive(
          (signal) => this.transport.claim({ clientId: this.id, kind: this.kind, revision: state.revision }, signal),
          generation,
        );
      } else if (this.snapshot.here && state.active && this.deadline - this.now() < 9000) {
        await this.receive(
          (signal) => this.transport.heartbeat({ clientId: this.id, epoch: state.active!.epoch }, signal),
          generation,
        );
      }
    } catch (error) {
      if (generation === this.generation) this.failure(error);
    } finally {
      if (this.running && generation === this.generation)
        this.poll = setTimeout(() => void this.tick(generation), this.pollMS);
    }
  }
  private serialized(task: (generation: number) => Promise<void>) {
    const generation = this.generation;
    const guarded = () => (this.running && generation === this.generation ? task(generation) : Promise.resolve());
    const next = this.queue.then(guarded, guarded);
    this.queue = next.catch(() => undefined);
    return next;
  }
  claim = () =>
    this.serialized(async (generation) => {
      this.suppressed = false;
      try {
        const state = await this.receive((signal) => this.transport.read(signal), generation);
        await this.receive(
          (signal) => this.transport.claim({ clientId: this.id, kind: this.kind, revision: state.revision }, signal),
          generation,
        );
      } catch (error) {
        if (this.running && generation === this.generation) this.failure(error);
      }
    });
  release = () =>
    this.serialized(async (generation) => {
      const active = this.snapshot.state?.active;
      if (!active || active.clientId !== this.id) return;
      this.suppressed = true;
      this.deadline = 0;
      this.publish({ here: false, message: "正在收好东西" });
      try {
        await this.receive(
          (signal) => this.transport.release({ clientId: this.id, epoch: active.epoch }, signal),
          generation,
        );
      } catch (error) {
        if (this.running && generation === this.generation) this.failure(error);
      }
    });
  bindConversation = (conversationId: string) =>
    this.serialized(async (generation) => {
      const active = this.snapshot.state?.active;
      if (!this.snapshot.here || this.deadline <= this.now() || !active)
        throw new Error("Call the companion here before starting a task");
      await this.receive(
        (signal) =>
          this.transport.act(
            {
              clientId: this.id,
              epoch: active.epoch,
              eventId: crypto.randomUUID(),
              type: "conversation",
              conversationId,
            },
            signal,
          ),
        generation,
      );
      if (!this.running || generation !== this.generation) throw new DOMException("stopped", "AbortError");
    });

  act = (type: CompanionAction["type"], character?: CompanionAction["character"]) =>
    this.serialized(async (generation) => {
      const active = this.snapshot.state?.active;
      if (!this.snapshot.here || this.deadline <= this.now() || !active) return;
      try {
        await this.receive(
          (signal) =>
            this.transport.act(
              {
                clientId: this.id,
                epoch: active.epoch,
                eventId: crypto.randomUUID(),
                type,
                ...(character ? { character } : {}),
              },
              signal,
            ),
          generation,
        );
      } catch (error) {
        if (this.running && generation === this.generation) this.failure(error);
      }
    });
}
