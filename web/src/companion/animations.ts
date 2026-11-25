// The animations the companion plays and how it picks them, from dsh-pet's own
// configuration at the pinned commit (scripts/vendor-dsh-pet.py writes
// dsh-pet/animations.json), narrowed to the animations installed on this NAS.
//
// The companion is quiet on its own and plays when the member clicks it: what moves it is
// what VNAS does, each step with its own clip (behaviors.ts), and those clips play only
// for a step.
import { TASK_CLIPS } from "./behaviors";
import config from "./dsh-pet/animations.json";
import type { Animations, EventSlot, PhysicsParams, Weights } from "./dsh-pet/shared/types";

/**
 * Mostly idle, an occasional step, and a category action on 2 % of the rolls: about one every eight
 * minutes while the member uses the page, as an idle clip lasts about 10 s (the card rests after a minute
 * without input). dsh-pet weighs idle 10, turn 5, move 5, which plays a category action every few
 * seconds. No turn: dsh-pet's one turn clip, 东张西望, now shows VNAS looking at the desktop, and the
 * card tells a turn by its clip, so an idle clip standing in for it would turn the pet at every end.
 */
export const companionWeights: Weights = { idle: 97, turn: 0, move: 1 };
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
  /** Not a step's clip: those play for VNAS's steps only (behaviors.ts). */
  const random = (name: string) => !TASK_CLIPS.has(name);
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
  const categories = all.categories
    .map((category) => ({ ...category, actions: keep(category.actions).filter(random) }))
    .filter((category) => category.actions.length > 0);
  const stages = all.events.workStatus ?? [];
  const whisper = all.events.whisper ?? [];
  return {
    idle,
    turn: keep(all.turn).filter(random),
    drag: keep(all.drag),
    // A click that has nothing to ask about plays a click response (petCard.tsx asks first); the
    // category actions are left to the random chain.
    clicks: keep(all.clicks),
    moves: { ...all.moves, actions: all.moves.actions.filter((action) => installed(action.name)) },
    categories,
    events: {
      // What the companion plays as it says something: a pool to pick from, so only installed ones stay.
      whisper: whisper.map(slot).filter((value) => value.length > 0),
      workStatus: stages.map(slot),
      // The steps' own clips that are not a stage's: in the pet's pools, so that the card plays them for a
      // step (patch 0028), and as events, so that one ends back in the stage or idle, not the random chain.
      steps: keep([...TASK_CLIPS].filter((name) => ![...stages, ...whisper].flat().includes(name))),
    },
    // A way to sleep plays its start, loop and end together, so only a whole one stays.
    sleep: (all.sleep ?? [])
      .map((set) => ({ start: keep(set.start), loop: keep(set.loop), end: keep(set.end) }))
      .filter((set) => set.start.length > 0 && set.loop.length > 0 && set.end.length > 0),
  };
}
