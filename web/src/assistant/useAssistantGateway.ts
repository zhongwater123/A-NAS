// One connection per assistant window to the member's own Hermes, over the
// product service's /api/v1/assistant/ws, with Hermes Desktop's JSON-RPC client
// and its reconnect backoff. The product service passes only the allowed part of
// `hermes serve` and closes the connection when the member's session ends.
import { useEffect, useMemo, useState } from "react";

import { assistantStatus, failureText, gatewayUrl } from "./assistantApi";
import { JsonRpcGatewayClient } from "./hermes/shared/json-rpc-gateway";
import { isStableOpen, reconnectBackoffDelayMs } from "./hermes/shared/reconnect-backoff";

export interface AssistantGateway {
  client: JsonRpcGatewayClient;
  /** Counts the instance's gateway.ready events: each is a (re)connection that open conversations resume on. */
  generation: number;
  ready: boolean;
  /** Why the assistant cannot be reached, when it keeps failing. */
  problem: string;
}

export function useAssistantGateway(): AssistantGateway {
  const client = useMemo(() => new JsonRpcGatewayClient(), []);
  const [generation, setGeneration] = useState(0);
  const [ready, setReady] = useState(false);
  const [problem, setProblem] = useState("");

  useEffect(() => {
    let stopped = false;
    let attempt = 0;
    let openedAt: number | null = null;
    let timer: number | undefined;
    const schedule = () => {
      if (stopped || timer !== undefined) return;
      // A socket that dropped right after opening counts as a failed attempt.
      if (isStableOpen(openedAt)) attempt = 0;
      openedAt = null;
      timer = window.setTimeout(() => { timer = undefined; void dial(); }, reconnectBackoffDelayMs(attempt++));
    };
    const explain = () => {
      // The socket says nothing about why it closed; the status route does, and a
      // session that ended is handled there like everywhere else on the desktop.
      assistantStatus()
        .then((status) => { if (!stopped) setProblem(status.available ? "" : "你的账号还没有 VNAS"); })
        .catch((reason) => { if (!stopped) setProblem(failureText(reason)); });
    };
    const dial = async () => {
      if (stopped) return;
      try {
        await client.connect(gatewayUrl());
      } catch {
        explain();
        schedule();
      }
    };
    const offReady = client.on("gateway.ready", () => {
      setReady(true);
      setProblem("");
      setGeneration((count) => count + 1);
    });
    const offState = client.onState((state) => {
      if (state === "open") openedAt = Date.now();
      if (state === "closed" || state === "error") {
        setReady(false);
        if (attempt > 1) explain();
        schedule();
      }
    });
    void dial();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
      offReady();
      offState();
      client.close();
    };
  }, [client]);

  return { client, generation, ready, problem };
}
