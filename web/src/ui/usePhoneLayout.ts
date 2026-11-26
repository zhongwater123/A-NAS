import { useSyncExternalStore } from "react";

// The desktop's phone layout: windows fill the screen above the system bar.
// The same query is in desktop.module.css and feedback.module.css; CSS cannot
// read it from here, so a change goes to all three.
export const phoneLayoutQuery = "(max-width: 620px), (max-height: 520px)";

const subscribe = (change: () => void) => {
  const list = window.matchMedia?.(phoneLayoutQuery);
  list?.addEventListener?.("change", change);
  return () => list?.removeEventListener?.("change", change);
};
const snapshot = () => window.matchMedia?.(phoneLayoutQuery).matches === true;

// usePhoneLayout says whether the desktop is laid out for a phone, for a
// window that shows a phone its own arrangement rather than a narrow desktop one.
export const usePhoneLayout = () => useSyncExternalStore(subscribe, snapshot, () => false);
