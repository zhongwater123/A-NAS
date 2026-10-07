import { DataSource } from "./api";

export type ContainerState = "created" | "running" | "paused" | "restarting" | "removing" | "exited" | "dead";
export type ContainerAction = "start" | "stop" | "restart";

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: ContainerState;
  status: string;
  createdAt: string;
  project?: string;
  ports: Array<{ hostIp?: string; hostPort?: number; containerPort: number; protocol: string }>;
  usage?: { cpuPercent: number; memoryBytes: number; memoryLimitBytes: number };
}

export interface ContainerImage {
  id: string;
  tags: string[];
  sizeBytes: number;
  createdAt: string;
}

export interface ContainerSnapshot {
  dataSource: DataSource;
  observedAt: string;
  engine: { version: string; apiVersion: string };
  containers: ContainerSummary[];
  images: ContainerImage[];
}

export interface ContainerLogLine {
  stream: "stdout" | "stderr";
  time?: string;
  text: string;
}

// Error codes shared by the product API and the container agent.
export class ContainerAPIError extends Error {
  constructor(readonly code: string, readonly status?: number) {
    super(`container request failed: ${code}`);
  }
}

export async function readContainers(signal?: AbortSignal): Promise<ContainerSnapshot> {
  const value = await request("/api/v1/containers", { signal });
  if (!isSnapshot(value)) throw new ContainerAPIError("malformed_response");
  return value;
}

export async function applyContainerAction(id: string, action: ContainerAction): Promise<void> {
  await request(`/api/v1/containers/${id}/actions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ action }),
  });
}

export async function readContainerLogs(id: string, tail = 200, signal?: AbortSignal): Promise<ContainerLogLine[]> {
  const value = await request(`/api/v1/containers/${id}/logs?tail=${tail}`, { signal });
  if (!isRecord(value) || !Array.isArray(value.lines)) throw new ContainerAPIError("malformed_response");
  return value.lines.filter(
    (line): line is ContainerLogLine => isRecord(line) && (line.stream === "stdout" || line.stream === "stderr") && typeof line.text === "string",
  );
}

async function request(path: string, init: RequestInit): Promise<unknown> {
  let response: Response;
  try {
    response = await fetch(path, { ...init, headers: { Accept: "application/json", ...init.headers } });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new ContainerAPIError("network_error");
  }
  if (response.status === 204) return undefined;
  const body: unknown = await response.json().catch(() => undefined);
  if (!response.ok) {
    const code = isRecord(body) && isRecord(body.error) && typeof body.error.code === "string" ? body.error.code : "request_failed";
    throw new ContainerAPIError(code, response.status);
  }
  return body;
}

function isSnapshot(value: unknown): value is ContainerSnapshot {
  if (!isRecord(value) || !["simulated", "live"].includes(String(value.dataSource)) || !isRecord(value.engine)) return false;
  if (!Array.isArray(value.containers) || !Array.isArray(value.images)) return false;
  return value.containers.every(
    (item) => isRecord(item) && typeof item.id === "string" && typeof item.name === "string" && typeof item.state === "string" && Array.isArray(item.ports),
  );
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
