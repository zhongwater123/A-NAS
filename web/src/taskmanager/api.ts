import { client } from "../api";
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

async function read<Data>(pending: Promise<{ data?: Data; response: Response }>): Promise<Data> {
  const { data, response } = await pending;
  if (!response.ok) throw new Error("暂时无法读取，请稍后重试");
  return data as Data;
}
export const readDevice = (signal: AbortSignal) => read(client.GET("/api/v1/task-manager/device", { signal }));
export const readTasks = (signal: AbortSignal) => read(client.GET("/api/v1/task-manager/tasks", { signal }));
