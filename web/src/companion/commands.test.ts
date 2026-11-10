import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  type CompanionCommand,
  type CompanionOutcome,
  releaseCommandLocks,
  resetCompanionCommands,
  runCompanionCommand,
} from "./commands";
import { companionLock, companionPlayback, resetCompanionHost } from "./host";
import {
  companionState,
  reportEvent,
  resetWorkStatus,
  setCompanionPaused,
  setCompanionReady,
  startTurn,
  subscribeCompanionState,
} from "./workStatus";

// The local console's screen saver, up or not.
const cover = vi.hoisted(() => ({ up: false }));
vi.mock("../desktop/LocalConsoleScreenSaver", () => ({
  localConsoleCovered: () => cover.up,
  isLocalConsole: () => false,
}));

// The page without the card: what a command writes is read from the page's state, and the
// card's receipts are reported to the playback store as petCard.tsx passes them on.
const SESSION = "live0001";
const WAVE = "点击回应-元气挥手";
const run = (action: string, args: CompanionCommand["args"] = {}, session = SESSION) =>
  runCompanionCommand(session, { action, args });
const leaf = (name: "say" | "anim" | "position" | "throw" | "lock" | "sleep") =>
  companionState.read().pets.main?.[name].data;
const refused = (reason: string, extra: Record<string, unknown> = {}) => ({
  ok: false,
  error: { reason, message: expect.any(String), ...extra },
});
const ok = (result: Record<string, unknown> = {}) => ({ ok: true, result: { status: "ok", ...result } });
/** A field of a command's result. */
const field = (outcome: CompanionOutcome, name: string) => (outcome.ok ? outcome.result[name] : undefined);
/** How often the page's state has been written: a refused command writes nothing. */
const writes = () => companionState.read().counter;
/** The animation the last anim command wrote. */
const playing = () => (leaf("anim") as { playbackId: string }).playbackId;

beforeEach(() => {
  setCompanionReady(true);
  startTurn(SESSION);
});
afterEach(() => {
  vi.useRealTimers();
  resetWorkStatus();
  resetCompanionHost();
  resetCompanionCommands();
  setCompanionReady(false);
  setCompanionPaused(false);
  cover.up = false;
});

describe("an animation VNAS asks for", () => {
  it("is written with a playback id and answered once the card has played it to its end", async () => {
    const answer = run("anim", { name: WAVE, facing: "right" });
    const id = playing();
    expect(leaf("anim")).toEqual({ name: WAVE, playbackId: id, facing: "right" });
    companionPlayback.report(id, "playing");
    companionPlayback.report(id, "ended");
    expect(await answer).toEqual({ ok: true, result: { status: "ended", playbackId: id } });
    expect(companionPlayback.waiterCount()).toBe(0);
  });

  it("is answered cancelled when the next one supersedes it, and failed with the card's reason", async () => {
    const first = run("anim", { name: WAVE });
    const firstId = playing();
    const second = run("anim", { name: "东张西望" });
    expect(await first).toEqual({
      ok: true,
      result: { status: "cancelled", playbackId: firstId, reason: "superseded" },
    });
    const secondId = playing();
    companionPlayback.report(secondId, "failed", "asset-load");
    expect(await second).toEqual({
      ok: true,
      result: { status: "failed", playbackId: secondId, reason: "asset-load" },
    });
  });

  it("is answered ended when a clip as long as dsh-pet's own, 10.04 s, has loaded and played to its end", async () => {
    vi.useFakeTimers();
    let outcome: unknown;
    void run("anim", { name: WAVE }).then((answer) => {
      outcome = answer;
    });
    const id = playing();
    await vi.advanceTimersByTimeAsync(400);
    companionPlayback.report(id, "playing");
    await vi.advanceTimersByTimeAsync(10_040);
    companionPlayback.report(id, "ended");
    await vi.advanceTimersByTimeAsync(0);
    expect(outcome).toEqual({ ok: true, result: { status: "ended", playbackId: id } });
  });

  it("is answered failed with reason timeout after 11.5 s without an end", async () => {
    vi.useFakeTimers();
    let outcome: unknown;
    void run("anim", { name: WAVE }).then((answer) => {
      outcome = answer;
    });
    const id = playing();
    companionPlayback.report(id, "playing");
    await vi.advanceTimersByTimeAsync(11_499);
    expect(outcome).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(outcome).toEqual({ ok: true, result: { status: "failed", playbackId: id, reason: "timeout" } });
  });

  it("stops being waited on when the window goes", async () => {
    const closed = new AbortController();
    const answer = runCompanionCommand(SESSION, { action: "anim", args: { name: WAVE } }, closed.signal);
    closed.abort();
    expect(await answer).toEqual(refused("not-ready"));
    expect(companionPlayback.waiterCount()).toBe(0);
  });

  it("must be one of the pet's animations, by name, facing left or right", async () => {
    const before = writes();
    expect(await run("anim", { name: "不存在的动画" })).toEqual(refused("unknown-animation"));
    expect(await run("anim", { url: "https://example.com/a.webm" })).toEqual(refused("bad-request"));
    expect(await run("anim", { name: WAVE, url: "https://example.com/a.webm" })).toEqual(refused("bad-request"));
    expect(await run("anim", { name: WAVE, facing: "up" })).toEqual(refused("bad-request"));
    expect(await run("anim")).toEqual(refused("bad-request"));
    expect(writes()).toBe(before);
  });
});

