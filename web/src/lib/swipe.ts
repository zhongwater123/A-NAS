// swipeStep is where a sideways swipe across a photo viewer goes: 1 to the
// next photo, -1 to the previous, 0 to stay. It counts once it has travelled
// 80px or was flicked faster than 500px a second, in 家庭中心's lightbox and
// the photos window's viewer alike.
export function swipeStep(offsetX: number, velocityX: number): -1 | 0 | 1 {
  if (offsetX < -80 || velocityX < -500) return 1;
  if (offsetX > 80 || velocityX > 500) return -1;
  return 0;
}
