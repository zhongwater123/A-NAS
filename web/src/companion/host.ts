// The page as 大肥鱼's host keeps what dsh-pet's host keeps beside its GET /state store
// (host/plugin/pet-lookup.ts and host/routes/motion.ts in dsh-pet): the lock and the sleep,
// each writing its pet leaf on every change, and the playback receipts of the animations a
// command asked for. All three are dsh-pet's own stores, vendored from dsh-pet/host.
//
// VNAS's commands write through them (commands.ts), and the card's own actions reach them
// through the page (petCard.tsx answers them): a click wakes the pet, a grab releases the
// lock, and the card's receipts settle the playback a command waits on. Like companionState,
// they live as long as the page; only the VNAS window and the card load them.
import { LockStore } from "./dsh-pet/host/lock";
import { PlaybackStore } from "./dsh-pet/host/playback/store";
import { SleepStore } from "./dsh-pet/host/sleep";
import { companionState } from "./workStatus";

/**
 * Who holds the pet, until when (dsh-pet host/lock.ts); writes pets.<id>.lock. Its clock is read at
 * each call, not taken once, so that a test's clock reaches it as dsh-pet's tests inject theirs.
 */
export const companionLock = new LockStore({
  now: () => Date.now(),
  onChange: (petId, data) => companionState.writePet(petId, "lock", data),
});
/** Whether the pet sleeps and which way (dsh-pet host/sleep.ts); writes pets.<id>.sleep. */
export const companionSleep = new SleepStore({
  onChange: (petId, data) => companionState.writePet(petId, "sleep", data),
});
/** The animations commands asked for and how each one went (dsh-pet host/playback/store.ts). */
export const companionPlayback = new PlaybackStore();

/** For tests: no lock, awake, and no animation waited on. */
export function resetCompanionHost(): void {
  companionPlayback.cancelAll();
  companionLock.releaseAll();
  companionSleep.wakeAll();
}