describe("what VNAS says and where it puts the companion", () => {
  it("says plain text for 10 s unless told otherwise, and clears the bubble", async () => {
    expect(await run("broadcast", { text: "  照片整理好了 " })).toEqual(ok());
    expect(leaf("say")).toEqual({ text: "照片整理好了", duration: 10_000, anim: true });
    expect(await run("broadcast", { text: "好", duration: 3000, anim: false })).toEqual(ok());
    expect(leaf("say")).toEqual({ text: "好", duration: 3000, anim: false });
    expect(await run("bubble_clear")).toEqual(ok());
    expect(leaf("say")).toEqual({ clear: true });
  });

  it("shows no picture: neither a meme by name nor an address", async () => {
    const before = writes();
    expect(await run("broadcast", { text: "看", image: "doge" })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: "看", url: "https://example.com/a.png" })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: " " })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: "看", duration: -1 })).toEqual(refused("bad-request"));
    expect(writes()).toBe(before);
  });

  it("moves it within the page and throws it", async () => {
    expect(await run("position", { x: 1.5, y: -0.2, mode: "spring" })).toEqual(ok({ x: 1, y: 0 }));
    expect(leaf("position")).toEqual({ x: 1, y: 0, mode: "spring" });
    expect(await run("position", { x: 0.5, y: 0.5, mode: "line", duration: 800 })).toEqual(ok({ x: 0.5, y: 0.5 }));
    expect(leaf("position")).toEqual({ x: 0.5, y: 0.5, mode: "line", duration: 800 });
    expect(await run("position", { x: 0.5, y: 0.5, mode: "jump" })).toEqual(refused("bad-request"));
    expect(await run("throw", { angle: 60, power: 1.5 })).toEqual(ok());
    expect(leaf("throw")).toEqual({ angle: 60, power: 1.5 });
    expect(await run("throw", { angle: 60, power: 0 })).toEqual(refused("bad-request"));
  });

  it("says only plain text, as the product service checks it", async () => {
    // Every bidi control, not only the overrides, the separators, a tab, and either even at the text's
    // ends (contract v1.2). Each is refused, and a refused broadcast takes none of the minute's six.
    const sneaky = [0x061c, 0x200e, 0x200f, 0x2066, 0x2028, 0x2029].map((code) => String.fromCodePoint(code));
    const tab = String.fromCodePoint(9);
    for (const text of [...sneaky.map((char) => `看${char}字`), `字${sneaky[4]}`, `${tab}字`])
      expect(await run("broadcast", { text })).toEqual(refused("bad-request"));
    for (let i = 0; i < 5; i++) expect(await run("broadcast", { text: `第${i}句` })).toEqual(ok());
    expect(await run("broadcast", { text: "两行\n字" })).toEqual(ok());
    expect(await run("broadcast", { text: "第七句" })).toEqual(refused("rate-limited"));
  });

  it("keeps to the contract's limits where dsh-pet sets none", async () => {
    const before = writes();
    expect(await run("broadcast", { text: "字".repeat(201) })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: "看\u202E反过来" })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: "响\u0007铃" })).toEqual(refused("bad-request"));
    expect(await run("broadcast", { text: "嗨", duration: 60_001 })).toEqual(refused("bad-request"));
    expect(await run("position", { x: 0.5, y: 0.5, mode: "line", duration: 5001 })).toEqual(refused("bad-request"));
    expect(await run("throw", { angle: 0, power: 3.5 })).toEqual(refused("bad-request"));
    expect(writes()).toBe(before);
    expect(await run("broadcast", { text: "字".repeat(200), duration: 60_000 })).toEqual(ok());
    expect(await run("broadcast", { text: "两行\n字" })).toEqual(ok());
    expect(await run("throw", { angle: 0, power: 3 })).toEqual(ok());
  });

  it("is answered even when the page cannot finish writing it", async () => {
    const stop = subscribeCompanionState(() => {
      throw new Error("a reader of the page's state broke");
    });
    try {
      expect(await run("broadcast", { text: "嗨" })).toEqual(refused("not-ready"));
      expect(await run("anim", { name: WAVE })).toEqual({
        ok: true,
        result: { status: "failed", playbackId: expect.stringMatching(/^pb-/), reason: "write-error" },
      });
      expect(companionPlayback.waiterCount()).toBe(0);
    } finally {
      stop();
    }
  });

  it("does not know a tool outside the contract", async () => {
    expect(await run("whisper", { text: "嗨" })).toEqual(refused("bad-request"));
  });
});

