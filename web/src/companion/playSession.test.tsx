import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Stage } from "./Stage";

const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("a simple proactive companion", () => {
  it("initiates its own surprise without a game menu, then responds to being found and its gift accepted", () => {
    vi.useFakeTimers();
    render(<Stage character="naiwa" quiet={false} />);
    expect(screen.queryByRole("button", { name: "抛接球" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "跟我做" })).not.toBeInTheDocument();
    advance(12000);
    expect(screen.getByRole("status")).toHaveTextContent("小惊喜");
    advance(650);
    fireEvent.click(screen.getByRole("button", { name: "找到躲起来的奶蛙" }));
    expect(screen.getByRole("status")).toHaveTextContent("开心 · 你找到我啦");
    advance(10000);
    expect(screen.getByRole("button", { name: "收下奶蛙送的小花" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "收下奶蛙送的小花" }));
    expect(screen.getByRole("status")).toHaveTextContent("开心 · 你收下了");
    expect(screen.getByRole("button", { name: "收下奶蛙送的小花" })).toBeDisabled();
    advance(4500);
    expect(screen.getByRole("button", { name: "陪我一下" })).toBeVisible();
  });
  it("takes no response neutrally and does not repeat its invitation on this visit", () => {
    vi.useFakeTimers();
    render(<Stage character="naiwa" quiet={false} />);
    advance(12000);
    advance(650);
    advance(20000);
    expect(screen.getByRole("status")).not.toHaveTextContent("失落");
    advance(120000);
    expect(screen.queryByRole("button", { name: "找到躲起来的奶蛙" })).not.toBeInTheDocument();
  });
  it("respects being busy and quiet, while keeping a manual invitation available later", () => {
    vi.useFakeTimers();
    const view = render(<Stage character="naiwa" quiet={false} />);
    advance(12000);
    advance(650);
    fireEvent.click(screen.getByRole("button", { name: "我先忙啦" }));
    expect(screen.getByRole("status")).toHaveTextContent("好，你先忙");
    advance(120000);
    expect(screen.queryByRole("button", { name: "找到躲起来的奶蛙" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "陪我一下" }));
    advance(650);
    view.rerender(<Stage character="naiwa" quiet />);
    expect(screen.getByRole("status")).toHaveTextContent("我在，安静陪你");
    expect(screen.getByRole("button", { name: "陪我一下" })).toBeDisabled();
  });
  it("makes task emotions and comfort clear, settles, and does not replay old success", () => {
    vi.useFakeTimers();
    const view = render(<Stage character="naiwa" quiet={false} taskPhase="success" />);
    expect(screen.getByRole("status")).toHaveTextContent("结果已经在小窝");
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="working" />);
    expect(screen.getByRole("button", { name: "陪我一下" })).toBeDisabled();
    advance(12000);
    expect(screen.queryByRole("button", { name: "找到躲起来的奶蛙" })).not.toBeInTheDocument();
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="success" />);
    expect(screen.getByRole("status")).toHaveTextContent("开心 · 这轮回答完成");
    advance(6500);
    expect(screen.getByRole("status")).toHaveTextContent("结果已经在小窝");
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="error" />);
    expect(screen.getByRole("status")).toHaveTextContent("失落 · 这次任务没完成");
    fireEvent.click(screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" }));
    expect(screen.getByRole("status")).toHaveTextContent("谢谢你安慰我");
    advance(1800);
    expect(screen.getByRole("status")).not.toHaveTextContent("失落");
  });
  it("cancels a surprise as soon as a task needs attention or the holder changes", () => {
    vi.useFakeTimers();
    const view = render(<Stage character="naiwa" quiet={false} />);
    fireEvent.click(screen.getByRole("button", { name: "陪我一下" }));
    advance(650);
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="waiting" />);
    expect(screen.queryByRole("button", { name: "找到躲起来的奶蛙" })).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("轮到你决定");
    view.rerender(<Stage character="naiwa" quiet={false} interactive={false} />);
    advance(120000);
    expect(screen.getByRole("button", { name: "陪我一下" })).toBeDisabled();
  });
});
