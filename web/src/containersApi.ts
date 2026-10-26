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

export const readContainers = (signal?: AbortSignal) => request(client.GET("/api/v1/containers", { signal }));

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
// with the server's code (malformed_response for a body that breaks the
// contract) or network_error when the request went nowhere.
async function request<Data>(pending: Promise<{ data?: Data; error?: unknown; response: Response }>): Promise<Data> {
  try {
    return await call(pending);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    if (error instanceof APIError) throw new ContainerAPIError(error.code, error.status);
    throw new ContainerAPIError(error instanceof TypeError ? "network_error" : "malformed_response");
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
