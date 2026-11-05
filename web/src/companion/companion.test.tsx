import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { Character } from "./Character";
import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import { advanceFlight, inside, speechOffset } from "./motion";
import { initialPreferences, preferenceKey, readPreferences, writePreferences } from "./preferences";
import { Stage } from "./Stage";

describe("local companion preferences", () => {
  it("keeps members separate and restores the same member", () => {
    localStorage.clear();
    writePreferences("alice", { character: "chestnut", quiet: true, place: "desk" }, localStorage);
    expect(readPreferences("bob", localStorage)).toEqual(initialPreferences);
    expect(readPreferences("alice", localStorage)).toEqual({ character: "chestnut", quiet: true, place: "desk" });
  });
  it("rejects unknown values and survives unavailable storage", () => {
    localStorage.setItem(`${preferenceKey("invalid")}place`, "another-account");
    localStorage.setItem(`${preferenceKey("invalid")}character`, "<script>");
    expect(readPreferences("invalid", localStorage)).toEqual(initialPreferences);
    const denied = {
      getItem() {
        throw new Error("denied");
      },
    };
    expect(readPreferences("private", denied)).toEqual(initialPreferences);
    expect(() =>
      writePreferences("private", initialPreferences, {
        setItem() {
          throw new Error("full");
        },
      }),
    ).not.toThrow();
  });
});

describe("flight boundaries", () => {
  it("bounces off the edge and lands within the visible space", () => {
    let flight = { position: { x: 40, y: 20 }, velocity: { x: 1000, y: -200 } };
    let bounced = false;
    for (let i = 0; i < 500; i++) {
      const previous = flight.velocity.x;
      flight = advanceFlight(flight, { width: 100, height: 80 }, 0.016);
      if (previous > 0 && flight.velocity.x < 0) bounced = true;
      expect(flight.position.x).toBeGreaterThanOrEqual(0);
      expect(flight.position.x).toBeLessThanOrEqual(100);
      expect(flight.position.y).toBeLessThanOrEqual(80);
    }
    expect(bounced).toBe(true);
    expect(flight.position.y).toBe(80);
    expect(flight.velocity).toEqual({ x: 0, y: 0 });
  });
  it("caps suspended time and handles a container smaller than the pet", () => {
    const flight = { position: { x: 0, y: 0 }, velocity: { x: 100, y: 0 } };
    expect(advanceFlight(flight, { width: 100, height: 80 }, 300)).toEqual(
      advanceFlight(flight, { width: 100, height: 80 }, 0.032),
    );
    expect(inside({ x: Infinity, y: 100 }, { width: -20, height: 0 })).toEqual({ x: 0, y: 0 });
  });
});

describe("speech follows the pet without leaving the scene", () => {
  it("stays centered above a pet with room overhead", () => {
    const pet = { x: 150, y: 100 };
    const offset = speechOffset(pet, 160, { width: 600, height: 300 }, { width: 240, height: 36 });
    expect(pet.x + offset.x + 120).toBe(pet.x + 80);
    expect(pet.y + offset.y + 36).toBeLessThan(pet.y);
  });
  it("keeps both left and right edge speech inside the scene", () => {
    for (const x of [0, 440]) {
      const offset = speechOffset({ x, y: 100 }, 160, { width: 600, height: 300 }, { width: 240, height: 36 });
      expect(x + offset.x).toBeGreaterThanOrEqual(6);
      expect(x + offset.x + 240).toBeLessThanOrEqual(594);
    }
  });
  it("moves speech below the pet at the top and fits taller text", () => {
    const offset = speechOffset({ x: 100, y: 0 }, 160, { width: 600, height: 300 }, { width: 240, height: 70 });
    expect(offset.y).toBeGreaterThanOrEqual(168);
    expect(offset.y + 70).toBeLessThanOrEqual(294);
  });
});

describe("character art", () => {
  it.each([
    [{ phase: "working" }, "working", /dafeiyu-working/, "infinite"],
    [{ phase: "success" }, "happy", /dafeiyu-happy/, "1"],
    [{ action: "wave" }, "wave", /dafeiyu-wave/, "1"],
    [{ action: "left" }, "left", /dafeiyu-idle/, "infinite"],
    [{ phase: "working", asleep: true }, "idle", /dafeiyu-idle/, "infinite"],
  ] as const)("draws %o from its own strip", (props, pose, sheet, count) => {
    const { container } = render(<Character kind="naiwa" {...props} />);
    const avatar = container.querySelector<HTMLElement>("[data-action]");
    const sprite = avatar?.querySelector<HTMLElement>("span");
    expect(avatar?.dataset.action).toBe(pose);
    expect(sprite?.style.backgroundImage).toMatch(sheet);
    expect(sprite?.style.animationIterationCount).toBe(count);
    expect(sprite?.style.animationPlayState).toBe("asleep" in props ? "paused" : "running");
  });
  it("idles after a one-shot pose ends and plays it again when asked anew", () => {
    const page = render(<Character kind="naiwa" action="happy" />);
    const pose = () => page.container.querySelector<HTMLElement>("[data-action]")?.dataset.action;
    const sprite = page.container.querySelector<HTMLElement>("[data-action] > span");
    if (!sprite) throw new Error("no sprite");
    // jsdom has no AnimationEvent, so React listens for the prefixed name there.
    fireEvent(sprite, new Event("webkitAnimationEnd", { bubbles: true }));
    expect(pose()).toBe("idle");
    page.rerender(<Character kind="naiwa" action="happy" />);
    expect(pose()).toBe("idle");
    page.rerender(<Character kind="naiwa" />);
    page.rerender(<Character kind="naiwa" action="happy" />);
    expect(pose()).toBe("happy");
  });
});

