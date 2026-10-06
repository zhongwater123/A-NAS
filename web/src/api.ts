export type Health = "healthy" | "warning" | "critical" | "unknown";
export type DiskRole = "system" | "data" | "unassigned";
export type Transport = "nvme" | "sata" | "usb" | "unknown";
export type DataSource = "simulated" | "live";

export interface HostState {
  dataSource: DataSource;
  productVersion: string;
  observedAt: string;
  system: {
    id: string;
    hostname: string;
    operatingSystem: { name: string; version: string };
    architecture: string;
    uptimeSeconds: number;
    health: Health;
  };
  disks: Array<{
    id: string;
    model: string;
    transport: Transport;
    capacityBytes: number;
    rotational: boolean;
    role: DiskRole;
    health: Health;
    temperatureCelsius?: number;
  }>;
}

export class HostStateError extends Error {
  constructor(readonly status?: number) {
    super(status ? `host state request failed with status ${status}` : "host state request failed");
  }
}

export async function readHostState(signal?: AbortSignal): Promise<HostState> {
  let response: Response;
  try {
    response = await fetch("/api/v1/host-state", {
      method: "GET",
      headers: { Accept: "application/json" },
      signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") {
      throw error;
    }
    throw new HostStateError();
  }
  if (!response.ok) {
    throw new HostStateError(response.status);
  }
  const value: unknown = await response.json();
  if (!isHostState(value)) {
    throw new HostStateError();
  }
  return value;
}

function isHostState(value: unknown): value is HostState {
  if (!isRecord(value) || !["simulated", "live"].includes(String(value.dataSource))) return false;
  if (typeof value.productVersion !== "string" || typeof value.observedAt !== "string") return false;
  if (!isRecord(value.system) || !Array.isArray(value.disks)) return false;
  const system = value.system;
  if (
    typeof system.id !== "string" ||
    typeof system.hostname !== "string" ||
    typeof system.architecture !== "string" ||
    typeof system.uptimeSeconds !== "number" ||
    !isHealth(system.health) ||
    !isRecord(system.operatingSystem) ||
    typeof system.operatingSystem.name !== "string" ||
    typeof system.operatingSystem.version !== "string"
  ) {
    return false;
  }
  return value.disks.every(
    (disk) =>
      isRecord(disk) &&
      typeof disk.id === "string" &&
      typeof disk.model === "string" &&
      ["nvme", "sata", "usb", "unknown"].includes(String(disk.transport)) &&
      typeof disk.capacityBytes === "number" &&
      typeof disk.rotational === "boolean" &&
      ["system", "data", "unassigned"].includes(String(disk.role)) &&
      isHealth(disk.health) &&
      (disk.temperatureCelsius === undefined || typeof disk.temperatureCelsius === "number"),
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isHealth(value: unknown): value is Health {
  return ["healthy", "warning", "critical", "unknown"].includes(String(value));
}
