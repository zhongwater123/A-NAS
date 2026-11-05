// Real SQLite/domain API plus real Web components in two Chromium contexts.
// The loopback test server supplies a synthetic member; product auth is not
// exercised here. No NAS URL, account, cookie or model is used.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const { spawn, execFile } = require("node:child_process");
const { promisify } = require("node:util");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const root = path.resolve(__dirname, "../..");

async function run() {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "anas-handoff-"));
  const output = path.join(__dirname, "build/handoff");
  await fs.mkdir(output, { recursive: true });
  let processAPI, vite, browser, pageDirectory;
  try {
    const binary = path.join(temporary, process.platform === "win32" ? "fixture.exe" : "fixture");
    await promisify(execFile)(process.env.GO || "go", ["build", "-o", binary, "./internal/companionapi/testdata/server"], { cwd: root });
    processAPI = spawn(binary, [temporary], { stdio: ["ignore", "pipe", "inherit"] });
    const api = (await once(processAPI.stdout, "data"))[0].toString().trim();
    assert.match(api, /^http:\/\/127\.0\.0\.1:\d+$/);
    await fs.mkdir(path.join(root, "web/.tmp"), { recursive: true });
    pageDirectory = await fs.mkdtemp(path.join(root, "web/.tmp/handoff-"));
    const pagePath = path.relative(path.join(root,"web"), pageDirectory).split(path.sep).join("/");
    await fs.writeFile(path.join(pageDirectory,"index.html"), `<html><head><meta charset="UTF-8"></head><body><div id="root"></div><script type="module" src="/${pagePath}/entry.jsx"></script></body></html>`);
    await fs.writeFile(path.join(pageDirectory,"entry.jsx"), `
      import React from "react"; import { createRoot } from "react-dom/client";
      import "/src/styles/tokens.css";
      import { CompanionPanel } from "/src/companion/CompanionPanel.tsx";
      import { CompanionDesktop } from "/src/companion/CompanionDesktop.tsx";
      const Component = new URLSearchParams(location.search).has("desktop") ? CompanionDesktop : CompanionPanel;
      createRoot(document.getElementById("root")).render(React.createElement(Component, { userId:"browser-test-member", connected:true }));
    `);
    await promisify(execFile)(path.join(root, "web/node_modules/.bin/biome"), ["check", "--write", pageDirectory], { cwd: path.join(root, "web") });
    const { createServer } = await import(path.join(root, "web/node_modules/vite/dist/node/index.js"));
    vite = await createServer({ root: path.join(root, "web"), configFile: path.join(root, "web/companion.vite.config.ts"),
      optimizeDeps: { include: ["react", "react-dom/client", "openapi-fetch"] },
      server: { host: "127.0.0.1", port: 0, hmr: false, open: false, proxy: { "/api/v1/companion": api } } });
    await vite.listen();
    const origin = vite.resolvedUrls.local[0];
    browser = await chromium.launch({ headless: true, channel: process.env.ANAS_TEST_BROWSER || "chrome" });
    const errors = [];
    const mount = async (component) => {
      const context = await browser.newContext({ viewport: component === "CompanionDesktop" ? { width:350, height:400 } : { width: 800, height: 900 } });
      const page = await context.newPage();
      await page.route("**/api/v1/assistant/status", (route) => route.fulfill({ json: { available: false } }));
      page.on("pageerror", (e) => errors.push(String(e)));
      await page.goto(`${origin}${pagePath}/index.html${component === "CompanionDesktop" ? "?desktop=1" : ""}`);
      return page;
    };
    const home = await mount("CompanionPanel");
    const section = home.getByRole("region", { name: "伙伴小窝" }).filter({ visible: true });
    const summon = section.getByRole("button", { name: "叫到这里" });
    await summon.waitFor();
    await assertEventually(async () => assert.equal(await summon.isEnabled(), true));
    await summon.click();
    const homeInvite = section.getByRole("button", { name: /^(安静陪我|醒来)$/ });
    await assertEventually(async () => assert.equal(await homeInvite.isEnabled(), true));
    const greet = async (page, isDesktop = false) => {
      await page.getByRole("button", { name: isDesktop ? "和奶蛙聊聊，按住可拖动" : "和奶蛙聊聊", exact:true }).click();
      await page.getByRole("textbox", { name: "跟奶蛙说句话" }).waitFor();
      assert.equal(await page.getByRole("button", { name: "找到躲起来的奶蛙" }).count(), 0);
      if (isDesktop) await page.getByRole("button", { name: "收起聊天" }).click();
    };
    await greet(home);
    await section.getByRole("button", { name: "安静陪我", exact: true }).click();
    await assertEventually(async () => assert.equal((await (await fetch(`${api}/api/v1/companion`)).json()).quiet, true));
    assert.match(await section.locator("[class*=petSprite]").first().getAttribute("style"), /dafeiyu-[a-z]+.*\.webp/);
    const desktop = await mount("CompanionDesktop");
    const native = desktop.locator("main");
    const desktopInvite = native.getByRole("button", { name: "和奶蛙聊聊，按住可拖动" });
    await assertEventually(async () => assert.equal(await desktopInvite.isEnabled(), true));
    await native.getByRole("button", { name: "奶蛙更多操作" }).click();
    const wake = desktop.getByRole("menuitem", { name: "醒来", exact: true });
    await assertEventually(async () => assert.equal(await wake.isEnabled(), true));
    await wake.click();
    await assertEventually(async () => assert.equal(await desktopInvite.isEnabled(), true));
    assert.equal(await homeInvite.isEnabled(), false);
    assert.equal((await (await fetch(`${api}/api/v1/companion`)).json()).character, "naiwa");
    await greet(desktop, true);
    await assertEventually(async () => assert.equal((await (await fetch(`${api}/api/v1/companion`)).json()).quiet, false));
    await home.screenshot({ path: path.join(output,"home-waiting.png") });
    await desktop.screenshot({ path: path.join(output,"desktop-active.png") });
    await summon.click();
    await assertEventually(async () => assert.equal(await homeInvite.isEnabled(), true));
    assert.equal(await desktopInvite.isEnabled(), false);
    // A genuine network interruption, followed by explicit isolated local play.
    await home.context().setOffline(true);
    await section.getByRole("button", { name: "先在本机陪伴" }).waitFor();
    await section.getByRole("button", { name: "先在本机陪伴" }).click();
    await greet(home);
    const state = await (await fetch(`${api}/api/v1/companion`)).json();
    assert.equal(state.playCount, 0);
    await home.context().setOffline(false);
    await assertEventually(async () => assert.equal(await section.getByRole("button", { name: "连接 NAS" }).isEnabled(), true));
    assert.equal((await (await fetch(`${api}/api/v1/companion`)).json()).playCount, 0);
    assert.deepEqual(errors, []);
    await fs.writeFile(path.join(output,"result.json"), JSON.stringify({ result:"passed", scope:"loopback test fixture; real SQLite and domain API; two browser components; synthetic member; no product auth or NAS", scenarios:["home summon", "persisted quiet/character", "web to desktop", "desktop to web", "disconnect pauses", "explicit local play never syncs/replays"], state, errors }, null, 2));
    console.log(`Handoff smoke passed; evidence: ${output}`);
  } finally {
    if (browser) await browser.close();
    if (vite) await vite.close();
    if (processAPI && processAPI.exitCode === null) { processAPI.kill("SIGINT"); await once(processAPI, "exit"); }
    if (pageDirectory) await fs.rm(pageDirectory, { recursive:true, force:true });
    await fs.rm(temporary, { recursive:true, force:true });
  }
}
async function assertEventually(check) {
  const until = Date.now()+20000;
  while (true) { try { await check(); return; } catch (e) { if(Date.now()>until) throw e; await new Promise(r=>setTimeout(r,100)); } }
}
run().catch((error) => { console.error(error); process.exitCode=1; });
