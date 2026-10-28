import { useCallback, useEffect, useMemo, useState } from "react";

import { errorText } from "../api/errorMessages";
import type { StoragePlan, VolumeLayout } from "../api.generated";
import { cancelStoragePlan, confirmStoragePlan, createStoragePlan, executeStoragePlan, getStoragePlan } from "./api";

export type PlanStep = "plan" | "confirm" | "execute" | "cancel";

export interface StoragePlanFlow {
  plan?: StoragePlan;
  phrase: string;
  busy?: PlanStep;
  error: string;
  setPhrase: (value: string) => void;
  generate: (diskIDs: string[], layout?: VolumeLayout) => Promise<void>;
  confirm: () => Promise<void>;
  // execute starts a confirmed plan, or retries one that needs attention.
  execute: () => Promise<void>;
  // cancel gives up a plan that has not run and forgets it; it reports
  // whether the plan is gone, so the wizard can close.
  cancel: () => Promise<boolean>;
  dismiss: () => void;
  // resume shows a plan that ran on the device while this page was away.
  resume: (plan: StoragePlan) => void;
}

// useStoragePlan belongs to the desktop rather than the storage section: the
// service cannot read back a plan that has not run, so closing settings or
// switching sections must not forget it. A running plan is followed until the
// device finishes it.
export function useStoragePlan(): StoragePlanFlow {
  const [plan, setPlan] = useState<StoragePlan>();
  const [phrase, setPhrase] = useState("");
  const [busy, setBusy] = useState<PlanStep>();
  const [error, setError] = useState("");
  const planID = plan?.id;
  const running = plan?.state === "running";
  useEffect(() => {
    if (!planID || !running) return;
    let current = true;
    const timer = window.setInterval(() => {
      getStoragePlan(planID)
        .then((next) => {
          if (current) setPlan((shown) => (shown?.id === planID ? next : shown));
        })
        // The device runs the plan regardless; the next check may reach it.
        .catch(() => undefined);
    }, 1500);
    return () => {
      current = false;
      window.clearInterval(timer);
    };
  }, [planID, running]);
  const resume = useCallback((found: StoragePlan) => {
    setPlan((shown) => shown ?? found);
    setPhrase("");
    setError("");
  }, []);
  // Settings re-renders with a new flow only when the plan, the phrase or the
  // progress changed, not whenever the desktop does.
  return useMemo(() => {
    // reread is how a plan that stopped is shown: its own failure says why.
    const run = async (step: PlanStep, action: () => Promise<StoragePlan>, reread?: () => Promise<StoragePlan>) => {
      setBusy(step);
      setError("");
      try {
        setPlan(await action());
      } catch (caught) {
        const stopped = reread && (caught as { code?: string } | undefined)?.code === "operation_needs_attention";
        try {
          if (stopped) setPlan(await reread());
          else setError(storageErrorText(caught));
        } catch {
          setError(storageErrorText(caught));
        }
      } finally {
        setBusy(undefined);
      }
    };
    return {
      plan,
      phrase,
      busy,
      error,
      setPhrase,
      resume,
      generate: async (diskIDs, layout) => {
        setPhrase("");
        await run("plan", () => createStoragePlan(diskIDs, layout));
      },
      confirm: async () => {
        if (plan) await run("confirm", () => confirmStoragePlan(plan.id, phrase));
      },
      execute: async () => {
        if (plan)
          await run(
            "execute",
            () => executeStoragePlan(plan.id),
            () => getStoragePlan(plan.id),
          );
      },
      cancel: async () => {
        if (plan?.state !== "planned" && plan?.state !== "confirmed") {
          setPlan(undefined);
          setPhrase("");
          setError("");
          return true;
        }
        setBusy("cancel");
        setError("");
        try {
          await cancelStoragePlan(plan.id);
          setPlan(undefined);
          setPhrase("");
          return true;
        } catch (caught) {
          setError(storageErrorText(caught));
          return false;
        } finally {
          setBusy(undefined);
        }
      },
      dismiss: () => {
        setPlan(undefined);
        setPhrase("");
        setError("");
      },
    };
  }, [plan, phrase, busy, error, resume]);
}

// storageErrorText turns the storage API's error codes into what to do.
export function storageErrorText(error: unknown): string {
  return errorText(error, "storage");
}
