// VNAS's commands to 大肥鱼 (the product service's anas.companion events), carried out in
// the page the way dsh-pet's host carries out its routes. What a command may do and what it
// writes are dsh-pet's own decision layers, vendored from dsh-pet/host: decideAnim (/anim),
// decidePosition, decideThrow and positionLeafData (/position, /throw), the broadcast rules
// (/broadcast, /bubble/clear), pickSleepNames (/sleep), and the lock and sleep stores with the
// lock's gate (host.ts). The answer is the companion contract's result for the tool.
//
// The route code around those layers (dsh-pet host/routes/{index,motion,interaction,sleep}.ts
// and plugin/pet-lookup.ts) runs an HTTP server and reads configuration files, so it is not
// vendored; what of it the commands need is ported here, in its order: the sleep gate first
// (anim, broadcast and bubble_clear are refused while the pet sleeps, unless it is locked), then
// the body and the pet (the decision layer), then the lock gate (anim, broadcast, position,
// throw and bubble_clear need the lock's id while it is held), then the write. dsh-pet also says
// a line from the pet's sleep texts when it falls asleep, wakes or refuses; A-NAS's
// configuration of the pet carries none of those lines, so it says nothing.
//
// A-NAS's own checks come before dsh-pet's: the conversation must be one whose turn this
// window started (not-controlling), the card must be on the page (not-ready), and the page
// must not be hidden for a command the card would drop (paused). Then a backstop to the
// product service's limits (Frank's D5): this window carries out at most 20 commands a
// minute, 6 broadcasts and 6 throws among them, and a lock lasts at most 5 minutes.
import { decideAnim } from "./dsh-pet/host/anim";
import {
  decideBroadcast,
  decideBubbleClear,
  normalizeBroadcastAnim,
  normalizeBroadcastDuration,
  normalizeBroadcastText,
} from "./dsh-pet/host/broadcast";
import { decidePosition, decideThrow, positionLeafData } from "./dsh-pet/host/motion-cue";
import { findPetInstance, readAllConfig } from "./dsh-pet/host/pet-config";
import { MAX_PLAYBACK_WAIT_MS, TERMINAL_STATUSES } from "./dsh-pet/host/playback/status";
import { pickSleepNames } from "./dsh-pet/host/sleep";
import { companionLock, companionPlayback, companionSleep } from "./host";
import { COMPANION_PET, companionPaused, companionReady, companionState, isPrompting } from "./workStatus";

/** One command: the companion tool VNAS called and its arguments, as the product service checked them. */
export interface CompanionCommand {
  action: string;
  args: Record<string, unknown>;
}

/** Why a command was refused: dsh-pet's reason, or A-NAS's (not-controlling, not-ready, paused, rate-limited). */
export interface CompanionRefusal {
  reason: string;
  message: string;
  /** locked: when the lock ends. */
  expiresAt?: number;
  /** lock-lost: how the caller's lock went. */
  cause?: string;
}

/** The answer companion.result carries: the tool's result, or why the companion did not do it. */
export type CompanionOutcome = { ok: true; result: Record<string, unknown> } | { ok: false; error: CompanionRefusal };

/** The tools of the contract. */
const ACTIONS = new Set([
  "anim",
  "broadcast",
  "bubble_clear",
  "position",
  "throw",
  "sleep",
  "wake",
  "lock",
  "lock_release",
]);
/** The commands the card drops while the page is hidden; a lock and a sleep are states it takes up when shown. */
const CUES = new Set(["anim", "broadcast", "bubble_clear", "position", "throw"]);
/** dsh-pet's sleep gate (host/routes/index.ts sleepGated): what would disturb the pet, not where it is. */
const SLEEP_GATED = new Set(["anim", "broadcast", "bubble_clear"]);
/** The contract and D5: a lock lasts at most 5 minutes (dsh-pet allows 30). */
const LOCK_TTL_CAP_MS = 300_000;
/** D5's backstop to the product service's limits, per window: 20 commands a minute, 6 of these each. */
const RATE_WINDOW_MS = 60_000;
const RATE_LIMIT = 20;
const RATE_LIMITS: Readonly<Record<string, number>> = { broadcast: 6, throw: 6 };

const carriedOut: { at: number; action: string }[] = [];

const ok = (result: Record<string, unknown>): CompanionOutcome => ({ ok: true, result });
const refuse = (
  reason: string,
  message: string,
  extra?: Omit<CompanionRefusal, "reason" | "message">,
): CompanionOutcome => ({ ok: false, error: { reason, message, ...extra } });

