import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { expect, it } from "vitest";

import type { Timeline } from "../api/client";
import { CutProvider, createCutStore } from "./cutStore";
import { useFollowSelection } from "./player";
import { keys } from "./works";

const shot = (slot: number) => ({
  slot,
  part: 1,
  start: (slot - 1) * 2,
  end: slot * 2,
  beat: `a/${slot}`,
  assetId: "a",
  from: 0,
  to: 2,
  speed: 1,
  anchor: false,
  fitted: false,
});

it("moves the playhead to a shot when it is selected, and not again when the film changes", () => {
  const client = new QueryClient();
  const timeline = { revision: 1, duration: 10, parts: [], shots: [1, 2, 3, 4, 5].map(shot), canUndo: false };
  client.setQueryData(keys.timeline("w"), timeline as unknown as Timeline);
  const store = createCutStore({ mode: "ready" });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <CutProvider store={store}>{children}</CutProvider>
    </QueryClientProvider>
  );
  renderHook(() => useFollowSelection("w"), { wrapper });
  act(() => store.set({ selectedSlot: 3 }));
  expect(store.get().playhead).toBe(4);
  // An edit plays again from another shot, and the timeline changes.
  act(() => {
    store.set({ playhead: 8 });
    client.setQueryData(keys.timeline("w"), {
      ...timeline,
      revision: 2,
      shots: [...timeline.shots],
    } as unknown as Timeline);
  });
  expect(store.get().playhead).toBe(8);
});
