import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { changedElements, measure, numbers, startHelper } from "./helper";
import { type FromFrame, readFromFrame } from "./protocol";

// The workspace service's sample page and what it serves of it
// (internal/workspace/testdata, checked by the Go tests): the positions the
// helper reports are byte offsets into the stored source.
const testdata = "../internal/workspace/testdata";
const source = readFileSync(`${testdata}/prd.html`);
const annotated = readFileSync(`${testdata}/prd.annotated.html`, "utf8");
const at = (offset: number, length: number) => source.subarray(offset, offset + length).toString("utf8");

function showAnnotated() {
  const page = new DOMParser().parseFromString(annotated, "text/html");
  document.documentElement.replaceWith(document.importNode(page.documentElement, true));
}

const button = (name: string) =>
  Array.from(document.querySelectorAll("button")).find((el) => el.textContent?.trim() === name) as HTMLElement;

describe("the frame helper on the service's sample page", () => {
  beforeEach(showAnnotated);

  it("reports the position of a picked button in the stored source", () => {
    const picked = measure(button("发出分享"));
    expect(picked.generated).toBe(false);
    expect(picked.tag).toBe("button");
    expect(picked.text).toBe("发出分享");
    expect(at(picked.at, 32)).toBe('<button class="btn btn-primary">');
    // What the agent reads of the element carries no position attributes.
    expect(picked.dom).toBe('<button class="btn btn-primary">发出分享</button>');
    expect(picked.rules).toEqual(expect.arrayContaining([".btn", ".btn-primary"]));
  });

  it("reports an element a script made by its nearest written ancestor", () => {
    const actions = button("发出分享").parentElement as HTMLElement;
    const made = document.createElement("span");
    made.textContent = "脚本加的";
    actions.append(made);
    const picked = measure(made);
    expect(picked.generated).toBe(true);
    expect(picked.at).toBe(Number(actions.getAttribute("data-anas-at")));
    expect(at(picked.at, 4)).toBe("<div");
  });

  it("points only at the changed elements that show", () => {
    const style = document.querySelector("style") as HTMLElement;
    const target = button("发出分享");
    vi.spyOn(Element.prototype, "getClientRects").mockImplementation(function (this: Element) {
      return (this.localName === "style" ? [] : [new DOMRect(0, 0, 1, 1)]) as unknown as DOMRectList;
    });
    const offsets = numbers(`${style.getAttribute("data-anas-at")},${target.getAttribute("data-anas-at")},99999999`);
    expect(changedElements(document, offsets)).toEqual([target]);
    vi.restoreAllMocks();
  });
});

describe("the frame helper's messages", () => {
  const origin = "http://nas.local:8080";
  let sent: FromFrame[];
  let targets: unknown[];
  let stop: () => void;
  beforeEach(() => {
    showAnnotated();
    sent = [];
    targets = [];
    vi.spyOn(window, "postMessage").mockImplementation((message: unknown, target?: unknown) => {
      targets.push(target);
      const read = readFromFrame(message);
      if (read) sent.push(read);
    });
    const script = document.createElement("script");
    script.src = `${origin}/assets/workspace-frame.js`;
    script.dataset.page = "page_1";
    script.dataset.version = "2";
    stop = startHelper(script);
  });
  afterEach(() => {
    stop();
    vi.restoreAllMocks();
  });
  const fromDesktop = (data: unknown, from = origin) =>
    window.dispatchEvent(new MessageEvent("message", { data, origin: from, source: window }));

  it("tells the desktop, and only the desktop, that the page is ready", () => {
    expect(sent).toContainEqual({ type: "anas:ready", title: "家庭相册分享 PRD", blocked: [] });
    expect(new Set(targets)).toEqual(new Set([origin]));
  });

  it("picks what the member clicks while picking, and keeps the click from the page", () => {
    const target = button("发出分享");
    const clicked = vi.fn();
    target.addEventListener("click", clicked);
    target.click();
    expect(clicked).toHaveBeenCalledTimes(1);
    expect(sent.some((message) => message.type === "anas:picked")).toBe(false);

    fromDesktop({ type: "anas:pick", on: true });
    target.click();
    expect(clicked).toHaveBeenCalledTimes(1);
    const picked = sent.find((message) => message.type === "anas:picked");
    expect(picked).toMatchObject({ tag: "button", text: "发出分享", generated: false });
  });

  it("listens to the desktop only", () => {
    fromDesktop({ type: "anas:pick", on: true }, "http://elsewhere");
    button("发出分享").click();
    expect(sent.some((message) => message.type === "anas:picked")).toBe(false);
  });
});
