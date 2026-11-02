import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import type { AssistantApproval } from "../api.generated";
import { failureText, listApprovals } from "../assistant/api";
import { useAssistantGateway } from "../assistant/useAssistantGateway";
import { useAssistantThread } from "../assistant/useAssistantThread";
import { taskSummary } from "./taskState";
import type { CompanionView } from "./useCompanionView";

// Presentation only: every turn, file rule and approval is still owned by VNAS.
export function useCompanionTask(view: CompanionView) {
  const gateway = useAssistantGateway();
  const storedId = view.remote.state?.conversationId || undefined;
  const latest = useRef(view);
  useLayoutEffect(() => {
    latest.current = view;
  });
  const [approvals, setApprovals] = useState<AssistantApproval[]>([]);
  const [approvalError, setApprovalError] = useState("");
  const [loadedGeneration, setLoadedGeneration] = useState(-1);
  const cardsLoaded = gateway.ready && loadedGeneration === gateway.generation;
  const version = useRef(0);
  const thread = useAssistantThread(gateway, storedId, {
    onCreated: async (id) => {
      const sync = latest.current.remote.sync;
      if (!sync) throw new Error("Companion disconnected");
      await sync.bindConversation(id);
    },
    onChanged: () => undefined,
    beforePrompt: async () => {
      const current = latest.current;
      if (current.local || !current.remote.here || !current.remote.sync?.get().here) throw new Error("Companion moved");
      if (!current.remote.sync.get().state?.conversationId) throw new Error("Conversation not shared");
    },
  });
  useEffect(
    () =>
      gateway.client.onEvent((event) => {
        if ((event.type as string) !== "anas.approval" || !event.payload) return;
        const card = event.payload as unknown as AssistantApproval;
        version.current++;
        setApprovals((old) => [...old.filter((a) => a.requestId !== card.requestId), card]);
      }),
    [gateway.client],
  );
  useEffect(() => {
    if (!gateway.ready) return;
    let active = true;
    const started = version.current;
    listApprovals()
      .then((cards) => {
        if (!active) return;
        // An event newer than this read wins; retain other records from the read.
        setApprovals((old) =>
          version.current === started
            ? cards
            : [...cards.filter((a) => !old.some((b) => b.requestId === a.requestId)), ...old],
        );
        setApprovalError("");
        setLoadedGeneration(gateway.generation);
      })
      .catch((error) => {
        if (active) setApprovalError(failureText(error));
      });
    return () => {
      active = false;
    };
  }, [gateway.ready, gateway.generation]);
  const own = approvals.filter(
    (a) => (storedId && a.storedSessionId === storedId) || (thread.liveId && a.sessionId === thread.liveId),
  );
  const summary = taskSummary({
    connected: view.remote.connection === "online",
    ready: gateway.ready,
    ...thread,
    error: thread.error || gateway.problem || approvalError,
    approvals: own,
  });
  const waiting = own.some((a) => a.status === "awaiting" || a.status === "deciding");
  const canSend =
    !view.local && view.remote.here && gateway.ready && cardsLoaded && !thread.running && !thread.loading && !waiting;
  const newTask = useCallback(async () => {
    if (!canSend) return;
    await latest.current.remote.sync?.bindConversation("");
  }, [canSend]);
  return { ...summary, thread, storedId, canSend, newTask, waiting, approvals: own };
}
