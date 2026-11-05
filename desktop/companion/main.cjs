const { app, BrowserWindow, dialog, ipcMain, Menu, Tray, nativeImage, screen, powerMonitor, shell } = require("electron");
const path = require("node:path");
const fs = require("node:fs");
const { nasOrigin, safeDelta, clampWindow, clampBody, validBody, validConversation } = require("./policy.cjs");

let pet;
let home;
let tray;
let origin;
let interactive = true;
// Whether the pet window is the small character (350x400) rather than the NAS sign-in page.
// Kept here: Windows rounds a scaled window's width (352 at 150%), so the width cannot tell.
let compact = false;
let positionTimer;
// The pet was showing when the computer went to sleep, so it comes back on wake.
let hiddenForSleep = false;
// The pet's page did not load; the member is being asked what to do.
let loadFailed = false;
// Where the character and its buttons sit in the compact window, as the page reports it.
// Until then the whole window is kept on screen.
let body;
// A chat or menu is open, so the whole window is on screen; `from` is where the pet was.
let revealed;

// The smoke runner gives the app its own disposable profile, so verification
// cannot change a member's preferences or reuse a live NAS session.
if (process.env.ANAS_COMPANION_DATA_DIR) app.setPath("userData", process.env.ANAS_COMPANION_DATA_DIR);

// Each pose the pet has shown keeps its decoded strip (up to 12 MB) in the GPU
// process's image cache long after it is back to idle; the default cache is far
// larger than the pet ever needs. Past this cap the GPU process drops the strips
// nothing is drawing (one being drawn is never dropped; a pose shown again is
// decoded again). It must stay above the strips drawn at once: the pet's and the
// home window's, plus a pose fading into the next.
// See docs/investigations/2026-11-05-companion-gpu-memory.md.
app.commandLine.appendSwitch("force-gpu-mem-discardable-limit-mb", "32");

if (!app.requestSingleInstanceLock()) app.quit();

