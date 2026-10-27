// The backup center: each space's snapshots and schedule, what a snapshot
// holds, what is gone from a space, a file's versions and restores. The spaces
// and the folders a restore goes to are the file manager's (filesHandlers).
import type {
  FileEntry,
  RemovedEntry,
  Snapshot,
  SnapshotEntry,
  SnapshotMatch,
  SnapshotRestore,
  SnapshotSchedule,
  SnapshotScheduleSettings,
  Space,
} from "../../api.generated";
import { apiError, http } from "../server";

// schedule is a space's schedule as a space without one reports it: off, daily
// at 03:00, keeping 14, and the caller may change it.
export function schedule(space: Space, overrides: Partial<SnapshotSchedule> = {}): SnapshotSchedule {
  return {
    spaceId: space.id,
    enabled: false,
    frequency: "daily",
    time: "03:00",
    weekday: 0,
    keep: 14,
    retention: "count",
    tiers: { hourly: 0, daily: 0, weekly: 0, monthly: 0 },
    canManage: true,
    ...overrides,
  };
}

// The lists are read on every request, so a test that changes one changes
// what the server says.
export interface BackupOptions {
  snapshots?: Snapshot[];
  schedules?: SnapshotSchedule[];
  // What the snapshots hold, listed by snapshot and parent.
  snapshotEntries?: SnapshotEntry[];
  // What is gone from each space, by space id.
  removed?: Record<string, RemovedEntry[]>;
  // What a search of each space's snapshots finds, by space id; a match's
  // name contains the query.
  matches?: Record<string, SnapshotMatch[]>;
  // The versions the snapshots hold of a file in the space, by file id.
  versions?: (fileId: string) => SnapshotMatch;
}

// backupHandlers answers the backup center's reads. Restores and schedule
// changes are left to the test (restores, scheduleSaves), which sees them.
export function backupHandlers(options: BackupOptions = {}) {
  const { snapshots = [], schedules = [], snapshotEntries = [] } = options;
  return [
    http.get("/api/v1/spaces/{spaceId}/snapshots", ({ params, response }) =>
      response(200).json({ items: snapshots.filter((snapshot) => snapshot.spaceId === params.spaceId) }),
    ),
    http.get("/api/v1/spaces/{spaceId}/snapshot-schedule", ({ params, response }) => {
      const found = schedules.find((item) => item.spaceId === params.spaceId);
      return found ? response(200).json(found) : response(403).json(apiError("forbidden", "not your space's schedule"));
    }),
    http.get("/api/v1/snapshots/{snapshotId}/entries", ({ params, query, response }) => {
      const parentId = query.get("parentId") ?? "";
      return response(200).json({
        items: snapshotEntries.filter(
          (entry) => entry.snapshotId === params.snapshotId && (entry.parentId ?? "") === parentId,
        ),
      });
    }),
    http.get("/api/v1/spaces/{spaceId}/removed-entries", ({ params, response }) =>
      response(200).json({ items: options.removed?.[params.spaceId] ?? [], truncated: false }),
    ),
    http.get("/api/v1/spaces/{spaceId}/snapshot-search", ({ params, query, response }) => {
      const q = query.get("q") ?? "";
      return response(200).json({
        items: (options.matches?.[params.spaceId] ?? []).filter((match) => match.name.includes(q)),
        truncated: false,
      });
    }),
    http.get("/api/v1/files/{fileId}/versions", ({ params, response }) =>
      options.versions
        ? response(200).json(options.versions(params.fileId))
        : response(404).json(apiError("not_found", "no such file")),
    ),
  ];
}

// scheduleSaves records the schedules saved and keeps each in schedules, the
// list backupHandlers reads, as the server would.
export function scheduleSaves(schedules: SnapshotSchedule[]) {
  const sent: SnapshotScheduleSettings[] = [];
  const handler = http.put("/api/v1/spaces/{spaceId}/snapshot-schedule", async ({ params, request, response }) => {
    const settings = await request.json();
    sent.push(settings);
    const index = schedules.findIndex((item) => item.spaceId === params.spaceId);
    if (index < 0) return response(403).json(apiError("forbidden", "not your space's schedule"));
    schedules[index] = { ...schedules[index], ...settings, tiers: settings.tiers ?? schedules[index].tiers };
    return response(200).json(schedules[index]);
  });
  return { sent, handler };
}

export interface Restore {
  snapshotId: string;
  // Absent when the whole snapshot is restored.
  entryId?: string;
  body: SnapshotRestore;
}

// restores records the restores asked for, of an entry or a whole snapshot,
// and answers each with the entry answer makes of it.
export function restores(answer: (restore: Restore) => FileEntry) {
  const sent: Restore[] = [];
  const handlers = [
    http.post("/api/v1/snapshots/{snapshotId}/entries/{entryId}/restore", async ({ params, request, response }) => {
      const restore = { snapshotId: params.snapshotId, entryId: params.entryId, body: await request.json() };
      sent.push(restore);
      return response(201).json(answer(restore));
    }),
    http.post("/api/v1/snapshots/{snapshotId}/restore", async ({ params, request, response }) => {
      const restore = { snapshotId: params.snapshotId, body: await request.json() };
      sent.push(restore);
      return response(201).json(answer(restore));
    }),
  ];
  return { sent, handlers };
}
