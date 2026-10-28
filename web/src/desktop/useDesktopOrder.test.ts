import { describe, expect, it } from "vitest";

import { mergeOrder, moveItem } from "./useDesktopOrder";

describe("desktop icon order", () => {
  it("keeps stored order, drops unknown and duplicate IDs, and appends new apps", () => {
    expect(mergeOrder(["c", "gone", "a", "c"], ["a", "b", "c", "d"])).toEqual(["c", "a", "b", "d"]);
    expect(mergeOrder(undefined, ["a", "b"])).toEqual(["a", "b"]);
  });

  it("moves an item and clamps the target index", () => {
    expect(moveItem(["a", "b", "c", "d"], "d", 1)).toEqual(["a", "d", "b", "c"]);
    expect(moveItem(["a", "b", "c"], "a", 99)).toEqual(["b", "c", "a"]);
    expect(moveItem(["a", "b", "c"], "b", -5)).toEqual(["b", "a", "c"]);
    expect(moveItem(["a", "b"], "missing", 0)).toEqual(["a", "b"]);
  });
});
