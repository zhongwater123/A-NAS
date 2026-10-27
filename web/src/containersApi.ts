import { APIError, call, client } from "./api";
import type { Container, ContainerLogsResponse, ContainerSnapshotResponse } from "./api.generated";

export type { ContainerImage } from "./api.generated";
export type ContainerSummary = Container;
export type ContainerSnapshot = ContainerSnapshotResponse;
export type ContainerState = ContainerSummary["state"];
export type ContainerLogLine = ContainerLogsResponse["lines"][number];
export type ContainerAction = "start" | "stop" | "restart";

export const readContainers = (signal?: AbortSignal) => call(client.GET("/api/v1/containers", { signal }));

export async function applyContainerAction(containerId: string, action: ContainerAction): Promise<void> {
  await call(
    client.POST("/api/v1/containers/{containerId}/actions", { params: { path: { containerId } }, body: { action } }),
  );
}

export async function readContainerLogs(
  containerId: string,
  tail = 200,
  signal?: AbortSignal,
): Promise<ContainerLogLine[]> {
  const value: unknown = await call(
    client.GET("/api/v1/containers/{containerId}/logs", { params: { path: { containerId }, query: { tail } }, signal }),
  );
  if (!isRecord(value) || !Array.isArray(value.lines)) throw new APIError(200, "malformed_response");
  return value.lines.filter(
    (line): line is ContainerLogLine =>
      isRecord(line) && (line.stream === "stdout" || line.stream === "stderr") && typeof line.text === "string",
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
