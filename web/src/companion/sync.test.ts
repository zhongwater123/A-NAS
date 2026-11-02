import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { CompanionState } from "../api.generated";
import { CompanionSync, type CompanionTransport } from "./sync";

const id = "00000000-0000-4000-8000-000000000001";
const other = "00000000-0000-4000-8000-000000000002";
const empty = (): CompanionState => ({
  conversationId: "",
  revision: 0,
  character: "glow",
  quiet: false,
  playCount: 0,
  active: null,
  pending: null,
});
const owned = (revision = 1): CompanionState => ({
  ...empty(),
  revision,
  active: { clientId: id, kind: "web", epoch: 1, remainingMs: 15000 },
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
async function flush() {
  for (let i = 0; i < 15; i++) await Promise.resolve();
}
function fixture() {
  const transport = {
    read: vi.fn<CompanionTransport["read"]>().mockResolvedValue(empty()),
    claim: vi.fn<CompanionTransport["claim"]>().mockResolvedValue(owned()),
    heartbeat: vi.fn<CompanionTransport["heartbeat"]>().mockResolvedValue(owned()),
    release: vi.fn<CompanionTransport["release"]>().mockResolvedValue({ ...empty(), revision: 2 }),
    act: vi.fn<CompanionTransport["act"]>().mockResolvedValue({ ...owned(2), playCount: 1 }),
  };
  const sync = new CompanionSync(id, "web", transport, () => Date.now(), 1000);
  return { sync, transport };
}
const running: CompanionSync[] = [];
async function start(sync: CompanionSync) {
  running.push(sync);
  sync.start();
  await flush();
}
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => {
  for (const sync of running.splice(0)) sync.stop();
  vi.useRealTimers();
});

describe("NAS companion handoff controller", () => {
  it("only observes on opening; explicit summon obtains authority", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    expect(sync.get().here).toBe(false);
    expect(transport.claim).not.toHaveBeenCalled();
    await sync.claim();
    expect(sync.get().here).toBe(true);
  });

  it("stops locally before acknowledging handoff to the candidate", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    await sync.claim();
    const transferring: CompanionState = { ...owned(2), pending: { clientId: other, kind: "desktop" } };
    transport.read.mockResolvedValue(transferring);
    transport.release.mockImplementation(async () => {
      expect(sync.get().here).toBe(false);
      return { ...owned(3), active: { ...owned().active!, clientId: other, kind: "desktop", epoch: 2 } };
    });
    await vi.advanceTimersByTimeAsync(1000);
    expect(transport.release).toHaveBeenCalledWith({ clientId: id, epoch: 1 }, expect.any(AbortSignal));
    expect(sync.get().here).toBe(false);
    await sync.act("ball");
    expect(transport.act).not.toHaveBeenCalled();
  });

  it("a candidate waits while the old holder is alive, then claims after expiry", async () => {
    const { sync, transport } = fixture();
    const waiting: CompanionState = {
      ...owned(2),
      active: { ...owned().active!, clientId: other },
      pending: { clientId: id, kind: "web" },
    };
    transport.read.mockResolvedValue(waiting);
    transport.claim.mockResolvedValueOnce(waiting).mockResolvedValue(owned(3));
    await start(sync);
    await sync.claim();
    expect(sync.get()).toMatchObject({ here: false, waiting: true });
    await vi.advanceTimersByTimeAsync(1000);
    expect(transport.claim).toHaveBeenCalledTimes(1);
    transport.read.mockResolvedValue({ ...waiting, active: null });
    await vi.advanceTimersByTimeAsync(1000);
    expect(sync.get()).toMatchObject({ here: true, waiting: false });
  });

  it("expires local authority even when the next read never returns", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    await sync.claim();
    transport.read.mockReturnValue(deferred<CompanionState>().promise);
    await vi.advanceTimersByTimeAsync(15001);
    expect(sync.get().here).toBe(false);
    await sync.act("ball");
    expect(transport.act).not.toHaveBeenCalled();
  });

  it("does not extend authority by the duration of a delayed response", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    const delayed = deferred<CompanionState>();
    transport.claim.mockReturnValue(delayed.promise);
    const claim = sync.claim();
    await flush();
    await vi.advanceTimersByTimeAsync(16000);
    delayed.resolve(owned());
    await claim;
    expect(sync.get().here).toBe(false);
  });

  it("does not let an older read revert shared progress", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    await sync.claim();
    await sync.act("ball");
    transport.read.mockResolvedValue(owned(1));
    await vi.advanceTimersByTimeAsync(1000);
    expect(sync.get().state?.playCount).toBe(1);
  });

  it("cancels pending claim and queued actions across stop/logout", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    const pending = deferred<CompanionState>();
    transport.read.mockReturnValue(pending.promise);
    const claim = sync.claim();
    const action = sync.act("ball");
    await flush();
    const requestSignal = transport.read.mock.calls.at(-1)?.[0];
    sync.stop();
    expect(requestSignal?.aborted).toBe(true);
    pending.resolve(empty());
    await claim;
    await action;
    expect(transport.claim).not.toHaveBeenCalled();
    expect(transport.act).not.toHaveBeenCalled();
    expect(sync.get().here).toBe(false);
  });

  it("pauses on disconnect and never replays offline actions", async () => {
    const { sync, transport } = fixture();
    await start(sync);
    await sync.claim();
    transport.read.mockRejectedValue(new TypeError("network"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(sync.get()).toMatchObject({ connection: "offline", here: false });
    await sync.act("ball");
    transport.read.mockResolvedValue(owned());
    await vi.advanceTimersByTimeAsync(1000);
    expect(transport.act).not.toHaveBeenCalled();
  });
});
