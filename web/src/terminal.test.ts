import { describe, expect, it } from "vitest";

import { exitCodeFromReason, resizeMessage, terminalSessionURL } from "./terminal";

describe("terminal protocol", () => {
  it("connects over the page's own origin", () => {
    expect(terminalSessionURL({ protocol: "http:", host: "127.0.0.1:8080" })).toBe("ws://127.0.0.1:8080/api/v1/terminal/session");
    expect(terminalSessionURL({ protocol: "https:", host: "localhost:8443" })).toBe("wss://localhost:8443/api/v1/terminal/session");
  });

  it("encodes resize control frames", () => {
    expect(JSON.parse(resizeMessage(120, 40))).toEqual({ type: "resize", cols: 120, rows: 40 });
  });

  it("reads the shell exit code from the close reason", () => {
    expect(exitCodeFromReason("exit 0")).toBe(0);
    expect(exitCodeFromReason("exit 130")).toBe(130);
    expect(exitCodeFromReason("session closed")).toBeUndefined();
  });
});
