import { describe, expect, it } from "vitest";

import { batchByDay, maxPhotos } from "./fromLibrary";

const day = (date: string, count: number) =>
  Array.from({ length: count }, (_, i) => ({ id: `${date}-${i}`, day: date }));

describe("batchByDay", () => {
  it("keeps each day's photos in one request where they fit", () => {
    const batches = batchByDay([...day("2026-05-02", 150), ...day("2026-05-01", 30), ...day("2026-05-03", 60)]);
    expect(batches.map((batch) => batch.length)).toEqual([180, 60]);
    expect(batches[1].every((id) => id.startsWith("2026-05-03"))).toBe(true);
  });

  it("splits only a day larger than one request", () => {
    const batches = batchByDay(day("2026-05-01", maxPhotos + 1));
    expect(batches.map((batch) => batch.length)).toEqual([maxPhotos, 1]);
  });
});
