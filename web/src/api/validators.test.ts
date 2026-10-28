import { afterEach, describe, expect, it, vi } from "vitest";

import { http, server } from "../test/server";

describe("response validators", () => {
  afterEach(() => {
    vi.doUnmock("../api.validators.generated");
    vi.resetModules();
  });

  it("are downloaded again after a failed download instead of failing every later call", async () => {
    let online = false;
    vi.doMock("../api.validators.generated", async (original) => {
      if (!online) throw new TypeError("Failed to fetch dynamically imported module");
      return original();
    });
    vi.resetModules();
    // A page that starts while the device is out of reach.
    const { listSpaces } = await import("../files/api");
    server.use(http.get("/api/v1/spaces", ({ response }) => response(200).json({ items: [] })));

    // The browser's TypeError is network_error; the test's mock reports another failure.
    await expect(listSpaces()).rejects.toMatchObject({ status: 0 });
    online = true;
    await expect(listSpaces()).resolves.toEqual([]);
  });
});
