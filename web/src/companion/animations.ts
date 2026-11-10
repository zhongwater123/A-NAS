// The animations the companion plays and how it picks them, from dsh-pet's own
// configuration at the pinned commit (scripts/vendor-dsh-pet.py writes
// dsh-pet/animations.json), narrowed to the animations installed on this NAS.
import config from "./dsh-pet/animations.json";
import type { Animations, EventSlot, PhysicsParams, Weights } from "./dsh-pet/shared/types";

export const companionWeights: Weights = config.animationWeights;
/** The lines the companion's bubble picks from for each VNAS stage, from the same configuration. */
export const workStatusTexts: string[][] = config.workStatusTexts;
/**
 * dsh-pet's physics for a throw, without falling the way a phone tilts or flying when it is shaken: the
 * spec has neither, and a tilted tablet would keep a frame loop running for as long as it lies there.
 */
export const companionPhysics: PhysicsParams = { ...config.physics, tiltGravity: false, shakeThrow: false };

/**
 * The animation pools with only the installed animations, so a partial install
 * plays what it has; undefined when not even an idle animation is installed.
 */
export function installedAnimations(installed: (name: string) => boolean): Animations | undefined {
  const all = config.animations as unknown as Animations;
  const keep = (names: readonly string[]) => names.filter(installed);
  const idle = keep(all.idle);
  if (idle.length === 0) return undefined;
  // A work stage keeps its place in the list (dsh-pet reads it by index). Without
  // its animation the slot is empty and the companion carries on as it was: an
  // idle animation put there would count as the stage's, end as an event and
  // never hand back to the random chain.
  const slot = (value: EventSlot): EventSlot => {
    const names = keep(Array.isArray(value) ? value : [value]);
    return names.length === 1 ? names[0] : names;
  };
  return {
    idle,
    turn: keep(all.turn),
    drag: keep(all.drag),
    clicks: keep(all.clicks),
    moves: { ...all.moves, actions: all.moves.actions.filter((action) => installed(action.name)) },
    categories: all.categories
      .map((category) => ({ ...category, actions: keep(category.actions) }))
      .filter((category) => category.actions.length > 0),
    events: {
      // What the companion plays as it says something: a pool to pick from, so only installed ones stay.
      whisper: (all.events.whisper ?? []).map(slot).filter((value) => value.length > 0),
      workStatus: (all.events.workStatus ?? []).map(slot),
    },
    // A way to sleep plays its start, loop and end together, so only a whole one stays.
    sleep: (all.sleep ?? [])
      .map((set) => ({ start: keep(set.start), loop: keep(set.loop), end: keep(set.end) }))
      .filter((set) => set.start.length > 0 && set.loop.length > 0 && set.end.length > 0),
  };
}
