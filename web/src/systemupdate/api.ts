import { call, client } from "../api/client";
import type {
  SystemUpdateAction,
  SystemUpdatePhase,
  SystemUpdateProblem,
  SystemUpdateRecord,
  SystemUpdateSettings,
  SystemUpdateStatus,
} from "../api.generated";

export type {
  SystemUpdateAction,
  SystemUpdatePhase,
  SystemUpdateProblem,
  SystemUpdateRecord,
  SystemUpdateSettings,
  SystemUpdateStatus,
};

// The system update service (docs/specs/system-update.md), for administrators.

export const readSystemUpdate = (): Promise<SystemUpdateStatus> => call(client.GET("/api/v1/system/update"));

export const requestSystemUpdate = (action: SystemUpdateAction["action"]): Promise<SystemUpdateStatus> =>
  call(client.POST("/api/v1/system/update/actions", { body: { action } }));

export const updateSystemUpdateSettings = (settings: SystemUpdateSettings): Promise<SystemUpdateStatus> =>
  call(client.PUT("/api/v1/system/update/settings", { body: settings }));
