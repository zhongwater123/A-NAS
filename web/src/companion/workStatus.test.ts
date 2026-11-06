import { describe, expect, it } from "vitest";

import { WORK_STATUS_STATES } from "./dsh-pet/shared/work-status";
import { workStatusOf } from "./workStatus";

describe("the companion's work status", () => {
  it("passes VNAS's stage to dsh-pet without what the turn is about", () => {
    expect(workStatusOf({ state: "working", seq: 7 })).toEqual({
      workStatus: { state: "working", task: null },
      tick: 7,
    });
    expect(workStatusOf({ state: null, seq: 8 }).workStatus.state).toBeNull();
  });

  it("uses only stages dsh-pet has animations for", () => {
    for (const state of ["thinking", "working", "result", "waiting", "success", "error"] as const)
      expect(WORK_STATUS_STATES).toContain(workStatusOf({ state, seq: 1 }).workStatus.state);
  });
});
