import { describe, expect, it } from "vitest";

import type { HostState } from "../api";
import { describeFinding } from "./health";

const disk = (id: string, model: string, rotational: boolean): HostState["disks"][number] => ({
  id, model, transport: rotational ? "sata" : "nvme", capacityBytes: 1, rotational, removable: false, inUse: true, filesystems: [],
  role: rotational ? "data" : "system", health: "healthy", smartStatus: "healthy", eligibleForDataVolume: false, ineligibleReasons: [],
});
const disks = [disk("disk:hdd", "ST500DM002-1BD142", true), disk("disk:ssd", "Acer SSD N3500CN", false)];

describe("describeFinding", () => {
  it("names the disk and the limit for its kind", () => {
    expect(describeFinding({ code: "disk_hot", severity: "warning", diskId: "disk:hdd", value: 57 }, disks)).toMatchObject({
      title: "ST500DM002-1BD142 温度 57 °C，偏高", detail: expect.stringContaining("机械硬盘最好低于 55 °C"),
    });
    expect(describeFinding({ code: "disk_hot", severity: "warning", diskId: "disk:ssd", value: 74 }, disks).detail).toContain("固态硬盘最好低于 70 °C");
    expect(describeFinding({ code: "disk_failing", severity: "critical", diskId: "disk:gone" }, disks).title).toBe("一块硬盘的 SMART 自检未通过");
  });

  it("says what a full volume means for writing", () => {
    expect(describeFinding({ code: "volume_full", severity: "critical", value: 96 }, disks).title).toBe("数据卷已用 96%，新文件会被拒绝");
    expect(describeFinding({ code: "volume_nearly_full", severity: "warning", value: 91 }, disks).title).toBe("数据卷已用 91%");
    expect(describeFinding({ code: "volume_errors", severity: "warning", value: 3 }, disks).detail).toContain("累计");
  });
});