describe("sleep", () => {
  it("puts it to sleep with dsh-pet's sleep animations and wakes it, each once", async () => {
    expect(await run("sleep")).toEqual(ok({ sleeping: true, changed: true }));
    expect(leaf("sleep")).toEqual({ sleeping: true, start: "睡觉-开始", loop: "睡觉-循环", end: "睡觉-起来" });
    expect(await run("sleep")).toEqual(ok({ sleeping: true, changed: false }));
    expect(await run("wake")).toEqual(ok({ sleeping: false, changed: true }));
    expect(leaf("sleep")).toEqual({ sleeping: false });
    expect(await run("wake")).toEqual(ok({ sleeping: false, changed: false }));
  });

  it("refuses what would disturb it while it sleeps, but not where it lies", async () => {
    await run("sleep");
    expect(await run("anim", { name: WAVE })).toEqual(refused("sleeping"));
    expect(await run("broadcast", { text: "醒醒" })).toEqual(refused("sleeping"));
    expect(await run("bubble_clear")).toEqual(refused("sleeping"));
    // The gate comes before the arguments, as dsh-pet's router puts it.
    expect(await run("anim", { name: "不存在的动画" })).toEqual(refused("sleeping"));
    expect(await run("position", { x: 0.2, y: 0.8, mode: "instant" })).toEqual(ok({ x: 0.2, y: 0.8 }));
    expect(await run("throw", { angle: 90, power: 1 })).toEqual(ok());
  });

  it("is not for a pet someone holds, and taking the lock wakes it", async () => {
    await run("sleep");
    const taken = await run("lock");
    expect(leaf("sleep")).toEqual({ sleeping: false });
    const expiresAt = field(taken, "expiresAt");
    expect(await run("sleep")).toEqual(refused("locked", { expiresAt }));
  });
});

