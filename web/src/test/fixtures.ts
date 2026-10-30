// Test data for the product API, typed by its contract (api.generated.ts). A
// fixture that leaves out what the server always sends does not compile, and
// the client's check of the desktop shell's responses would refuse it.
import type {
  HostStateResponse,
  MetricsResponse,
  Session,
  TaskManagerDeviceResponse,
  TaskManagerTasksResponse,
  User,
} from "../api.generated";

export function user(overrides: Partial<User> = {}): User {
  return {
    id: "user:owner",
    username: "owner",
    role: "admin",
    status: "active",
    createdAt: "2026-10-07T10:00:00Z",
    mustChangePassword: false,
    ...overrides,
  };
}

// session signs in the owner unless account names someone else.
export function session(account: Partial<User> = {}, overrides: Partial<Omit<Session, "user">> = {}): Session {
  return { csrfToken: "csrf-test", expiresAt: "2026-10-07T22:00:00Z", user: user(account), ...overrides };
}

export const healthyState: HostStateResponse = {
  dataSource: "simulated",
  productVersion: "v1.0.1-rc.1",
  observedAt: "2026-10-07T10:00:00Z",
  system: {
    id: "host:fake-01",
    hostname: "anas-fake",
    operatingSystem: { name: "Debian", version: "13" },
    architecture: "amd64",
    uptimeSeconds: 3661,
    health: "healthy",
    healthFindings: [],
    thermal: {
      cpu: { celsius: 48, hotCelsius: 90 },
      gpus: [{ id: "gpu:fake-01", name: "GPU 0 (8086:4692)", integrated: true }],
      fans: [{ id: "fan:fake-01", number: 1, rpm: 1912, stopped: false }],
    },
  },
  disks: [
    {
      id: "disk:fake-data-01",
      model: "A-NAS Fake HDD",
      transport: "sata",
      capacityBytes: 512000000000,
      rotational: true,
      removable: false,
      inUse: false,
      filesystems: [],
      role: "unassigned",
      health: "healthy",
      smartStatus: "healthy",
      eligibleForDataVolume: true,
      ineligibleReasons: [],
      temperatureCelsius: 31,
      temperatureHotCelsius: 55,
    },
  ],
};

export const metricsState: MetricsResponse = {
  dataSource: "simulated",
  observedAt: "2026-10-06T00:00:00Z",
  cpu: { usagePercent: 23.5, logicalCores: 4 },
  memory: { totalBytes: 8589934592, usedBytes: 3435973837 },
  network: { receiveBytesPerSecond: 2516582, transmitBytesPerSecond: 327680 },
};

// The task manager's device and task samples, for its own tests and the desktop
// tests that open it.
export const taskManagerDevice: TaskManagerDeviceResponse = {
  dataSource: "simulated",
  observedAt: "2026-10-15T10:00:00Z",
  sampleSeconds: 2,
  cpu: { model: "Test CPU", logicalCores: 8, usagePercent: 25, temperature: { celsius: 52, hotCelsius: 90 } },
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
      {
        id: "disk:1",
        model: "Test SSD",
        capacityBytes: 8192,
        rotational: false,
        readBytesPerSecond: 2048,
        writeBytesPerSecond: 1024,
        temperature: { celsius: 41, hotCelsius: 70 },
      },
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
        integrated: false,
        temperature: null,
      },
    ],
  },
  npu: { status: "absent", items: [] },
  fans: { status: "available", items: [{ id: "fan:2", number: 2, label: "", rpm: 2700, stopped: false }] },
};
const taskManagerTask = (name: string, pid: number, cpu: number | null, memory: number | null) => ({
  id: `pid:${pid}`,
  name,
  pid,
  state: "running",
  cpuPercent: cpu,
  memoryBytes: memory,
  readBytesPerSecond: memory,
  writeBytesPerSecond: cpu,
});
export const taskManagerTasks: TaskManagerTasksResponse = {
  dataSource: "simulated",
  observedAt: taskManagerDevice.observedAt,
  sampleSeconds: 2,
  services: { status: "available", items: [taskManagerTask("a-nas.service", 100, 10, 1024)] },
  processes: {
    status: "available",
    items: [
      taskManagerTask("two", 2, 2, 1024),
      taskManagerTask("ten", 10, 10, 128),
      taskManagerTask("unknown", 3, null, null),
    ],
  },
};
