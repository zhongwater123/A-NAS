// swipeStep is where a sideways swipe across a photo viewer goes: 1 to the
// next photo, -1 to the previous, 0 to stay. It counts once it has travelled
// 80px or was flicked faster than 500px a second, in 家庭中心's lightbox and
// the photos window's viewer alike.
export function swipeStep(offsetX: number, velocityX: number): -1 | 0 | 1 {
  if (offsetX < -80 || velocityX < -500) return 1;
  if (offsetX > 80 || velocityX > 500) return -1;
  return 0;
}

interface Point {
  clientX: number;
  clientY: number;
}
export interface SwipeStart {
  x: number;
  y: number;
  at: number;
}

// swipeStart is where a swipe across the photos window's viewer begins: one
// finger only, as two are a pinch, and not on a zoomed photo, which pans, or
// on the video's controls, which keep their own drags (held).
export function swipeStart(touches: ArrayLike<Point>, at: number, held: boolean): SwipeStart | undefined {
  return touches.length === 1 && !held ? { x: touches[0].clientX, y: touches[0].clientY, at } : undefined;
}

// swipeEnd is where that swipe goes as the finger lifts. It must run more
// sideways than up or down, by half again, so scrolling the details is not a
// step.
export function swipeEnd(start: SwipeStart | undefined, end: Point | undefined, at: number): -1 | 0 | 1 {
  if (!start || !end) return 0;
  const dx = end.clientX - start.x;
  const dy = end.clientY - start.y;
  if (Math.abs(dx) <= Math.abs(dy) * 1.5) return 0;
  return swipeStep(dx, (dx / Math.max(1, at - start.at)) * 1000);
}
