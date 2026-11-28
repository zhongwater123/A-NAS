// useEditor: every edit of the film and its undo go through one wrapper
// (docs/specs/venus.md#镜头操作). One at a time: while one is being made, or
// the film is not there to edit (生成中, 导出中), the next is refused. Each
// names the timeline's revision; a revision that moved on is read again.
// After an edit the timeline is the server's answer, the preview plays again
// from the first changed shot, and a toast says what was done.
import { useQueryClient } from "@tanstack/react-query";

import { APIError } from "../../api/client";
import { errorText } from "../../api/errorMessages";
import { type Edit, editTimeline, type Timeline, undoTimeline } from "../api/client";
import { messages } from "../ui/messages";
import { useToast } from "../ui/Toast";
import { readOnly, useCut, useCutStore } from "./cutStore";
import { useTimeline } from "./features";
import { keys } from "./works";

export function useEditor(workId: string) {
  const store = useCutStore();
  const toast = useToast();
  const client = useQueryClient();
  const busy = useCut((state) => state.busy);
  const locked = useCut(readOnly);
  // Watches the timeline the film page reads; it does not read it itself.
  const canUndo = (useTimeline(workId, false).data?.canUndo ?? false) && !locked;

  // run makes one edit; it answers whether the edit was made.
  const run = async (done: string, call: (revision: number) => Promise<Timeline>): Promise<boolean> => {
    const state = store.get();
    if (readOnly(state)) {
      toast(state.busy ? messages.busy : state.sample ? messages.sampleReadOnly : messages.readOnly);
      return false;
    }
    const revision = client.getQueryData<Timeline>(keys.timeline(workId))?.revision;
    if (revision === undefined) return false;
    store.set({ busy: true });
    try {
      const timeline = await call(revision);
      client.setQueryData(keys.timeline(workId), timeline);
      // What a slot may take changes with the film: a beat taken out is a
      // candidate again, and the one put in is not. The lock holds until the
      // candidates are read again, so a stale one cannot be clicked.
      await client.invalidateQueries({ queryKey: [...keys.work(workId), "candidates"] });
      const first = timeline.changedSlots?.[0];
      const shot = timeline.shots.find((s) => s.slot === first);
      if (shot) store.set({ playhead: shot.start });
      toast(done);
      return true;
    } catch (error) {
      if (error instanceof APIError && error.code === "timeline_conflict") {
        await client.invalidateQueries({ queryKey: keys.timeline(workId) });
      }
      toast(errorText(error, "videocut"));
      return false;
    } finally {
      store.set({ busy: false });
    }
  };

  const edit = (done: string, change: Edit) => run(done, (revision) => editTimeline(workId, revision, change));

  return {
    replace: (slot: number, beat: string) => edit(messages.done.replace(slot), { kind: "replace", slot, beat }),
    remove: (slot: number) => edit(messages.done.remove(slot), { kind: "remove", slot }),
    setInPoint: (slot: number, from: number) =>
      edit(messages.done.set_in_point(slot), { kind: "set_in_point", slot, from }),
    reorderParts: (from: number, to: number) =>
      edit(messages.done.reorder_parts(), { kind: "reorder_parts", from, to }),
    place: (slot: number, assetId: string) => edit(messages.done.place(slot), { kind: "place", slot, assetId }),
    undo: () => run(messages.undone, (revision) => undoTimeline(workId, revision)),
    canUndo,
    busy,
    locked,
  };
}
