import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { CompanionFloating } from "./CompanionFloating";
import { CompanionPanel } from "./CompanionPanel";
import { advanceFlight, inside } from "./motion";
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

describe("companion interactions", () => {
  it("can greet and invite using only the keyboard", async () => {
    const user = userEvent.setup();
    render(<Stage character="glow" quiet={false} />);
    screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" }).focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("status")).toHaveTextContent("嗨，我在这儿");
    await user.tab();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("status")).toHaveTextContent("我有个小惊喜");
  });
  it("quiet mode stops new play without penalizing or blocking greetings", async () => {
    const user = userEvent.setup();
    render(<Stage character="chestnut" quiet />);
    expect(screen.getByRole("button", { name: "陪我一下" })).toBeDisabled();
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
    await user.click(within(floating).getByRole("button", { name: "休息" }));
    expect(screen.queryByRole("complementary", { name: "网页桌宠" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "叫回小窝" }));
    expect(screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" })).toBeVisible();
  });
  it("a second member does not inherit the first member's visible pet", async () => {
    const user = userEvent.setup();
    const view = render(<CompanionPanel key="member-a" userId="member-a" />);
    await user.click(screen.getByRole("button", { name: "先休息" }));
    view.rerender(<CompanionPanel key="member-b" userId="member-b" />);
    expect(screen.getByRole("button", { name: "和伙伴打招呼，按住可拖动" })).toBeVisible();
  });
});
