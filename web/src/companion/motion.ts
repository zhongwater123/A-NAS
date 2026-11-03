export interface Point {
  x: number;
  y: number;
}
export interface Bounds {
  width: number;
  height: number;
}

export function inside(point: Point, bounds: Bounds): Point {
  return {
    x: Math.max(0, Math.min(Math.max(0, bounds.width), Number.isFinite(point.x) ? point.x : 0)),
    y: Math.max(0, Math.min(Math.max(0, bounds.height), Number.isFinite(point.y) ? point.y : 0)),
  };
}

export interface Flight {
  position: Point;
  velocity: Point;
}

// Relative to the actor: above the head, below it near the top edge, bounded
// by the scene. Both actor and speech share the same moving parent.
export function speechOffset(pet: Point, size: number, scene: Bounds, bubble: Bounds): Point {
  const x = pet.x + size / 2 - bubble.width / 2;
  const above = pet.y - bubble.height - 8;
  const below = pet.y + size + 8;
  const y = above >= 6 ? above : below + bubble.height <= scene.height - 6 ? below : 6;
  const fitted = inside({ x, y }, { width: scene.width - bubble.width - 6, height: scene.height - bubble.height - 6 });
  return { x: Math.max(6, fitted.x) - pet.x, y: Math.max(6, fitted.y) - pet.y };
}

// Small deterministic local flight, independent of renderer/React. Capped
// elapsed time prevents a tab returning from suspension teleporting the pet.
export function advanceFlight(flight: Flight, bounds: Bounds, seconds: number): Flight {
  const dt = Math.max(0, Math.min(0.032, seconds));
  const velocity = { x: flight.velocity.x * Math.exp(-2.4 * dt), y: flight.velocity.y + 900 * dt };
  const next = { x: flight.position.x + velocity.x * dt, y: flight.position.y + velocity.y * dt };
  if (next.x < 0 || next.x > bounds.width) velocity.x *= -0.55;
  if (next.y < 0 || next.y > bounds.height) velocity.y *= -0.38;
  const position = inside(next, bounds);
  if (position.y === bounds.height && Math.abs(velocity.y) < 25) velocity.y = 0;
  if (Math.abs(velocity.x) < 8) velocity.x = 0;
  return { position, velocity };
}
