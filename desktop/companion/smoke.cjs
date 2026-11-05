const { _electron } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");

async function run() {
  const profile = await fs.mkdtemp(path.join(os.tmpdir(), "anas-companion-smoke-"));
  const executable = process.env.ANAS_SMOKE_EXECUTABLE;
  const output = path.join(__dirname, "build", executable ? "smoke-packaged" : "smoke");
  await fs.mkdir(output, { recursive: true });
  const env = { ...process.env, ANAS_NAS_URL: "", ANAS_COMPANION_DATA_DIR: profile };
  delete env.ELECTRON_RUN_AS_NODE;
  let desktop;
  try {
    desktop = await _electron.launch({ executablePath: executable || require("electron"), args: executable ? [] : [__dirname], env });
    const page = await desktop.firstWindow();
    const errors = [];
    page.on("pageerror", (error) => errors.push(String(error)));
    await page.getByRole("button", { name: "奶蛙更多操作" }).waitFor();
    const preferences = await desktop.evaluate(({ BrowserWindow }) => {
      const window = BrowserWindow.getAllWindows()[0];
      const prefs = window.webContents.getLastWebPreferences();
      return { top: window.isAlwaysOnTop(), bounds: window.getBounds(), node: prefs.nodeIntegration,
        sandbox: prefs.sandbox, isolation: prefs.contextIsolation };
    });
    assert.equal(preferences.top, true);
    assert.equal(preferences.node, false);
    assert.equal(preferences.sandbox, true);
    assert.equal(preferences.isolation, true);
    assert.equal(preferences.bounds.width, 350);
    await page.getByRole("button", { name: "和奶蛙聊聊，按住可拖动" }).click();
    await page.getByRole("textbox", { name: "跟奶蛙说句话" }).fill("专注25分钟");
    await page.getByRole("button", { name: "发送给奶蛙" }).click();
    await page.getByText("开始专注 25 分钟。我帮你看时间，到点提醒你休息。", { exact:true }).waitFor();
    await page.getByRole("button", { name:"收起聊天" }).click();
    assert.equal(await page.getByRole("button", { name: "找到躲起来的奶蛙" }).count(), 0);
    await page.getByRole("button", { name: "奶蛙更多操作" }).click();
    await page.getByRole("menuitem", { name: "安静陪我", exact: true }).click();
    await page.getByRole("button", { name: "奶蛙更多操作" }).click();
    await page.getByRole("menuitem", { name: "醒来", exact: true }).waitFor();
    const homeWindow = desktop.waitForEvent("window");
    await page.getByRole("menuitem", { name: "回小窝", exact: true }).click();
    const home = await homeWindow;
    await home.getByRole("heading", { name: "伙伴的小窝" }).waitFor();
    assert.match(await home.locator("[class*=petSprite]").getAttribute("style"), /dafeiyu-idle/);
    await page.screenshot({ path: path.join(output, "desktop.png") });
    await home.screenshot({ path: path.join(output, "home.png") });
    await page.getByRole("button", { name: "奶蛙更多操作" }).click();
    await page.getByRole("menuitem", { name: "收起奶蛙", exact: true }).click();
    const hidden = await desktop.evaluate(({ BrowserWindow }) =>
      !BrowserWindow.getAllWindows().find((window) => window.webContents.getURL().includes("desktop=1")).isVisible());
    assert.equal(hidden, true);
    assert.deepEqual(errors, []);
    await fs.writeFile(path.join(output, "result.json"), JSON.stringify({ result: "passed", scope: executable ? "packaged app; offline only" : "source Electron; offline only", preferences,
      scenarios: ["chat focus command", "quiet", "home", "dafeiyu asset", "hide"], errors }, null, 2));
    console.log(`Desktop smoke passed; evidence: ${output}`);
  } finally {
    if (desktop) await desktop.close();
    await fs.rm(profile, { recursive: true, force: true });
  }
}
run().catch((error) => { console.error(error); process.exitCode = 1; });
