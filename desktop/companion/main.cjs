const { app, BrowserWindow, ipcMain, Menu, Tray, nativeImage, screen, powerMonitor } = require("electron");
const path = require("node:path");
const fs = require("node:fs");
const { nasOrigin, safeDelta, clampWindow, validConversation } = require("./policy.cjs");

let pet;
let home;
let tray;
let origin;
let interactive = true;
let positionTimer;

// The smoke runner gives the app its own disposable profile, so verification
// cannot change a member's preferences or reuse a live NAS session.
if (process.env.ANAS_COMPANION_DATA_DIR) app.setPath("userData", process.env.ANAS_COMPANION_DATA_DIR);

if (!app.requestSingleInstanceLock()) app.quit();

function protect(window, allowedOrigin) {
  window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  window.webContents.on("will-navigate", (event, url) => {
    if (!allowedOrigin || new URL(url).origin !== allowedOrigin) event.preventDefault();
  });
  window.webContents.session.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
}
function load(window, desktop) {
  if (origin) window.loadURL(`${origin}/?${desktop ? "companion=desktop" : "open=companion&summon=1"}`);
  else window.loadFile(path.join(__dirname, "dist", "companion.html"), { query: desktop ? { desktop: "1" } : {} });
}
function showHome(storedId) {
  const show = (window) => {
    if (origin && validConversation(storedId)) window.loadURL(`${origin}/?open=assistant&session=${encodeURIComponent(storedId)}`);
    else load(window, false);
  };
  if (home && !home.isDestroyed()) { if (origin) show(home); home.show(); return; }
  home = new BrowserWindow({ width: 760, height: 840, minWidth: 420, title: "A-NAS 伙伴小窝",
    backgroundColor: "#eceee7", webPreferences: { nodeIntegration: false, contextIsolation: true, sandbox: true } });
  protect(home, origin);
  show(home);
}
function showPet() { if (pet && !pet.isDestroyed()) pet.showInactive(); }
function placePet() {
  const work = screen.getPrimaryDisplay().workArea;
  const bounds = pet.getBounds();
  const point = clampWindow({ x: work.x + work.width - bounds.width - 24, y: work.y + work.height - bounds.height - 18 }, work, bounds);
  pet.setPosition(point.x, point.y);
}
function restorePosition() {
  try {
    const point = JSON.parse(fs.readFileSync(path.join(app.getPath("userData"), "companion-position.json"), "utf8"));
    if (!Number.isFinite(point.x) || !Number.isFinite(point.y)) { placePet(); return; }
    const bounds = pet.getBounds();
    const work = screen.getDisplayNearestPoint({ x: Math.round(point.x), y: Math.round(point.y) }).workArea;
    const fitted = clampWindow(point, work, bounds);
    pet.setPosition(fitted.x, fitted.y);
  } catch { placePet(); }
}
function rememberPosition() {
  clearTimeout(positionTimer);
  positionTimer = setTimeout(() => {
    if (!pet || pet.isDestroyed() || pet.getBounds().width !== 350) return;
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
  catch (error) { console.error(error.message); app.quit(); return; }
  app.setName("A-NAS Companion");
  pet = new BrowserWindow({ width: origin ? 760 : 350, height: origin ? 720 : 400, show: false,
    frame: false, transparent: true, alwaysOnTop: true, skipTaskbar: true, resizable: false,
    backgroundColor: "#00000000", hasShadow: false,
    webPreferences: { preload: path.join(__dirname, "preload.cjs"), nodeIntegration: false,
      contextIsolation: true, sandbox: true, backgroundThrottling: true } });
  protect(pet, origin);
  pet.on("close", (event) => { if (!app.isQuitting) { event.preventDefault(); pet.hide(); } });
  pet.once("ready-to-show", () => { restorePosition(); showPet(); });
  pet.on("move", rememberPosition);
  pet.webContents.on("did-fail-load", (_event, code, description) => {
    if (code !== -3) console.error(`Companion could not load: ${description}. Use the tray to retry or quit.`);
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
    { label: "打开小窝", click: showHome },
    { label: "隐藏", click: () => pet.hide() },
    { label: "重新连接", click: () => { load(pet, true); showPet(); } },
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
    const point = clampWindow({ x: current.x + safeDelta(dx), y: current.y + safeDelta(dy) }, work, current);
    pet.setPosition(point.x, point.y);
  });
  ipcMain.on("companion:hide", (event) => { if (senderIsPet(event)) pet.hide(); });
  ipcMain.on("companion:conversation", (event, id) => { if (senderIsPet(event) && origin && validConversation(id)) showHome(id); });
  ipcMain.on("companion:home", (event) => { if (senderIsPet(event)) showHome(); });
  ipcMain.on("companion:compact", (event) => {
    if (!senderIsPet(event)) return;
    pet.setSize(350, 400);
    restorePosition();
  });
  ipcMain.on("companion:expand", (event) => {
    if (!senderIsPet(event)) return;
    pet.setIgnoreMouseEvents(false);
    interactive = true;
    pet.setSize(760, 720);
    placePet();
  });
  screen.on("display-removed", () => placePet());
  screen.on("display-metrics-changed", () => placePet());
  powerMonitor.on("suspend", () => pet.hide());
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
