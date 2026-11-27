import { useSyncExternalStore } from "react";

// The desktop's phone layout: windows fill the screen above the system bar,
// which lies along the bottom. The one definition: the desktop marks its
// <main> with data-phone from it, the styles read that, and what runs outside
// React (floatingArea) asks phoneLayout(). Tests that stand in for a phone
// match this query.
export const phoneLayoutQuery = "(max-width: 620px), (max-height: 520px)";

export const phoneLayout = () =>
  typeof window !== "undefined" && window.matchMedia?.(phoneLayoutQuery).matches === true;

const subscribe = (change: () => void) => {
  const list = window.matchMedia?.(phoneLayoutQuery);
  list?.addEventListener?.("change", change);
  return () => list?.removeEventListener?.("change", change);
};

// usePhoneLayout says whether the desktop is laid out for a phone, for a
// window that shows a phone its own arrangement rather than a narrow desktop one.
export const usePhoneLayout = () => useSyncExternalStore(subscribe, phoneLayout, () => false);