describe("companion interactions", () => {
  it.each([
    ["success", "结果已经在小窝里了。接下来做什么，听你的。"],
    ["error", "先歇一下也好。想继续时，我们再看看这件事。"],
  ] as const)("does not animate a %s outcome restored after connecting", (phase, words) => {
    const page = render(<Stage character="naiwa" quiet={false} taskPhase="connecting" />);
    page.rerender(<Stage character="naiwa" quiet={false} taskPhase={phase} />);
    expect(screen.getByText(words)).toBeVisible();
  });
  it.each([
    ["success", "结果已经在小窝里了。接下来做什么，听你的。"],
    ["error", "先歇一下也好。想继续时，我们再看看这件事。"],
  ] as const)("settles the previous %s after a notification instead of replaying its emotion", (phase, words) => {
    const page = render(<Stage character="naiwa" quiet={false} taskPhase={phase} />);
    page.rerender(
      <Stage
        character="naiwa"
        quiet={false}
        taskPhase="success"
        taskEvent="server-notice-1"
        taskMessage="有新结果啦 · 合成提醒"
      />,
    );
    expect(screen.getByText("有新结果啦 · 合成提醒")).toBeVisible();
    page.rerender(<Stage character="naiwa" quiet={false} taskPhase={phase} />);
    expect(screen.getByText(words)).toBeVisible();
    expect(screen.queryByText("开心 · 这轮回答完成啦！一起看看结果？")).toBeNull();
  });
  it("can greet using only the keyboard", async () => {
    const user = userEvent.setup();
    render(<Stage character="glow" quiet={false} />);
    screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" }).focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("status")).toHaveTextContent("嗨，我在这儿");
    expect(screen.queryByRole("button", { name: "陪我一下" })).not.toBeInTheDocument();
  });
  it("quiet mode stops new play without penalizing or blocking greetings", async () => {
    const user = userEvent.setup();
    render(<Stage character="chestnut" quiet />);
    expect(screen.queryByRole("button", { name: "陪我一下" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" }));
    expect(screen.getByRole("status")).toHaveTextContent("我在，安静陪你");
  });
  it("cancels a drag without leaving a stuck dragging state", () => {
    render(<Stage character="glow" quiet={false} />);
    const pet = screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" });
    fireEvent.pointerDown(pet, { button: 0, pointerId: 1, clientX: 20, clientY: 20 });
    fireEvent.pointerMove(pet, { pointerId: 1, clientX: 60, clientY: 40 });
    fireEvent.pointerCancel(pet, { pointerId: 1 });
    expect(screen.getByRole("status")).toHaveTextContent("坐稳啦");
    fireEvent.click(pet);
    fireEvent.click(pet);
    expect(screen.getByRole("status")).toHaveTextContent("嗨，我在这儿");
  });
  it("moves between the home and page desktop and can be recalled after hiding", async () => {
    const user = userEvent.setup();
    render(
      <>
        <CompanionPanel userId="panel-roundtrip" />
        <CompanionFloating userId="panel-roundtrip" onOpen={() => {}} />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "到网页桌面陪我" }));
    const floating = screen.getByRole("complementary", { name: "网页桌宠" });
    expect(floating).toBeVisible();
    await user.click(within(floating).getByRole("button", { name: "大肥鱼更多操作" }));
    await user.click(screen.getByRole("menuitem", { name: "收起大肥鱼" }));
    expect(screen.queryByRole("complementary", { name: "网页桌宠" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "叫回小窝" }));
    expect(screen.getByRole("button", { name: "和大肥鱼聊聊" })).toBeVisible();
  });
  it("a second member does not inherit the first member's visible pet", async () => {
    const user = userEvent.setup();
    const view = render(<CompanionPanel key="member-a" userId="member-a" />);
    await user.click(screen.getByRole("button", { name: "到网页桌面陪我" }));
    view.rerender(<CompanionPanel key="member-b" userId="member-b" />);
    expect(screen.getByRole("button", { name: "和大肥鱼聊聊" })).toBeVisible();
  });
});
