import { createContext, useContext } from "react";

export { useWindowShown } from "../../ui/useWindowShown";

// Whether the 剪辑中心 window can be seen (ui/useWindowShown): what polls
// reads this and stops while the window is minimized.
const Shown = createContext(true);
export const ShownProvider = Shown.Provider;
export const useShown = () => useContext(Shown);
