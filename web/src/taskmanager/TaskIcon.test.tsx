import { fireEvent, render } from "@testing-library/react";
import { expect, it } from "vitest";
import { TaskIcon } from "./TaskIcon";
import type { Task } from "./api";

const task: Task = { id: "process:1:1", name: "app", pid: 1, state: "S", cpuPercent: 0, memoryBytes: 1, readBytesPerSecond: 0, writeBytesPerSecond: 0, iconId: "a" };
const png = "data:image/png;base64,iVBORw0KGgo=";
it("uses the installed icon and falls back when the image cannot load", () => {
  const {container, rerender} = render(<TaskIcon task={task} icons={{a:png}} processes />);
  expect(container.querySelector("img")?.getAttribute("src")).toBe(png);
  fireEvent.error(container.querySelector("img")!);
  expect(container.querySelector("img")).toBeNull();
  expect(container.querySelector("svg")).toBeTruthy();
  const updated = "data:image/svg+xml;base64,PHN2Zy8+";
  rerender(<TaskIcon task={task} icons={{a:updated}} processes />);
  expect(container.querySelector("img")?.getAttribute("src")).toBe(updated);
});
it("uses defaults for missing icons and does not request a remote URL", () => {
  const {container, rerender} = render(<TaskIcon task={task} processes={false} />);
  expect(container.querySelector("svg")).toBeTruthy();
  rerender(<TaskIcon task={task} icons={{a:"https://example.invalid/icon.png"}} processes={false} />);
  expect(container.querySelector("img")).toBeNull();
});
