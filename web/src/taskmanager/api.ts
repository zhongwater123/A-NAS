import { apiFetch, type DataSource } from "../api";

export type Availability = "available" | "partial" | "unavailable" | "unsupported" | "absent";
export interface Group<T> {
  status: Availability;
  items: T[];
}
export interface Observation {
  observedAt: string;
  sampleSeconds: number;
  dataSource: DataSource;
}
export interface Network {
  id: string;
  name: string;
  physical: boolean;
  receiveBytes: number;
  transmitBytes: number;
  receiveBytesPerSecond: number | null;
  transmitBytesPerSecond: number | null;
}
export interface Disk {
  id: string;
  model: string;
  capacityBytes: number;
  readBytesPerSecond: number | null;
  writeBytesPerSecond: number | null;
}
export interface Accelerator {
  usageStatus: Availability;
  memoryUsedStatus: Availability;
  memoryTotalStatus: Availability;
  id: string;
  name: string;
  driver: string;
  status: Availability;
  usagePercent: number | null;
  memoryUsedBytes: number | null;
  memoryTotalBytes: number | null;
}
export interface DeviceSnapshot extends Observation {
  cpu: { model: string; logicalCores: number; usagePercent: number | null };
  memory: { totalBytes: number; usedBytes: number };
  network: Group<Network>;
  disks: Group<Disk>;
  gpu: Group<Accelerator>;
  npu: Group<Accelerator>;
}
export interface Task {
  iconId?: string;
  id: string;
  name: string;
  pid: number;
  state: string;
  cpuPercent: number | null;
  memoryBytes: number | null;
  readBytesPerSecond: number | null;
  writeBytesPerSecond: number | null;
}
export interface TaskSnapshot extends Observation {
  icons?: Record<string, string>;
  processes: Group<Task>;
  services: Group<Task>;
}

async function read<T>(part: "device" | "tasks", signal: AbortSignal): Promise<T> {
  const response = await apiFetch(`/api/v1/task-manager/${part}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    credentials: "same-origin",
    signal,
  });
  if (!response.ok) throw new Error("暂时无法读取，请稍后重试");
  return response.json() as Promise<T>;
}
export const readDevice = (signal: AbortSignal) => read<DeviceSnapshot>("device", signal);
export const readTasks = (signal: AbortSignal) => read<TaskSnapshot>("tasks", signal);
