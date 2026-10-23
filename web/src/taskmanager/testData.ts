// Task manager samples shared by its own tests and the desktop tests that open
// it, in the shape the product API returns.

export const device = {
  dataSource: "simulated",
  observedAt: "2026-10-15T10:00:00Z",
  sampleSeconds: 2,
  cpu: { model: "Test CPU", logicalCores: 8, usagePercent: 25 },
  memory: { totalBytes: 8192, usedBytes: 2048 },
  network: {
    status: "available",
    items: [
      {
        id: "net:1",
        name: "eth0",
        physical: true,
        receiveBytes: 2048,
        transmitBytes: 1024,
        receiveBytesPerSecond: 1024,
        transmitBytesPerSecond: 512,
      },
    ],
  },
  disks: {
    status: "available",
    items: [
      { id: "disk:1", model: "Test SSD", capacityBytes: 8192, readBytesPerSecond: 2048, writeBytesPerSecond: 1024 },
    ],
  },
  gpu: {
    status: "partial",
    items: [
      {
        id: "gpu:1",
        name: "Test GPU",
        driver: "test",
        status: "unsupported",
        usageStatus: "unsupported",
        memoryUsedStatus: "unsupported",
        memoryTotalStatus: "unsupported",
        usagePercent: null,
        memoryUsedBytes: null,
        memoryTotalBytes: null,
      },
    ],
  },
  npu: { status: "absent", items: [] },
};
const task = (name: string, pid: number, cpu: number | null, memory: number | null) => ({
  id: `pid:${pid}`,
  name,
  pid,
  state: "running",
  cpuPercent: cpu,
  memoryBytes: memory,
  readBytesPerSecond: memory,
  writeBytesPerSecond: cpu,
});
export const tasks = {
  dataSource: "simulated",
  observedAt: device.observedAt,
  sampleSeconds: 2,
  services: { status: "available", items: [task("a-nas.service", 100, 10, 1024)] },
  processes: {
    status: "available",
    items: [task("two", 2, 2, 1024), task("ten", 10, 10, 128), task("unknown", 3, null, null)],
  },
};
