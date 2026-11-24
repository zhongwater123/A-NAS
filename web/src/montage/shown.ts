import { createContext, type RefObject, useContext, useEffect, useState } from "react";

import { WINDOW_ID } from "../desktop/windowSnapshot";

// Whether the 剪辑中心 window can be seen. The desktop keeps a minimized
// window mounted and only hides its frame (AppWindow), so the window's
// queries would go on asking; what polls reads this and stops meanwhile. A
// page in a background tab is the queries' own to notice
// (refetchIntervalInBackground).
const Shown = createContext(true);
export const ShownProvider = Shown.Provider;
export const useShown = () => useContext(Shown);

// useWindowShown follows the hidden attribute of the app window root is in;
// outside one, as in a test of the panel alone, it is shown.
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
