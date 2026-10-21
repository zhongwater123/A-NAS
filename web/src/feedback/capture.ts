import { drawDesktop } from "../desktopPicture";

// FEEDBACK_UI marks the feedback button, panel and viewer, which a
// screenshot leaves out so it shows the desktop underneath.
export const FEEDBACK_UI = "data-feedback-ui";

// captureDesktop renders the web desktop to a WebP image, as VNAS sees it
// too (desktopPicture.ts).
export async function captureDesktop(): Promise<Blob> {
  const canvas = await drawDesktop({ leaveOut: (element) => element.hasAttribute(FEEDBACK_UI) });
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => (blob ? resolve(blob) : reject(new Error("截图没有生成"))), "image/webp", 0.9);
  });
}
