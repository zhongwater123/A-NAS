// FEEDBACK_UI marks the feedback button, panel and viewer, which a
// screenshot leaves out so it shows the desktop underneath.
export const FEEDBACK_UI = "data-feedback-ui";

// captureDesktop renders the web desktop to a WebP image. It draws the page
// itself rather than asking for screen capture, which browsers only allow
// over HTTPS and the LAN entry is plain HTTP. The renderer loads on first use.
export async function captureDesktop(): Promise<Blob> {
  const root = document.querySelector<HTMLElement>(".desktop-shell") ?? document.body;
  const { domToBlob } = await import("modern-screenshot");
  return domToBlob(root, {
    type: "image/webp",
    quality: 0.9,
    scale: Math.min(window.devicePixelRatio || 1, 2),
    filter: (node) => !(node instanceof Element && node.hasAttribute(FEEDBACK_UI)),
  });
}
