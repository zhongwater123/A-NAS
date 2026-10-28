import { describe, expect, it } from "vitest";

import { closedWindow, type WindowAction, type WindowModel, windowReducer } from "./windows";

const bounds = { x: 200, y: 60, width: 900, height: 600 };
const desktop = (): WindowModel[] => [closedWindow("files", bounds), closedWindow("trash", bounds)];
const run = (actions: WindowAction[], start = desktop()) => actions.reduce(windowReducer, start);

describe("window reducer", () => {
  it("returns the same state for actions that change nothing", () => {
    const state = run([
      { type: "open", id: "files" },
      { type: "open", id: "trash" },
    ]);
    const unchanged: WindowAction[] = [
      // trash is on top already
      { type: "focus", id: "trash" },
      { type: "open", id: "trash" },
      { type: "move", id: "trash", x: bounds.x, y: bounds.y },
      { type: "resize", id: "files", width: bounds.width, height: bounds.height },
      { type: "restore", ids: [] },
    ];
    for (const action of unchanged) expect(windowReducer(state, action), action.type).toBe(state);

    const minimized = windowReducer(state, { type: "minimize", id: "files" });
    expect(windowReducer(minimized, { type: "minimize", id: "files" })).toBe(minimized);
    const closed = windowReducer(state, { type: "close", id: "files" });
    expect(windowReducer(closed, { type: "close", id: "files" })).toBe(closed);
    const hidden = windowReducer(state, { type: "show-desktop" });
    expect(windowReducer(hidden, { type: "show-desktop" })).toBe(hidden);
  });

  it("keeps the windows an action does not touch", () => {
    const state = run([
      { type: "open", id: "files" },
      { type: "open", id: "trash" },
    ]);
    const moved = windowReducer(state, { type: "move", id: "trash", x: 300, y: 90 });
    expect(moved).not.toBe(state);
    expect(moved[0]).toBe(state[0]);
    expect(moved[1]).toMatchObject({ x: 300, y: 90 });

    const focused = windowReducer(state, { type: "focus", id: "files" });
    expect(focused[0].z).toBeGreaterThan(focused[1].z);
    expect(focused[1]).toBe(state[1]);
  });
});
