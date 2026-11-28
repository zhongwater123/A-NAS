import { createContext, useContext } from "react";

// ActionLock says whether the actions under it are locked: on the film page,
// while an edit is being made, the film is not made yet, or it is exporting.
// ActionButton reads it, so feature components never check the lock themselves.
export const ActionLock = createContext(false);
export const useActionLock = () => useContext(ActionLock);
