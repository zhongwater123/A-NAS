// Test data for the product API, typed by its contract (api.generated.ts). A
// fixture that leaves out what the server always sends does not compile, and
// the client's check of the desktop shell's responses would refuse it.
import type { AppStatus, HostStateResponse, MetricsResponse, Session, User } from "../api.generated";

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

// app is a catalog app as the App Center lists it, not installed.
export function app(id: string, overrides: Partial<AppStatus> = {}): AppStatus {
  return {
    id,
    title: id[0].toUpperCase() + id.slice(1),
    tagline: "",
    description: "",
    notes: "",
    category: "notes",
    version: "1.0",
    author: "",
    developer: "",
    website: "",
    repo: "",
    docs: "",
    updated: "",
    scheme: "http",
    path: "/",
    state: "available",
    running: 0,
    total: 0,
    ...overrides,
  };
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