describe("the lock", () => {
  it("lets only its holder command the pet, and tells a holder whose lock went", async () => {
    vi.useFakeTimers({ now: 1_000_000 });
    const taken = await run("lock", { ttl: 60_000 });
    expect(taken).toEqual(ok({ lockId: expect.stringMatching(/^lk-/), expiresAt: 1_060_000 }));
    expect(leaf("lock")).toEqual({ locked: true, expiresAt: 1_060_000 });
    const lockId = field(taken, "lockId");

    for (const [action, args] of [
      ["anim", { name: WAVE }],
      ["broadcast", { text: "嗨" }],
      ["bubble_clear", {}],
      ["position", { x: 0.5, y: 0.5, mode: "instant" }],
      ["throw", { angle: 0, power: 1 }],
    ] as const) {
      expect(await run(action, args)).toEqual(refused("lock-required", { expiresAt: 1_060_000 }));
      expect(await run(action, { ...args, lock: "lk-other" })).toEqual(refused("lock-lost", { cause: "unknown" }));
    }
    // The arguments come before the lock, as in dsh-pet's routes.
    expect(await run("anim", { name: "不存在的动画" })).toEqual(refused("unknown-animation"));
    expect(await run("position", { x: 0.3, y: 0.3, mode: "instant", lock: lockId })).toEqual(ok({ x: 0.3, y: 0.3 }));
    const answer = run("anim", { name: WAVE, lock: lockId });
    companionPlayback.report(playing(), "ended");
    expect(await answer).toMatchObject({ ok: true, result: { status: "ended" } });
    // Another caller cannot take it, and only its holder renews it.
    expect(await run("lock")).toEqual(refused("locked", { expiresAt: 1_060_000 }));
    vi.setSystemTime(1_010_000);
    expect(await run("lock", { lockId })).toEqual(ok({ lockId, expiresAt: 1_070_000 }));

    expect(await run("lock_release", { lockId })).toEqual(ok());
    expect(leaf("lock")).toEqual({ locked: false });
    expect(await run("throw", { angle: 0, power: 1, lock: lockId })).toEqual(
      refused("lock-lost", { cause: "released" }),
    );
    expect(await run("throw", { angle: 0, power: 1 })).toEqual(ok());
  });

  it("ends when its time is up", async () => {
    vi.useFakeTimers({ now: 1_000_000 });
    const lockId = field(await run("lock", { ttl: 1000 }), "lockId");
    vi.setSystemTime(1_001_000);
    expect(await run("broadcast", { text: "嗨", lock: lockId })).toEqual(refused("lock-lost", { cause: "expired" }));
    expect(await run("lock", { lockId })).toEqual(refused("lock-lost", { cause: "expired" }));
  });

  it("lasts at most 5 minutes, and at least dsh-pet's second", async () => {
    vi.useFakeTimers({ now: 1_000_000 });
    expect(await run("lock", { ttl: 1_800_000 })).toEqual(ok({ lockId: expect.any(String), expiresAt: 1_300_000 }));
    resetCompanionHost();
    expect(await run("lock", { ttl: 500 })).toEqual(refused("bad-request"));
    expect(await run("lock", { ttl: "long" })).toEqual(refused("bad-request"));
  });

  it("is released by force when the product service settles the turn that took it, and only that one", async () => {
    const lockId = field(await run("lock"), "lockId");
    expect(await run("lock_release", { lockId: "lk-other" })).toEqual(refused("lock-lost", { cause: "unknown" }));
    // Another conversation's turn in this window ends: the lock is not its own.
    startTurn("live0002");
    reportEvent("live0002", "message.complete", { status: "complete" });
    expect(await run("lock_release", { force: true }, "live0002")).toEqual(ok());
    expect(companionLock.locked("main")).toBe(true);
    reportEvent(SESSION, "message.complete", { status: "complete" });
    expect(await run("lock_release", { lockId })).toEqual(refused("not-controlling"));
    setCompanionReady(false);
    expect(await run("lock_release", { force: true })).toEqual(ok());
    expect(companionLock.locked("main")).toBe(false);
    expect(leaf("lock")).toEqual({ locked: false });
    // Its turn ended: the member did not take the pet over.
    startTurn(SESSION);
    setCompanionReady(true);
    expect(await run("lock", { lockId })).toEqual(refused("lock-lost", { cause: "released" }));
    // Releasing what is not there is done already.
    expect(await run("lock_release", { force: true })).toEqual(ok());
    expect(await run("lock_release", { force: true, lockId })).toEqual(refused("bad-request"));
  });

  it("goes with the turn of the conversation that last renewed it", async () => {
    const lockId = field(await run("lock"), "lockId");
    startTurn("live0002");
    expect(await run("lock", { lockId }, "live0002")).toMatchObject({ ok: true });
    // The conversation that took it ends its turn: the lock is the renewer's now, and stays.
    reportEvent(SESSION, "message.complete", { status: "complete" });
    expect(await run("lock_release", { force: true })).toEqual(ok());
    expect(companionLock.locked("main")).toBe(true);
    // The renewer's turn ends: it goes, rather than holding the pet until it runs out.
    reportEvent("live0002", "message.complete", { status: "complete" });
    expect(await run("lock_release", { force: true }, "live0002")).toEqual(ok());
    expect(companionLock.locked("main")).toBe(false);
  });

  it("goes with the window, which the product service can no longer settle", async () => {
    const lockId = field(await run("lock"), "lockId");
    releaseCommandLocks();
    expect(companionLock.locked("main")).toBe(false);
    expect(leaf("lock")).toEqual({ locked: false });
    expect(await run("broadcast", { text: "嗨", lock: lockId })).toEqual(refused("lock-lost", { cause: "released" }));
  });
});

