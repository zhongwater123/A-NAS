import { describe, expect, it } from "vitest";

import { problemText, versionLabel, versionParts } from "./texts";

describe("system update words", () => {
  it("reads the versions scripts/release-version.sh writes", () => {
    expect(versionParts("1.0.2")).toEqual({ release: "1.0.2", dev: false });
    expect(versionParts("1.0.1~rc.7")).toEqual({ release: "1.0.1-rc.7", dev: false });
    expect(versionParts("1.0.2~dev20261120.1+gabcdefabcdef")).toEqual({
      release: "1.0.2",
      dev: true,
      date: "2026-11-20",
      commit: "abcdefab",
    });
    expect(versionLabel("1.0.1~rc.6+dev20261111.3+g083bb9db1234")).toBe("1.0.1-rc.6 开发版 083bb9db");
    // A tree without Git builds 0~dev.
    expect(versionLabel("0~dev")).toBe("0 开发版");
  });

  it("says how much system disk space a download still needs", () => {
    expect(problemText({ code: "disk_space", detail: String(3 * 1024 ** 3) })).toBe("系统盘空间不够，还差 3.0 GiB");
    expect(problemText({ code: "disk_space" })).toBe("系统盘空间不够");
  });
});