const GRANTED = new Set(["fullscreen", "clipboard-sanitized-write"]);
function protect(window, allowedOrigin, onBlockedRedirect) {
  const sameOrigin = (url) => {
    try { return Boolean(allowedOrigin) && new URL(url).origin === allowedOrigin; } catch { return false; }
  };
  // Links elsewhere open in the system browser: web addresses only, never other protocols.
  window.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:/i.test(url) && !sameOrigin(url)) void shell.openExternal(url);
    return { action: "deny" };
  });
  window.webContents.on("will-navigate", (event, url) => { if (!sameOrigin(url)) event.preventDefault(); });
  window.webContents.on("will-redirect", (event, url) => {
    if (sameOrigin(url)) return;
    event.preventDefault();
    onBlockedRedirect?.(url);
  });
  // Only the NAS's own pages get anything: fullscreen for a video in the home window and
  // writing to the clipboard (copying a share link). Nothing else is granted.
  const granted = (permission, url) => GRANTED.has(permission) && sameOrigin(url);
  window.webContents.session.setPermissionRequestHandler((contents, permission, callback) =>
    callback(granted(permission, contents.getURL())));
  window.webContents.session.setPermissionCheckHandler((_contents, permission, requestingOrigin) =>
    granted(permission, requestingOrigin));
}
function load(window, desktop) {
  if (window === pet) body = undefined;
  if (origin) window.loadURL(`${origin}/?${desktop ? "companion=desktop" : "open=companion&summon=1"}`);
  else window.loadFile(path.join(__dirname, "dist", "companion.html"), { query: desktop ? { desktop: "1" } : {} });
}
function loadPet() {
  loadFailed = false;
  load(pet, true);
  pet.webContents.once("did-finish-load", () => { if (!loadFailed) showPet(); });
}
// A window made with resizable: false keeps its size as both its minimum and maximum, so
// setSize alone cannot change it (seen on Windows: the pet stayed 760x720 offline).
function resizePet(width, height) {
  pet.setResizable(true);
  pet.setSize(width, height);
  pet.setResizable(false);
}
// A page that did not load leaves a transparent window over the desktop: hide it
// and let the member retry, keep the pet company offline, or quit.
function petFailed(message, reason) {
  if (loadFailed) return;
  loadFailed = true;
  pet.hide();
  const detail = origin
    ? `${origin} 打不开（${reason}）。请检查 NAS 是否开机、地址是否正确，再重试；也可以先离线陪伴。`
    : `离线页面打不开（${reason}）。请先构建桌宠页面（npm run companion:build）。`;
  void dialog.showMessageBox({ type: "warning", title: "A-NAS 伙伴", message, detail, noLink: true,
    buttons: ["重试", "先离线陪伴", "退出"], defaultId: 0, cancelId: 1 }).then(({ response }) => {
    if (response === 2) { app.quit(); return; }
    if (response === 1 && origin) {
      origin = undefined;
      compact = true;
      resizePet(350, 400);
      restorePosition();
    }
    loadPet();
  });
}
function showHome(storedId) {
  const show = (window) => {
    if (origin && validConversation(storedId)) window.loadURL(`${origin}/?open=assistant&session=${encodeURIComponent(storedId)}`);
    else load(window, false);
  };
  if (home && !home.isDestroyed()) { if (origin) show(home); home.show(); return; }
  home = new BrowserWindow({ width: 760, height: 840, minWidth: 420, title: "A-NAS 伙伴小窝",
    backgroundColor: "#eceee7",
    webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true, devTools: !app.isPackaged } });
  protect(home, origin);
  show(home);
}
function showPet() { if (pet && !pet.isDestroyed()) pet.showInactive(); }
// Windows at a fractional scale grows a window a few pixels each time it is moved partly off
// the screen (seen at 150%: 352x400 became 388x434), so the compact pet moves with its size pinned.
function movePet(x, y) {
  if (compact) pet.setBounds({ x, y, width: 350, height: 400 });
  else pet.setPosition(x, y);
}
// The compact pet keeps only its body on screen, unless a chat or menu needs the whole window.
function fit(point, work, bounds) {
  return compact && body && !revealed ? clampBody(point, work, body) : clampWindow(point, work, bounds);
}
function placePet() {
  revealed = undefined;
  const work = screen.getPrimaryDisplay().workArea;
  const bounds = pet.getBounds();
  const point = fit({ x: work.x + work.width - bounds.width - 24, y: work.y + work.height - bounds.height - 18 }, work, bounds);
  movePet(point.x, point.y);
}
// Tells the page how much of the window is off the work area, so the speech bubble stays visible.
function sendClip() {
  if (!pet || pet.isDestroyed() || !compact) return;
  const bounds = pet.getBounds();
  const work = screen.getDisplayMatching(bounds).workArea;
  pet.webContents.send("companion:clip", {
    top: Math.max(0, work.y - bounds.y),
    left: Math.max(0, work.x - bounds.x),
    right: Math.max(0, bounds.x + bounds.width - work.x - work.width),
    bottom: Math.max(0, bounds.y + bounds.height - work.y - work.height),
  });
}
function restorePosition() {
  try {
    const point = JSON.parse(fs.readFileSync(path.join(app.getPath("userData"), "companion-position.json"), "utf8"));
    if (!Number.isFinite(point.x) || !Number.isFinite(point.y)) { placePet(); return; }
    const bounds = pet.getBounds();
    const work = screen.getDisplayNearestPoint({ x: Math.round(point.x), y: Math.round(point.y) }).workArea;
    const fitted = fit(point, work, bounds);
    movePet(fitted.x, fitted.y);
  } catch { placePet(); }
}
function rememberPosition() {
  clearTimeout(positionTimer);
  positionTimer = setTimeout(() => {
    if (!pet || pet.isDestroyed() || !compact) return;
    const { x, y } = pet.getBounds();
    try {
      fs.mkdirSync(app.getPath("userData"), { recursive: true });
      fs.writeFileSync(path.join(app.getPath("userData"), "companion-position.json"), JSON.stringify({ x, y }));
    } catch { /* Local preferences must not stop the companion. */ }
  }, 250);
}
function senderIsPet(event) {
  if (event.sender !== pet?.webContents) return false;
  const url = event.sender.getURL();
  return origin ? new URL(url).origin === origin : url.startsWith("file:");
}

