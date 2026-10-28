// Storage: the data volume, storage plans from planning to running on the
// Host Agent, and conversions after disks were added. The disks themselves
// come with the host state (fixtures.ts).
import type { Disk, StoragePlan, Volume, VolumeLayout } from "../../api.generated";
import { healthyState } from "../fixtures";
import { apiError, http, pathOf } from "../server";

// disk is the fixtures' blank disk under another id.
export function disk(id: string, overrides: Partial<Disk> = {}): Disk {
  return { ...healthyState.disks[0], id, ...overrides };
}

// volume is a mounted data volume on one disk.
export function volume(overrides: Partial<Volume> = {}): Volume {
  return { id: "volume:data", diskIds: ["disk:data"], layout: "single", state: "available", ...overrides };
}

// storagePlan creates a data volume on the fixtures' blank disk; it waits
// for its confirmation and does not expire.
export function storagePlan(id: string, overrides: Partial<StoragePlan> = {}): StoragePlan {
  return {
    id,
    kind: "create_volume",
    layout: "single",
    capacityAfterBytes: 512000000000,
    disks: [
      {
        id: "disk:fake-data-01",
        model: "A-NAS Fake HDD",
        capacityBytes: 512000000000,
        fingerprint: `${id}-fingerprint`,
        signatures: [],
      },
    ],
    confirmationPhrase: "ERASE data-01",
    actions: [],
    state: "planned",
    createdAt: "2026-10-07T10:00:00Z",
    expiresAt: "2099-01-01T00:00:00Z",
    ...overrides,
  };
}

export interface StorageOptions {
  // The data volumes; none unless given.
  volumes?: Volume[];
  // The plans the device still holds for the administrator: the one running,
  // or one that stopped.
  unfinished?: StoragePlan[];
  // The plan a new plan request makes; confirming, running and cancelling
  // answer it in its next state.
  plan?: StoragePlan;
  // Running the plan stops on the device, which then needs attention.
  executeFails?: boolean;
  // What following the plan's operation reads; the plan succeeded unless given.
  operation?: StoragePlan;
}

// storageHandlers answers the storage section. It records the bodies of new
// plans, and every other write and operation read as "METHOD /path" as the
// client wrote it.
export function storageHandlers(options: StorageOptions = {}) {
  const plan = options.plan ?? storagePlan("plan:new");
  const sent = { plans: [] as Array<{ diskIds: string[]; layout?: VolumeLayout }>, requests: [] as string[] };
  const record = (request: Request) => {
    sent.requests.push(`${request.method} ${pathOf(request)}`);
  };
  const handlers = [
    http.get("/api/v1/volumes", ({ response }) => response(200).json({ items: options.volumes ?? [] })),
    http.get("/api/v1/storage/plans", ({ response }) => response(200).json({ items: options.unfinished ?? [] })),
    http.post("/api/v1/storage/plans", async ({ request, response }) => {
      sent.plans.push(await request.json());
      return response(201).json(plan);
    }),
    http.post("/api/v1/storage/plans/{planId}/confirm", ({ request, response }) => {
      record(request);
      return response(200).json({ ...plan, state: "confirmed" });
    }),
    http.post("/api/v1/storage/plans/{planId}/execute", ({ request, response }) => {
      record(request);
      if (options.executeFails) return response(503).json(apiError("operation_needs_attention"));
      return response(200).json({ ...plan, state: "succeeded" });
    }),
    http.post("/api/v1/storage/plans/{planId}/cancel", ({ request, response }) => {
      record(request);
      return response(200).json({ ...plan, state: "cancelled" });
    }),
    http.get("/api/v1/operations/{operationId}", ({ request, response }) => {
      record(request);
      return response(200).json(options.operation ?? { ...plan, state: "succeeded" });
    }),
    http.post("/api/v1/volumes/{volumeId}/conversion", ({ request, response }) => {
      record(request);
      return response(202).empty();
    }),
  ];
  return { sent, handlers };
}
