import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setSession } from "../api/client";
import { session } from "../test/fixtures";
import { type PersonalizationOptions, personalizationHandlers } from "../test/handlers/settings";
import { server } from "../test/server";
import { PersonalizationSection } from "./PersonalizationSection";

const signedIn = session();

// serve answers 个性化 and returns what the page sent.
function serve(options: PersonalizationOptions = {}) {
  const personalization = personalizationHandlers(options);
  server.use(...personalization.handlers);
  return personalization.sent;
}

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
  beforeEach(() => setSession(signedIn));
  afterEach(() => {
    setSession();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("chooses when the local screen goes black", async () => {
    const sent = serve();
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    const choices = screen.getByRole("radiogroup", { name: "息屏时间" });
    expect(await within(choices).findByRole("radio", { name: "30 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText(/登录页没有屏保，33 分钟后直接变黑/)).toBeTruthy();

    await user.click(within(choices).getByRole("radio", { name: "10 分钟" }));
    expect(await within(choices).findByRole("radio", { name: "10 分钟", checked: true })).toBeTruthy();
    expect(screen.getByText("13 分钟")).toBeTruthy();
    expect(sent.displays[0]).toEqual({ body: { sleepAfterMinutes: 10 }, csrf: signedIn.csrfToken });

    await user.click(within(choices).getByRole("radio", { name: "从不" }));
    expect(await screen.findByText("屏保会一直播放，登录页一直亮着。")).toBeTruthy();
    expect(screen.queryByText("黑屏")).toBeNull();
  });

  it("puts the previous choices back when saving fails", async () => {
    serve({ refuse: true });
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
    const sent = serve();
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
    expect(sent.styles).toEqual([{ style: "tile" }]);
  });

  it("sets a picture from this computer as the wallpaper after showing it on the desktop", async () => {
    const drawImage = stubPictures();
    const sent = serve();
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
    expect(sent.images).toEqual([{ type: "image/jpeg", csrf: signedIn.csrfToken, bytes: "jpeg" }]);
    expect(screen.getByRole("img", { name: "当前桌面壁纸的预览" }).style.backgroundImage).toContain(
      "/api/v1/wallpaper/image?v=0123456789abcdef",
    );
  });

  it("asks before going back to the default wallpaper", async () => {
    const sent = serve({
      wallpaper: { style: "center", image: { version: "0123456789abcdef", width: 800, height: 600 } },
    });
    const user = userEvent.setup();
    render(<PersonalizationSection />);
    await user.click(await screen.findByRole("button", { name: "恢复默认" }));
    const confirm = screen.getByRole("group", { name: "确认恢复默认壁纸" });
    expect(sent.resets).toBe(0);
    await user.click(within(confirm).getByRole("button", { name: "恢复默认" }));
    expect(await screen.findByText("默认壁纸")).toBeTruthy();
    expect(sent.resets).toBe(1);
    expect(
      within(screen.getByRole("radiogroup", { name: "壁纸样式" })).getByRole("radio", { name: "填充", checked: true }),
    ).toBeTruthy();
  });
});
