// WINDOW_ID marks each app window's root so the dock can find it by window id.
export const WINDOW_ID = "data-window-id";

// snapshotWindow draws an open app window into a PNG that fits within the
// given box, for the dock's hover preview. Like the feedback screenshot it
// renders the page itself; the renderer loads on first use.
export async function snapshotWindow(id: string, box: { width: number; height: number }): Promise<string | undefined> {
  const element = document.querySelector<HTMLElement>(`[${WINDOW_ID}="${CSS.escape(id)}"]`);
  if (!element) return undefined;
  const { domToPng } = await import("modern-screenshot");
  // A minimized window has no layout. Lay it out unseen for the capture:
  // transparent, untouchable and without replaying its opening animation.
  const reveal = element.hidden;
  const unseen = { display: "flex", opacity: "0", animation: "none", "pointer-events": "none" };
  if (reveal) {
    for (const [property, value] of Object.entries(unseen)) element.style.setProperty(property, value);
    element.inert = true;
  }
  try {
    const bounds = element.getBoundingClientRect();
    if (!bounds.width || !bounds.height) return undefined;
    return await domToPng(element, {
      scale: Math.min(box.width / bounds.width, box.height / bounds.height) * Math.min(window.devicePixelRatio || 1, 2),
      style: { opacity: "1", animation: "none", transform: "none" },
      features: { restoreScrollPosition: true },
      // Content past the window's bottom or right edge cannot show; leaving
      // it out keeps a folder of thousands of files from stalling the page
      // (3000 rows: 0.4 s instead of 10 s). Content above stays, so scrolled
      // lists keep their offset. Boxless elements such as display: contents
      // may still hold visible children.
      filter: (node) => {
        if (!(node instanceof Element)) return true;
        const { top, left, width, height } = node.getBoundingClientRect();
        return (!width && !height) || (top < bounds.bottom && left < bounds.right);
      },
      timeout: 4000,
    });
  } finally {
    if (reveal) {
      for (const property of Object.keys(unseen)) element.style.removeProperty(property);
      element.inert = false;
    }
  }
}
