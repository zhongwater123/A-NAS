import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { assistantHandlers } from "../test/handlers/assistant";
import { server } from "../test/server";
import type { AssistantGateway } from "./useAssistantGateway";
import { useAssistantThread } from "./useAssistantThread";

function fixture(ready = false) {
  const readyHandlers = new Set<() => void>();
  const request = vi.fn(
    async (_method: string, _args: unknown): Promise<unknown> => ({
      session_id: "abcdef12",
      stored_session_id: "20261102_090000_abcdef",
    }),
  );
  const gateway = {
    ready,
    generation: 1,
    problem: "",
    client: {
      request,
      onEvent: () => () => undefined,
      on: (_event: string, fn: () => void) => {
        readyHandlers.add(fn);
        return () => readyHandlers.delete(fn);
      },
    },
  } as unknown as AssistantGateway;
  return {
    gateway,
    request,
    ready: () => {
      for (const fn of readyHandlers) fn();
    },
  };
}
describe("assistant send lifetime", () => {
  it("drops a queued cold-start message when its view closes", async () => {
    const f = fixture();
    const hook = renderHook(() =>
      useAssistantThread(f.gateway, undefined, { onCreated: () => undefined, onChanged: () => undefined }),
    );
    let send!: Promise<void>;
    act(() => {
      send = hook.result.current.send("整理纪要");
    });
    hook.unmount();
    f.ready();
    await act(() => send);
    expect(f.request).not.toHaveBeenCalled();
  });
  it("can stop before a runtime exists without submitting later", async () => {
    const f = fixture();
    const hook = renderHook(() =>
      useAssistantThread(f.gateway, undefined, { onCreated: () => undefined, onChanged: () => undefined }),
    );
    let send!: Promise<void>;
    act(() => {
      send = hook.result.current.send("整理纪要");
    });
    act(() => hook.result.current.cancel());
    f.ready();
    await act(() => send);
    expect(f.request).not.toHaveBeenCalled();
    expect(hook.result.current.running).toBe(false);
  });
  it("waits for shared identity, prevents duplicate sends and drops late prompts after close", async () => {
    const f = fixture(true);
    let release!: () => void;
    const shared = new Promise<void>((resolve) => {
      release = resolve;
    });
    const created = vi.fn(() => shared);
    const hook = renderHook(() =>
      useAssistantThread(f.gateway, undefined, { onCreated: created, onChanged: () => undefined }),
    );
    let send!: Promise<void>;
    act(() => {
      send = hook.result.current.send("整理纪要");
      void hook.result.current.send("重复点击");
    });
    await waitFor(() => expect(created).toHaveBeenCalledOnce());
    expect(f.request).toHaveBeenCalledTimes(1);
    hook.unmount();
    release();
    await act(() => send);
    expect(f.request.mock.calls.map(([method]) => method)).toEqual(["session.create"]);
  });
  it("reloads an answer completed while disconnected", async () => {
    server.use(
      ...assistantHandlers({
        messages: [
          { id: 1, role: "user", content: "整理", timestamp: 1 },
          { id: 2, role: "assistant", content: "已整理摘要", timestamp: 2 },
        ],
      }),
    );
    const f = fixture(true);
    f.request.mockResolvedValue({ session_id: "abcdef12", running: false });
    const hook = renderHook(() =>
      useAssistantThread(f.gateway, "20261102_090000_abcdef", {
        onCreated: () => undefined,
        onChanged: () => undefined,
      }),
    );
    await waitFor(() =>
      expect(
        hook.result.current.messages.some((m) => m.parts.some((p) => p.type === "text" && p.text === "已整理摘要")),
      ).toBe(true),
    );
    expect(hook.result.current.running).toBe(false);
  });
});