describe("what the window checks first", () => {
  it("answers not-controlling for a conversation whose turn another device started", async () => {
    const before = writes();
    expect(await run("broadcast", { text: "嗨" }, "live0002")).toEqual(refused("not-controlling"));
    expect(await run("sleep", {}, "live0002")).toEqual(refused("not-controlling"));
    expect(writes()).toBe(before);
  });

  it("answers not-ready while there is no card on the page", async () => {
    setCompanionReady(false);
    const before = writes();
    for (const action of ["anim", "broadcast", "position", "sleep", "wake", "lock"])
      expect(await run(action, { name: WAVE, text: "嗨" })).toEqual(refused("not-ready"));
    expect(writes()).toBe(before);
  });

  it("answers paused for what a hidden page's card would drop, and still takes a lock or a sleep", async () => {
    setCompanionPaused(true);
    expect(await run("anim", { name: WAVE })).toEqual(refused("paused"));
    expect(await run("broadcast", { text: "嗨" })).toEqual(refused("paused"));
    expect(await run("bubble_clear")).toEqual(refused("paused"));
    expect(await run("position", { x: 0.5, y: 0.5, mode: "instant" })).toEqual(refused("paused"));
    expect(await run("throw", { angle: 0, power: 1 })).toEqual(refused("paused"));
    expect(await run("sleep")).toEqual(ok({ sleeping: true, changed: true }));
    expect(await run("lock")).toMatchObject({ ok: true });
  });

  it("answers paused under the local console's screen saver", async () => {
    cover.up = true;
    const before = writes();
    expect(await run("anim", { name: WAVE })).toEqual(refused("paused"));
    expect(await run("broadcast", { text: "嗨" })).toEqual(refused("paused"));
    expect(writes()).toBe(before);
    cover.up = false;
    expect(await run("broadcast", { text: "嗨" })).toEqual(ok());
  });

  it("carries out at most 20 commands a minute, 6 broadcasts and 6 throws among them", async () => {
    vi.useFakeTimers({ now: 1_000_000 });
    for (let i = 0; i < 6; i++) expect(await run("broadcast", { text: `第${i}句` })).toEqual(ok());
    expect(await run("broadcast", { text: "第7句" })).toEqual(refused("rate-limited"));
    for (let i = 0; i < 6; i++) expect(await run("throw", { angle: 0, power: 1 })).toEqual(ok());
    expect(await run("throw", { angle: 0, power: 1 })).toEqual(refused("rate-limited"));
    for (let i = 0; i < 8; i++)
      expect(await run("position", { x: 0.5, y: 0.5, mode: "instant" })).toMatchObject({ ok: true });
    expect(await run("wake")).toEqual(refused("rate-limited"));
    // Releasing is never held back.
    expect(await run("lock_release", { force: true })).toEqual(ok());
    vi.setSystemTime(1_060_000);
    expect(await run("broadcast", { text: "过了一分钟" })).toEqual(ok());
  });
});
