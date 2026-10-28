// Docker: the container snapshot, logs and actions.
import type { Container, ContainerLogsResponse, ContainerSnapshotResponse } from "../../api.generated";
import { http } from "../server";

// container is a running container named name; its id is derived from the name.
export function container(name: string, overrides: Partial<Container> = {}): Container {
  return {
    id: Array.from(name, (char) => char.charCodeAt(0).toString(16))
      .join("")
      .padEnd(64, "0")
      .slice(0, 64),
    name,
    image: `${name}:1`,
    imageId: `sha256:${name}`,
    state: "running",
    status: "Up 2 days",
    createdAt: "2026-10-01T00:00:00Z",
    ports: [],
    ...overrides,
  };
}

// containerSnapshot is a Docker engine with nothing on it unless given.
export function containerSnapshot(overrides: Partial<ContainerSnapshotResponse> = {}): ContainerSnapshotResponse {
  return {
    dataSource: "live",
    observedAt: "2026-10-06T00:00:00Z",
    engine: { version: "29.1.3", apiVersion: "1.52" },
    containers: [],
    images: [],
    ...overrides,
  };
}

// dockerHandlers answers Docker's reads: the snapshot and every container's
// logs. Actions are left to the test, which sees what it asks for.
export function dockerHandlers(snapshot: ContainerSnapshotResponse, logs: ContainerLogsResponse = { lines: [] }) {
  return [
    http.get("/api/v1/containers", ({ response }) => response(200).json(snapshot)),
    http.get("/api/v1/containers/{containerId}/logs", ({ response }) => response(200).json(logs)),
  ];
}

// dockerActions records the actions sent to containers and accepts them.
export function dockerActions() {
  const sent: Array<{ id: string; action: string }> = [];
  const handler = http.post("/api/v1/containers/{containerId}/actions", async ({ params, request, response }) => {
    sent.push({ id: params.containerId, action: (await request.json()).action });
    return response(204).empty();
  });
  return { sent, handler };
}
