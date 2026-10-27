import { call, client } from "../api/client";
import type { TaskManagerDeviceResponse, TaskManagerProcessGroup, TaskManagerStatus } from "../api.generated";

export type {
  TaskManagerAccelerator as Accelerator,
  TaskManagerDeviceResponse as DeviceSnapshot,
  TaskManagerDisk as Disk,
  TaskManagerNetwork as Network,
  TaskManagerTask as Task,
  TaskManagerTasksResponse as TaskSnapshot,
} from "../api.generated";
export type Availability = TaskManagerStatus;
// Every group of the device and task snapshots: a status and its items.
export type Group<T> = Omit<TaskManagerProcessGroup, "items"> & { items: T[] };
export type Observation = Pick<TaskManagerDeviceResponse, "observedAt" | "sampleSeconds" | "dataSource">;

export const readDevice = (signal: AbortSignal) => call(client.GET("/api/v1/task-manager/device", { signal }));
export const readTasks = (signal: AbortSignal) => call(client.GET("/api/v1/task-manager/tasks", { signal }));
