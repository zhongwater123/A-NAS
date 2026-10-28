// The task manager: the device and task samples every account reads.
import type { TaskManagerDeviceResponse, TaskManagerTasksResponse } from "../../api.generated";
import { taskManagerDevice, taskManagerTasks } from "../fixtures";
import { http } from "../server";

export interface TaskManagerSamples {
  device?: TaskManagerDeviceResponse;
  tasks?: TaskManagerTasksResponse;
}

// taskManagerHandlers answers both samples from samples, read on every request
// so a test changes them as the host's next observation would; the fixtures
// stand in for what is not given.
export function taskManagerHandlers(samples: TaskManagerSamples = {}) {
  return [
    http.get("/api/v1/task-manager/device", ({ response }) => response(200).json(samples.device ?? taskManagerDevice)),
    http.get("/api/v1/task-manager/tasks", ({ response }) => response(200).json(samples.tasks ?? taskManagerTasks)),
  ];
}
