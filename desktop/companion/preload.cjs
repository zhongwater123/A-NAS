const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("companionDesktop", {
  interactive: (enabled) => ipcRenderer.send("companion:interactive", enabled === true),
  move: (dx, dy) => ipcRenderer.send("companion:move", dx, dy),
  hide: () => ipcRenderer.send("companion:hide"),
  home: () => ipcRenderer.send("companion:home"),
  conversation: (storedId) => ipcRenderer.send("companion:conversation", storedId),
  compact: () => ipcRenderer.send("companion:compact"),
  expand: () => ipcRenderer.send("companion:expand"),
});
