import { createContext, useContext } from "react";

// The window keeps one route: the start page, or a project opened. Which
// page a project shows is the server's state's (lib/pages.ts), never the
// page's own: pages only ask to go back or to open a project.
export type Route = { page: "start" } | { page: "work"; workId: string };

export interface Nav {
  toStart: () => void;
  openWork: (workId: string) => void;
}

export const NavContext = createContext<Nav>({ toStart: () => undefined, openWork: () => undefined });
export const useNav = () => useContext(NavContext);