/** Whether this command would go over a limit; one that does not counts from now on. */
function rateLimited(action: string): boolean {
  const now = Date.now();
  while (carriedOut.length > 0 && now - carriedOut[0].at >= RATE_WINDOW_MS) carriedOut.shift();
  if (carriedOut.length >= RATE_LIMIT) return true;
  const limit = RATE_LIMITS[action];
  if (limit !== undefined && carriedOut.filter((entry) => entry.action === action).length >= limit) return true;
  carriedOut.push({ at: now, action });
  return false;
}

/** dsh-pet's sleep gate (plugin/pet-lookup.ts sleepBlocked): a sleeping pet refuses, unless it is locked. */
function sleepBlocked(): CompanionOutcome | null {
  if (!companionSleep.sleeping(COMPANION_PET)) return null;
  if (companionLock.locked(COMPANION_PET)) return null;
  return refuse("sleeping", "桌宠正在睡觉，先叫醒它");
}

/**
 * dsh-pet's lock gate (plugin/pet-lookup.ts lockBlocked over host/lock.ts gate): while the pet is locked
 * only the lock's id lets a command through. dsh-pet answers a caller without it lock-required; the
 * contract names that locked, with when the lock ends.
 */
function lockBlocked(provided: unknown): CompanionOutcome | null {
  const gate = companionLock.gate(COMPANION_PET, String(provided ?? ""));
  if (gate.ok) return null;
  if (gate.reason === "lock-lost") return refuse("lock-lost", gate.message, { cause: gate.cause });
  return refuse("locked", gate.message, { expiresAt: companionLock.current(COMPANION_PET)?.expiresAt });
}

/**
 * An animation's end, as dsh-pet's GET /playback/{id}?wait=ended,failed,cancelled reports it, waited on
 * for at most dsh-pet's 10 s; past that the contract answers failed with reason timeout. A window that
 * goes away (signal) stops waiting and answers nothing.
 */
function played(playbackId: string, signal?: AbortSignal): Promise<CompanionOutcome> {
  return new Promise((resolve) => {
    function settle(outcome: CompanionOutcome) {
      signal?.removeEventListener("abort", abort);
      resolve(outcome);
    }
    const cancel = companionPlayback.wait({
      playbackId,
      wait: TERMINAL_STATUSES,
      timeoutMs: MAX_PLAYBACK_WAIT_MS,
      onWake: (record, matched) =>
        settle(
          ok(
            matched
              ? { status: record.status, playbackId, ...(record.reason ? { reason: record.reason } : {}) }
              : { status: "failed", playbackId, reason: "timeout" },
          ),
        ),
    });
    function abort() {
      cancel?.();
      settle(refuse("not-ready", "窗口已经关闭"));
    }
    if (signal?.aborted) abort();
    else signal?.addEventListener("abort", abort, { once: true });
  });
}

/**
 * Carries out one of VNAS's commands for a conversation and answers it as the contract says: anim once
 * its animation has ended, failed or been cancelled (at most 10 s), everything else as soon as it is
 * written. Never takes longer than that, so the window answers well within the product service's 12 s.
 */
