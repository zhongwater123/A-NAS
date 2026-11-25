import { act, fireEvent, render, screen } from "@testing-library/react";
import { Bell } from "lucide-react";
import { useEffect, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DeskNotice } from "./DeskNotice";

describe("a feature's notice on the desktop", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const show = (lines: string[]) => {
    const onOpen = vi.fn();
    const onDismiss = vi.fn();
    render(
      <DeskNotice label="测试提醒" source="测试" icon={<Bell />} lines={lines} onOpen={onOpen} onDismiss={onDismiss} />,
    );
    return { onOpen, onDismiss };
  };

  it("says each line and opens the feature's window after dismissing itself", () => {
    const { onOpen, onDismiss } = show(["第一件事", "第二件事"]);
    expect(screen.getByRole("status", { name: "测试提醒" })).toHaveTextContent("测试第一件事第二件事");
    fireEvent.click(screen.getByRole("button", { name: /去看看/ }));
    expect(onDismiss).toHaveBeenCalledOnce();
    expect(onOpen).toHaveBeenCalledOnce();
    expect(onDismiss.mock.invocationCallOrder[0]).toBeLessThan(onOpen.mock.invocationCallOrder[0]);
  });

  it("shows nothing with nothing to say", () => {
    const { onDismiss } = show([]);
    expect(screen.queryByRole("status")).toBeNull();
    act(() => vi.advanceTimersByTime(60_000));
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("keeps a thing to a line with its note, says what it leaves out, and offers the feature's own action", () => {
    const onAction = vi.fn();
    const onDismiss = vi.fn();
    render(
      <DeskNotice
        label="测试提醒"
        source="测试"
        icon={<Bell />}
        lines={[{ key: "a", text: "交水费", note: "今天截止" }, "还有一句"]}
        more="另有 2 件"
        action={{ label: "知道了", onClick: onAction }}
        onOpen={vi.fn()}
        onDismiss={onDismiss}
      />,
    );
    expect(screen.getByText("交水费").nextSibling?.textContent).toBe("今天截止");
    expect(screen.getByText("还有一句")).toBeTruthy();
    expect(screen.getByText("另有 2 件")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "知道了" }));
    expect(onAction).toHaveBeenCalledOnce();
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("still hides itself while the desktop around it re-renders every 10 seconds", () => {
    const onDismiss = vi.fn();
    function Desktop() {
      const [, setTick] = useState(0);
      useEffect(() => {
        const poll = window.setInterval(() => setTick((tick) => tick + 1), 10_000);
        return () => window.clearInterval(poll);
      }, []);
      // A new callback each render, as the desktop passes its arrows.
      return (
        <DeskNotice
          label="测试提醒"
          source="测试"
          icon={<Bell />}
          lines={["一件事"]}
          onOpen={() => {}}
          onDismiss={() => onDismiss()}
        />
      );
    }
    render(<Desktop />);
    // One second at a time, so each poll's re-render is committed before the next.
    for (let second = 0; second < 14; second++) act(() => vi.advanceTimersByTime(1_000));
    expect(onDismiss).not.toHaveBeenCalled();
    for (let second = 0; second < 2; second++) act(() => vi.advanceTimersByTime(1_000));
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("hides itself after a while, unless the pointer or the focus is on it", () => {
    const { onDismiss } = show(["一件事"]);
    const notice = screen.getByRole("status").parentElement as HTMLElement;
    fireEvent.pointerEnter(notice);
    act(() => vi.advanceTimersByTime(60_000));
    expect(onDismiss).not.toHaveBeenCalled();
    fireEvent.pointerLeave(notice);
    act(() => vi.advanceTimersByTime(14_000));
    expect(onDismiss).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1_000));
    expect(onDismiss).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "关闭" }));
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });
});
