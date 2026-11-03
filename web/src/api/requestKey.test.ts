import { afterEach, describe, expect, it, vi } from "vitest";

import { requestKey } from "./requestKey";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("requestKey", () => {
  it("makes a version 4 UUID over LAN HTTP, where randomUUID is missing", () => {
    vi.stubGlobal("crypto", { getRandomValues: crypto.getRandomValues.bind(crypto) });
    const key = requestKey();
    expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(requestKey()).not.toBe(key);
  });

  it("says so when the browser has no random source", () => {
    vi.stubGlobal("crypto", {});
    expect(() => requestKey()).toThrow();
  });
});
