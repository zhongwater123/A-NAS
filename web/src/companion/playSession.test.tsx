import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { playDelay, playSession, resting } from "./playSession";
import { Stage } from "./Stage";

const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("reciprocal play", () => {
  it("only credits a returned ball caught in its turn, then completes three rounds", () => {
    vi.useFakeTimers();
    const credit = vi.fn();
    render(<Stage character="naiwa" quiet={false} onPlay={credit} />);
    fireEvent.click(screen.getByRole("button", { name: "抛接球" }));
    fireEvent.click(screen.getByRole("button", { name: "把小球传给伙伴" }));
    expect(credit).not.toHaveBeenCalled();
    for (let round = 0; round < 3; round++) {
      advance(850);
      expect(screen.getByRole("status")).toHaveTextContent("准备传回给你");
      advance(750);
      const ball = screen.getByRole("button", { name: "接住奶蛙传回的小球" });
      fireEvent.click(ball);
      fireEvent.click(ball);
      expect(credit).toHaveBeenCalledTimes(round + 1);
      advance(1000);
    }
    expect(screen.getByRole("status")).toHaveTextContent("三回合结束，接住了 3 次");
  });
  it("can finish a missed round and offers a friendly restart", () => {
    let state = playSession(resting, { type: "start", kind: "ball" });
    for (let round = 0; round < 3; round++)
      for (let phase = 0; phase < 4; phase++) state = playSession(state, { type: "next" });
    expect(state).toMatchObject({ phase: "done", score: 0, round: 3 });
    expect(playDelay({ ...state, phase: "catch" }, true)).toBeUndefined();
  });
  it("shows a sequence, waits for the owner, and adds a beat in each of three rounds", () => {
    vi.useFakeTimers();
    vi.spyOn(Math, "random").mockReturnValue(0);
    render(<Stage character="naiwa" quiet={false} />);
    fireEvent.click(screen.getByRole("button", { name: "跟我做" }));
    expect(screen.getByRole("button", { name: "左边" })).toBeDisabled();
    for (let round = 1; round <= 3; round++) {
      for (let beat = 0; beat < round + 1; beat++) {
        advance(750);
        advance(300);
      }
      expect(screen.getByRole("status")).toHaveTextContent("轮到你");
      for (let beat = 0; beat < round + 1; beat++) fireEvent.click(screen.getByRole("button", { name: "左边" }));
      if (round < 3) advance(1000);
    }
    expect(screen.getByRole("status")).toHaveTextContent("三轮都跟上了");
  });
  it("finishes five breathing cycles and stops play when quiet or moved away", () => {
    vi.useFakeTimers();
    const view = render(<Stage character="naiwa" quiet={false} />);
    fireEvent.click(screen.getByRole("button", { name: "一起放松" }));
    for (let round = 0; round < 5; round++) {
      advance(3000);
      advance(3000);
    }
    expect(screen.getByRole("status")).toHaveTextContent("一起慢下来了");
    fireEvent.click(screen.getByRole("button", { name: "一起放松" }));
    view.rerender(<Stage character="naiwa" quiet />);
    advance(30000);
    expect(screen.getByRole("status")).toHaveTextContent("我在，安静陪你");
    expect(screen.queryByRole("button", { name: "先玩到这里" })).not.toBeInTheDocument();
  });
  it("reacts to a real task phase change once, settles, and does not replay history on mount", () => {
    vi.useFakeTimers();
    const view = render(<Stage character="naiwa" quiet={false} taskPhase="success" />);
    expect(screen.getByRole("status")).toHaveTextContent("结果已经在小窝");
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="working" />);
    expect(screen.getByRole("status")).toHaveTextContent("正在忙这件事");
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="success" />);
    expect(screen.getByRole("status")).toHaveTextContent("这轮回答完成啦");
    advance(6500);
    expect(screen.getByRole("status")).toHaveTextContent("结果已经在小窝");
    view.rerender(<Stage character="naiwa" quiet={false} taskPhase="error" />);
    expect(screen.getByRole("status")).toHaveTextContent("遇到困难");
  });
  it("invites once while idle and respects declining for the current visit", () => {
    vi.useFakeTimers();
    render(<Stage character="naiwa" quiet={false} />);
    advance(45000);
    expect(screen.getByRole("status")).toHaveTextContent("无聊吗");
    fireEvent.click(screen.getByRole("button", { name: "下次再玩" }));
    advance(90000);
    expect(screen.getByRole("status")).not.toHaveTextContent("无聊吗");
  });
});
