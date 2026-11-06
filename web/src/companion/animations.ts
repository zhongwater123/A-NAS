// The animations the companion plays and how it picks them, from dsh-pet's own
// configuration at the pinned commit (scripts/vendor-dsh-pet.py writes
// dsh-pet/animations.json), narrowed to the animations installed on this NAS.
import config from "./dsh-pet/animations.json";
import type { Animations, EventSlot, Weights } from "./dsh-pet/shared/types";

export const companionWeights: Weights = config.animationWeights;

/**
 * The animation pools with only the installed animations, so a partial install
 * plays what it has; undefined when not even an idle animation is installed.
 */
export function installedAnimations(installed: (name: string) => boolean): Animations | undefined {
  const all = config.animations as unknown as Animations;
  const keep = (names: readonly string[]) => names.filter(installed);
  const idle = keep(all.idle);
  if (idle.length === 0) return undefined;
  // A work stage keeps its place in the list (dsh-pet reads it by index); without
  // its animation the companion stays with an idle one.
  const slot = (value: EventSlot): EventSlot => {
    const names = keep(Array.isArray(value) ? value : [value]);
    return names.length === 0 ? idle[0] : names.length === 1 ? names[0] : names;
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
    events: { workStatus: (all.events.workStatus ?? []).map(slot) },
  };
}