export async function runCompanionCommand(
  sessionId: string,
  { action, args }: CompanionCommand,
  signal?: AbortSignal,
): Promise<CompanionOutcome> {
  if (!ACTIONS.has(action)) return refuse("bad-request", `没有这个命令：${action}`);
  // Only the product service sends a release with force, when it settles a turn that has ended or a
  // window that went away; that turn is no longer this window's, and a release must always succeed.
  const settling = action === "lock_release" && args.force === true;
  if (!settling && !isPrompting(sessionId)) return refuse("not-controlling", "这段对话不是在这个窗口里进行的");
  if (action !== "lock_release") {
    if (!companionReady()) return refuse("not-ready", "这个桌面上还没有伙伴：没有安装动画，或还没有加载");
    if (CUES.has(action) && companionPaused()) return refuse("paused", "页面在后台，伙伴暂停了");
    if (rateLimited(action)) return refuse("rate-limited", "命令太频繁，稍后再试");
  }
  if (SLEEP_GATED.has(action)) {
    const blocked = sleepBlocked();
    if (blocked) return blocked;
  }
  const cfg = readAllConfig();
  switch (action) {
    case "anim": {
      const d = decideAnim({ cfg, requested: COMPANION_PET, name: args.name, url: args.url, facing: args.facing });
      if (!d.ok) return refuse(d.reason, d.message);
      const blocked = lockBlocked(args.lock);
      if (blocked) return blocked;
      // The receipt is waited on before the leaf is written: a hidden or frozen card reports at once.
      const record = companionPlayback.create(d.petId, d.name ?? "");
      const outcome = played(record.playbackId, signal);
      companionState.writePet(d.petId, "anim", {
        name: d.name,
        playbackId: record.playbackId,
        ...(d.facing === undefined ? {} : { facing: d.facing }),
      });
      return outcome;
    }
    case "broadcast": {
      // What needs no configuration is checked first, as dsh-pet's route does.
      if (!normalizeBroadcastText(args.text)) return refuse("bad-request", "text 为空");
      const duration = normalizeBroadcastDuration(args.duration);
      if (duration === null) return refuse("bad-request", "duration 必须是 ≥0 的有限数字（毫秒）");
      const anim = normalizeBroadcastAnim(args.anim);
      const d = decideBroadcast({
        cfg,
        requested: COMPANION_PET,
        text: args.text,
        image: args.image,
        url: args.url,
        duration,
        anim,
      });
      if (!d.ok) return refuse(d.reason, d.message);
      const blocked = lockBlocked(args.lock);
      if (blocked) return blocked;
      companionState.writePet(d.petId, "say", { text: d.text, duration: d.duration, anim: d.anim });
      return ok({ status: "ok" });
    }
    case "bubble_clear": {
      const d = decideBubbleClear({ cfg, requested: COMPANION_PET });
      if (!d.ok) return refuse(d.reason, d.message);
      const blocked = lockBlocked(args.lock);
      if (blocked) return blocked;
      companionState.writePet(d.petId, "say", { clear: true });
      return ok({ status: "ok" });
    }
    case "position": {
      const d = decidePosition({
        cfg,
        requested: COMPANION_PET,
        x: args.x,
        y: args.y,
        mode: args.mode,
        duration: args.duration,
      });
      if (!d.ok) return refuse(d.reason, d.message);
      const blocked = lockBlocked(args.lock);
      if (blocked) return blocked;
      companionState.writePet(d.petId, "position", positionLeafData(d));
      return ok({ status: "ok", x: d.x, y: d.y });
    }
    case "throw": {
      const d = decideThrow({ cfg, requested: COMPANION_PET, angle: args.angle, power: args.power });
      if (!d.ok) return refuse(d.reason, d.message);
      const blocked = lockBlocked(args.lock);
      if (blocked) return blocked;
      companionState.writePet(d.petId, "throw", { angle: d.angle, power: d.power });
      return ok({ status: "ok" });
    }
    case "sleep": {
      // The lock first: a lock is someone else in charge, and taking it woke the pet.
      if (companionLock.locked(COMPANION_PET))
        return refuse("locked", "这只桌宠正被占用，不能睡觉", {
          expiresAt: companionLock.current(COMPANION_PET)?.expiresAt,
        });
      const names = pickSleepNames(findPetInstance(cfg, COMPANION_PET)?.conf.animations);
      if (names === null) return refuse("no-sleep-animations", "这只宠物没有配置睡觉动画（animations.sleep）");
      const changed = companionSleep.enter(COMPANION_PET, names);
      return ok({ status: "ok", sleeping: true, changed });
    }
    case "wake":
      return ok({ status: "ok", sleeping: false, changed: companionSleep.wake(COMPANION_PET) });
    case "lock": {
      const ttl = typeof args.ttl === "number" && args.ttl > LOCK_TTL_CAP_MS ? LOCK_TTL_CAP_MS : args.ttl;
      const d = companionLock.acquire({ petId: COMPANION_PET, lockId: args.lockId, ttl });
      if (!d.ok) {
        if (d.reason === "locked") return refuse(d.reason, d.message, { expiresAt: d.expiresAt });
        if (d.reason === "lock-lost") return refuse(d.reason, d.message, { cause: d.cause });
        return refuse(d.reason, d.message);
      }
      // Taking the lock wakes the pet, as dsh-pet's /lock does: it is not commanded in its sleep.
      companionSleep.wake(COMPANION_PET);
      return ok({ status: "ok", lockId: d.lockId, expiresAt: d.expiresAt });
    }
    default: {
      // lock_release: by its id, or with force (only the product service sends that).
      const d = companionLock.release({ petId: COMPANION_PET, lockId: args.lockId, force: args.force });
      if (!d.ok) return refuse(d.reason, d.message, d.reason === "lock-lost" ? { cause: d.cause } : undefined);
      return ok({ status: "ok" });
    }
  }
}

/** For tests: no command carried out yet this minute. */
export function resetCompanionCommands(): void {
  carriedOut.length = 0;
}
