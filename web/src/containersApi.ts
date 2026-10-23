import { APIError, call, client } from "./api";
import type { Container, ContainerLogsResponse, ContainerSnapshotResponse } from "./api.generated";

export type { ContainerImage } from "./api.generated";
export type ContainerSummary = Container;
export type ContainerSnapshot = ContainerSnapshotResponse;
export type ContainerState = ContainerSummary["state"];
export type ContainerLogLine = ContainerLogsResponse["lines"][number];
export type ContainerAction = "start" | "stop" | "restart";

// Error codes shared by the product API and the container agent.
export class ContainerAPIError extends Error {
  constructor(
    readonly code: string,
    readonly status?: number,
  ) {
    super(`container request failed: ${code}`);
  }
}

export async function readContainers(signal?: AbortSignal): Promise<ContainerSnapshot> {
  const value = await request(client.GET("/api/v1/containers", { signal }));
  if (!isSnapshot(value)) throw new ContainerAPIError("malformed_response");
  return value;
}

export async function applyContainerAction(containerId: string, action: ContainerAction): Promise<void> {
  await request(
    client.POST("/api/v1/containers/{containerId}/actions", { params: { path: { containerId } }, body: { action } }),
  );
}

export async function readContainerLogs(
  containerId: string,
  tail = 200,
  signal?: AbortSignal,
): Promise<ContainerLogLine[]> {
  const value: unknown = await request(
    client.GET("/api/v1/containers/{containerId}/logs", { params: { path: { containerId }, query: { tail } }, signal }),
  );
  if (!isRecord(value) || !Array.isArray(value.lines)) throw new ContainerAPIError("malformed_response");
  return value.lines.filter(
    (line): line is ContainerLogLine =>
      isRecord(line) && (line.stream === "stdout" || line.stream === "stderr") && typeof line.text === "string",
  );
}

// request unwraps a container call: a failed one throws a ContainerAPIError
// with the server's code, network_error when the request went nowhere and
// malformed_response when the body was not JSON.
async function request<Data>(pending: Promise<{ data?: Data; error?: unknown; response: Response }>): Promise<Data> {
  try {
    return await call(pending);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    if (error instanceof APIError) throw new ContainerAPIError(error.code, error.status);
    throw new ContainerAPIError(error instanceof TypeError ? "network_error" : "malformed_response");
  }
}

function isSnapshot(value: unknown): value is ContainerSnapshot {
  if (!isRecord(value) || !["simulated", "live"].includes(String(value.dataSource)) || !isRecord(value.engine))
    return false;
  if (!Array.isArray(value.containers) || !Array.isArray(value.images)) return false;
  return value.containers.every(
    (item) =>
      isRecord(item) &&
      typeof item.id === "string" &&
      typeof item.name === "string" &&
      typeof item.state === "string" &&
      Array.isArray(item.ports),
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
