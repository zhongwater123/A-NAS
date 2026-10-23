import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession, type Wallpaper } from "../api";
import { PersonalizationSection } from "./PersonalizationSection";
import { stubAPI } from "../test/api";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const refused = () => json({ error: { code: "forbidden", message: "administrator permission is required" } }, 403);

function installAPI(options: { sleepAfterMinutes?: number; wallpaper?: Wallpaper; refuse?: boolean } = {}) {
  let sleepAfterMinutes = options.sleepAfterMinutes ?? 30;
  let wallpaper: Wallpaper = options.wallpaper ?? { style: "fill" };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    if (path === "/api/v1/local-display") {
      if (method === "PUT") {
        if (options.refuse) return refused();
        sleepAfterMinutes = (JSON.parse(String(init?.body)) as { sleepAfterMinutes: number }).sleepAfterMinutes;
      }
      return json({ sleepAfterMinutes });
    }
    if (path === "/api/v1/wallpaper") {
      if (method === "PATCH") {
        if (options.refuse) return refused();
        wallpaper = { ...wallpaper, style: (JSON.parse(String(init?.body)) as Wallpaper).style };
      }
      return json(wallpaper);
    }
    if (path === "/api/v1/wallpaper/image" && method === "PUT") {
      wallpaper = { ...wallpaper, image: { version: "0123456789abcdef", width: 800, height: 600 } };
      return json(wallpaper);
    }
    if (path === "/api/v1/wallpaper/image" && method === "DELETE") {
      wallpaper = { style: "fill" };
      return json(wallpaper);
    }
    return json({ error: { code: "not_found", message: "not found" } }, 404);
  });
  stubAPI(fetchMock);
  return fetchMock;
}

const sent = (fetchMock: ReturnType<typeof installAPI>, path: string, method: string) =>
  fetchMock.mock.calls.filter(([input, init]) => String(input) === path && init?.method === method);

// jsdom neither decodes images nor draws on a canvas.
function stubPictures() {
  class DecodedImage {
    naturalWidth = 5000;
    naturalHeight = 2500;
    decoding = "";
    onload?: () => void;
    onerror?: () => void;
    private source = "";
    get src() {
      return this.source;
    }
    set src(value: string) {
      this.source = value;
      queueMicrotask(() => this.onload?.());
    }
  }
  vi.stubGlobal("Image", DecodedImage);
  vi.stubGlobal(
    "URL",
    Object.assign(URL, { createObjectURL: vi.fn(() => "blob:wallpaper-source"), revokeObjectURL: vi.fn() }),
  );
  const drawImage = vi.fn();
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ fillRect: vi.fn(), drawImage } as never);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) =>
    callback(new Blob(["jpeg"], { type: "image/jpeg" })),
  );
  return drawImage;
}

