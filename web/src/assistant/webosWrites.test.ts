import { describe, expect, it } from "vitest";

import { writeRisk } from "./webosWrites";

describe("writes a VNAS step may cause without asking", () => {
  it("lets the companion's timer writes through", () => {
    for (const action of ["claim", "heartbeat", "release"])
      expect(writeRisk("POST", `/api/v1/companion/${action}`)).toBeUndefined();
  });
  it("still asks about the companion's actions, writes it does not list, and deletes", () => {
    expect(writeRisk("POST", "/api/v1/companion/actions")).toBe("这一步会改动 NAS 上的内容");
    expect(writeRisk("POST", "/api/v1/companion/other")).toBe("这一步会改动 NAS 上的内容");
    expect(writeRisk("DELETE", "/api/v1/companion")).toBe("这一步会删除内容");
  });
});
