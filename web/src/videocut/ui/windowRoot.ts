import { createContext, type RefObject, useContext } from "react";

// WindowRoot is the 剪辑中心 window's root: dialogs open in it, so they
// cover the window only, as the desktop's other in-window dialogs do, and
// keep the window's --vn-* variables.
export const WindowRoot = createContext<RefObject<HTMLElement | null> | null>(null);
export const useWindowRoot = () => useContext(WindowRoot);
