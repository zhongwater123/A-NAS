// scrollInside scrolls a window's scrolling area so an element sits near its
// top, or at a share `at` of the area's height, without scrolling the window
// or the desktop around it as scrollIntoView would.
export function scrollInside(container: HTMLElement, element: HTMLElement, smooth: boolean, at = 0): void {
  // jsdom has no scrollTo.
  if (!container.scrollTo) return;
  const offset = element.getBoundingClientRect().top - container.getBoundingClientRect().top;
  container.scrollTo({
    top: container.scrollTop + offset - 16 - at * container.clientHeight,
    behavior: smooth ? "smooth" : "auto",
  });
}
