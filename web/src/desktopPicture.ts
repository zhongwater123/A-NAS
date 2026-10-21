// The web desktop as a picture, drawn by the page itself rather than asking for
// screen capture, which browsers allow only over HTTPS while the LAN entry is
// plain HTTP. 问题反馈 attaches it to a report; VNAS looks at it (Hermes's
// browser_vision). The renderer loads on first use.

/**
 * Draws what the browser window shows: the desktop with its windows, and the
 * menus, previews and notices laid over it. Elements `leaveOut` names are not
 * drawn, nor anything inside them; `maxEdge` bounds the picture's longer side.
 */
export async function drawDesktop({ leaveOut, maxEdge }: { leaveOut(element: Element): boolean; maxEdge?: number }): Promise<HTMLCanvasElement> {
  const view = { width: window.innerWidth, height: window.innerHeight };
  const sharp = Math.min(window.devicePixelRatio || 1, 2);
  const { domToCanvas } = await import("modern-screenshot");
  return domToCanvas(document.body, {
    width: view.width,
    height: view.height,
    scale: maxEdge ? Math.min(sharp, maxEdge / Math.max(view.width, view.height)) : sharp,
    // The drawn copy of <body> would take the browser's default 8 px margin
    // and shift the whole desktop; the page itself has none.
    style: { margin: "0" },
    // What lies wholly outside the window cannot show; leaving it out keeps a
    // folder of thousands of files from stalling the page.
    filter: (node) => !(node instanceof Element) || (!leaveOut(node) && onScreen(node, view)),
    features: { restoreScrollPosition: true },
  });
}

/** Whether any of the element shows in the window; a boxless one may hold children that do. */
export function onScreen(element: Element, view: { width: number; height: number }): boolean {
  const { top, left, bottom, right, width, height } = element.getBoundingClientRect();
  return (!width && !height) || (bottom > 0 && right > 0 && top < view.height && left < view.width);
}
