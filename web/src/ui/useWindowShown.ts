import { type RefObject, useEffect, useState } from "react";

// The attribute the desktop marks each app window's root with
// (desktop/windowSnapshot.ts); ui does not import the desktop.
const WINDOW_ID = "data-window-id";

// useWindowShown follows the hidden attribute of the app window root is in;
// outside one, as in a test of a panel alone, it is shown. The desktop keeps
// a minimized window mounted and only hides its frame (AppWindow), so what a
// window polls reads this and stops meanwhile. A page in a background tab is
// the queries' own to notice (refetchIntervalInBackground).
export function useWindowShown(root: RefObject<HTMLElement | null>): boolean {
  const [shown, setShown] = useState(true);
  useEffect(() => {
    const frame = root.current?.closest(`[${WINDOW_ID}]`);
    if (!frame) return;
    const read = () => setShown(!frame.hasAttribute("hidden"));
    read();
    const observer = new MutationObserver(read);
    observer.observe(frame, { attributes: true, attributeFilter: ["hidden"] });
    return () => observer.disconnect();
  }, [root]);
  return shown;
}