describe("Settings → 个性化", () => {
  beforeEach(() =>
    setSession({
      csrfToken: "csrf-token",
      expiresAt: "2026-10-21T00:00:00Z",
      user: {
        id: "user:owner",
        username: "owner",
        role: "admin",
        status: "active",
        createdAt: "2026-10-07T10:00:00Z",
        mustChangePassword: false,
      },
    }),
  );
  afterEach(() => {
    setSession();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("chooses when the local screen goes black", async () => {
    const fetchMock = installAPI();
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    const choices = screen.getByRole("radiogroup", { name: "息屏时间" });
    expect(await within(choices).findByRole("radio", { name: "30 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText(/登录页没有屏保，33 分钟后直接变黑/)).toBeTruthy();

    await user.click(within(choices).getByRole("radio", { name: "10 分钟" }));
    expect(await within(choices).findByRole("radio", { name: "10 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText("13 分钟")).toBeTruthy();
    const [[, init]] = sent(fetchMock, "/api/v1/local-display", "PUT");
    expect(JSON.parse(String(init?.body))).toEqual({ sleepAfterMinutes: 10 });
    expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("csrf-token");

    await user.click(within(choices).getByRole("radio", { name: "从不" }));
    expect(await screen.findByText("屏保会一直播放，登录页一直亮着。")).toBeTruthy();
    expect(screen.queryByText("黑屏")).toBeNull();
  });

  it("puts the previous choices back when saving fails", async () => {
    installAPI({ refuse: true });
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    const sleep = screen.getByRole("radiogroup", { name: "息屏时间" });
    await within(sleep).findByRole("radio", { name: "30 分钟", checked: true });
    await user.click(within(sleep).getByRole("radio", { name: "1 小时" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(within(sleep).getByRole("radio", { name: "30 分钟", checked: true })).toBeTruthy();

    const styles = screen.getByRole("radiogroup", { name: "壁纸样式" });
    await user.click(await within(styles).findByRole("radio", { name: "平铺" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(within(styles).getByRole("radio", { name: "填充", checked: true })).toBeTruthy();
  });

  it("draws the wallpaper in the chosen style for everyone", async () => {
    const fetchMock = installAPI();
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    expect(await screen.findByText("默认壁纸")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "恢复默认" })).toBeNull();
    const styles = screen.getByRole("radiogroup", { name: "壁纸样式" });
    expect(
      within(styles)
        .getAllByRole("radio")
        .map((radio) => radio.textContent),
    ).toEqual(["填充", "适应", "拉伸", "居中", "平铺"]);

    await user.click(within(styles).getByRole("radio", { name: "平铺" }));
    expect(await within(styles).findByRole("radio", { name: "平铺", checked: true })).toBeTruthy();
    expect(screen.getByText("按图片原始大小重复铺满桌面")).toBeTruthy();
    expect(screen.getByRole("img", { name: "当前桌面壁纸的预览" }).style.backgroundRepeat).toBe("repeat");
    const [[, init]] = sent(fetchMock, "/api/v1/wallpaper", "PATCH");
    expect(JSON.parse(String(init?.body))).toEqual({ style: "tile" });
  });

  it("sets a picture from this computer as the wallpaper after showing it on the desktop", async () => {
    const drawImage = stubPictures();
    const fetchMock = installAPI();
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    await user.click(await screen.findByRole("button", { name: "更换图片…" }));
    const dialog = screen.getByRole("dialog", { name: "更换壁纸" });
    await user.upload(
      within(dialog).getByLabelText("从本机选择图片"),
      new File(["png"], "beach.png", { type: "image/png" }),
    );

    expect(await within(dialog).findByRole("img", { name: "新壁纸在桌面上的预览" })).toBeTruthy();
    expect(within(dialog).getByText(/5000 × 2500 像素，保存时缩小到 3840 × 1920/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "设为壁纸" }));

    expect(await screen.findByText("自定义图片")).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "更换壁纸" })).toBeNull();
    expect(screen.getByText("800 × 600 像素")).toBeTruthy();
    expect(drawImage).toHaveBeenCalledWith(expect.anything(), 0, 0, 3840, 1920);
    const [[, init]] = sent(fetchMock, "/api/v1/wallpaper/image", "PUT");
    expect(new Headers(init?.headers).get("Content-Type")).toBe("image/jpeg");
    expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("csrf-token");
    expect(screen.getByRole("img", { name: "当前桌面壁纸的预览" }).style.backgroundImage).toContain(
      "/api/v1/wallpaper/image?v=0123456789abcdef",
    );
  });

  it("asks before going back to the default wallpaper", async () => {
    const fetchMock = installAPI({
      wallpaper: { style: "center", image: { version: "0123456789abcdef", width: 800, height: 600 } },
    });
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    await user.click(await screen.findByRole("button", { name: "恢复默认" }));
    const confirm = screen.getByRole("group", { name: "确认恢复默认壁纸" });
    expect(sent(fetchMock, "/api/v1/wallpaper/image", "DELETE")).toHaveLength(0);
    await user.click(within(confirm).getByRole("button", { name: "恢复默认" }));
    expect(await screen.findByText("默认壁纸")).toBeTruthy();
    expect(sent(fetchMock, "/api/v1/wallpaper/image", "DELETE")).toHaveLength(1);
    expect(
      within(screen.getByRole("radiogroup", { name: "壁纸样式" })).getByRole("radio", { name: "填充", checked: true }),
    ).toBeTruthy();
  });
});