app.whenReady().then(() => {
  try { origin = nasOrigin(process.env.ANAS_NAS_URL); }
  catch {
    dialog.showErrorBox("A-NAS 伙伴", `NAS 地址不能用：${process.env.ANAS_NAS_URL}\n` +
      "需要 HTTPS 地址，或局域网内的 HTTP 地址，并且不能带用户名和密码。");
    app.quit();
    return;
  }
  app.setName("A-NAS Companion");
  // No default English menu. macOS keeps the Edit menu: without it, copy and paste shortcuts stop working.
  Menu.setApplicationMenu(process.platform === "darwin"
    ? Menu.buildFromTemplate([{ role: "appMenu" }, { role: "editMenu" }])
    : null);
  compact = !origin;
  pet = new BrowserWindow({ width: origin ? 760 : 350, height: origin ? 720 : 400, show: false,
    frame: false, transparent: true, alwaysOnTop: true, skipTaskbar: true, resizable: false,
    backgroundColor: "#00000000", hasShadow: false,
    webPreferences: { preload: path.join(__dirname, "preload.cjs"), nodeIntegration: false,
      contextIsolation: true, sandbox: true, backgroundThrottling: true, devTools: !app.isPackaged } });
  protect(pet, origin, (url) => petFailed("连不上 NAS", `跳转到了另一个地址 ${new URL(url).origin}`));
  pet.on("close", (event) => { if (!app.isQuitting) { event.preventDefault(); pet.hide(); } });
  pet.once("ready-to-show", () => { restorePosition(); if (!loadFailed) showPet(); });
  pet.on("move", () => { rememberPosition(); sendClip(); });
  pet.webContents.on("did-fail-load", (_event, code, description, _url, isMainFrame) => {
    if (isMainFrame && code !== -3) petFailed("连不上 NAS", description);
  });
  pet.webContents.on("render-process-gone", (_event, details) => {
    if (details.reason !== "clean-exit") petFailed("大肥鱼的页面出错了", details.reason);
  });
  load(pet, true);

  // A small local tray image. No external assets, downloads or network at startup.
  const pixels = Buffer.alloc(16 * 16 * 4);
  for (let y = 2; y < 14; y++) for (let x = 3; x < 13; x++) {
    const i = (y * 16 + x) * 4;
    pixels[i] = 89; pixels[i + 1] = 145; pixels[i + 2] = 125; pixels[i + 3] = 255;
  }
  tray = new Tray(nativeImage.createFromBitmap(pixels, { width: 16, height: 16, scaleFactor: 1 }));
  tray.setToolTip("A-NAS 伙伴");
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: "叫出伙伴", click: showPet },
    { label: "打开小窝", click: () => showHome() },
    { label: "隐藏", click: () => pet.hide() },
    { label: "重新连接", click: loadPet },
    { label: "回到屏幕角落", click: () => { placePet(); showPet(); } },
    { type: "separator" }, { label: "退出", click: () => app.quit() },
  ]));
  tray.on("click", showPet);

  ipcMain.on("companion:interactive", (event, enabled) => {
    if (!senderIsPet(event) || typeof enabled !== "boolean" || enabled === interactive) return;
    interactive = enabled;
    pet.setIgnoreMouseEvents(!enabled, { forward: true });
  });
  ipcMain.on("companion:move", (event, dx, dy) => {
    if (!senderIsPet(event)) return;
    const current = pet.getBounds();
    const work = screen.getDisplayNearestPoint(screen.getCursorScreenPoint()).workArea;
    const point = fit({ x: current.x + safeDelta(dx), y: current.y + safeDelta(dy) }, work, current);
    movePet(point.x, point.y);
  });
  ipcMain.on("companion:body", (event, rect) => {
    if (!senderIsPet(event) || !compact) return;
    const bounds = pet.getBounds();
    const next = validBody(rect, bounds);
    if (!next) return;
    const first = !body;
    body = next;
    // The saved position may have the character at an edge, which needs the body to restore.
    if (first) restorePosition();
    else if (!revealed) {
      const work = screen.getDisplayMatching(bounds).workArea;
      const point = fit(bounds, work, bounds);
      if (point.x !== bounds.x || point.y !== bounds.y) movePet(point.x, point.y);
    }
    sendClip();
  });
  ipcMain.on("companion:reveal", (event, open) => {
    if (!senderIsPet(event) || typeof open !== "boolean" || !compact) return;
    if (open) {
      if (revealed) return;
      const bounds = pet.getBounds();
      const point = clampWindow(bounds, screen.getDisplayMatching(bounds).workArea, bounds);
      if (point.x === bounds.x && point.y === bounds.y) return;
      movePet(point.x, point.y);
      // Read back: Windows rounds a scaled window's position.
      revealed = { from: { x: bounds.x, y: bounds.y }, to: pet.getBounds() };
      return;
    }
    if (!revealed) return;
    const { from, to } = revealed;
    revealed = undefined;
    // Back to the edge it came from, unless the member moved it while the chat was open.
    const now = pet.getBounds();
    if (now.x === to.x && now.y === to.y) movePet(from.x, from.y);
  });
  ipcMain.on("companion:hide", (event) => { if (senderIsPet(event)) pet.hide(); });
  ipcMain.on("companion:conversation", (event, id) => { if (senderIsPet(event) && origin && validConversation(id)) showHome(id); });
  ipcMain.on("companion:home", (event) => { if (senderIsPet(event)) showHome(); });
  ipcMain.on("companion:compact", (event) => {
    if (!senderIsPet(event)) return;
    resizePet(350, 400);
    compact = true;
    revealed = undefined;
    restorePosition();
  });
  ipcMain.on("companion:expand", (event) => {
    if (!senderIsPet(event)) return;
    pet.setIgnoreMouseEvents(false);
    interactive = true;
    compact = false;
    resizePet(760, 720);
    placePet();
  });
  screen.on("display-removed", () => placePet());
  screen.on("display-metrics-changed", () => placePet());
  powerMonitor.on("suspend", () => {
    hiddenForSleep = pet.isVisible();
    pet.hide();
  });
  const wake = () => {
    if (!hiddenForSleep) return;
    hiddenForSleep = false;
    showPet();
  };
  powerMonitor.on("resume", wake);
  powerMonitor.on("unlock-screen", wake);
  // Let the OS pause rendering while hidden. Never keep an invisible video running.
  app.on("browser-window-focus", (_event, window) => {
    if (window !== pet && window.isFullScreen() && pet.isVisible()) {
      pet.hide();
    }
  });
  app.on("activate", showPet);
});
app.on("second-instance", showPet);
app.on("before-quit", () => { app.isQuitting = true; clearTimeout(positionTimer); tray?.destroy(); });
app.on("window-all-closed", () => { if (!tray) app.quit(); });
