import { describe, expect, it } from "vitest";

import { messages, pages } from ".";

describe("the copy table", () => {
  it("has each key in one page's file only", () => {
    const keys = Object.values(pages).flatMap((page) => Object.keys(page));
    const twice = keys.filter((key, i) => keys.indexOf(key) !== i);
    expect(twice).toEqual([]);
    expect(Object.keys(messages)).toHaveLength(keys.length);
  });
});
