// Where the page loads a project's media from: frames for shot cards and
// candidates, and the exported film. The page gets these from here, never
// from api/.
import { filmURL, frameURL as served } from "../api/client";

export { filmURL };

// In the Fake preview's build the stand-ins answer fetch, which an <img>
// does not use, so a frame is drawn here instead: the video and the time it
// shows, enough to tell one frame from another.
function drawnFrame(assetId: string, seconds: number): string {
  const label = `${assetId} · ${seconds.toFixed(1)}s`.replace(/[<&>]/g, "");
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 180"><rect width="320" height="180" fill="#2a2f37"/><text x="160" y="96" fill="#9aa1ab" font-size="16" text-anchor="middle">${label}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const drawn = import.meta.env.VITE_VIDEOCUT_STANDINS === "1";

// frameURL is a frame of one of the project's videos, seconds in.
export const frameURL = (workId: string, assetId: string, seconds = 0) =>
  drawn ? drawnFrame(assetId, seconds) : served(workId, assetId, Math.round(seconds * 1000));
