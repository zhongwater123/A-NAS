// VNAS's stage as dsh-pet's work status, which picks the animation the
// companion plays (dsh-pet/shared/work-status.ts). The task stays empty: the
// host's screen is the family's, so the companion shows how VNAS is doing and
// never what it is doing it about.
import { type AssistantActivitySnapshot, useAssistantActivity } from "../assistant/assistantActivity";
import type { WorkStatusSnapshot } from "./dsh-pet/shared/work-status";

export interface CompanionWorkStatus {
  workStatus: WorkStatusSnapshot;
  /** Grows with every change, as dsh-pet's workStatusTick, so a stage reached again replays. */
  tick: number;
}

export function workStatusOf(activity: AssistantActivitySnapshot): CompanionWorkStatus {
  return { workStatus: { state: activity.state, task: null }, tick: activity.seq };
}

export function useCompanionWorkStatus(): CompanionWorkStatus {
  return workStatusOf(useAssistantActivity());
}
