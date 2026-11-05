const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("companionDesktop", {
  interactive: (enabled) => ipcRenderer.send("companion:interactive", enabled === true),
  move: (dx, dy) => ipcRenderer.send("companion:move", dx, dy),
  hide: () => ipcRenderer.send("companion:hide"),
  home: () => ipcRenderer.send("companion:home"),
  conversation: (storedId) => ipcRenderer.send("companion:conversation", storedId),
  compact: () => ipcRenderer.send("companion:compact"),
  expand: () => ipcRenderer.send("companion:expand"),
  body: (rect) => ipcRenderer.send("companion:body", rect),
  reveal: (open) => ipcRenderer.send("companion:reveal", open === true),
  onClip: (callback) => {
    const listener = (_event, clip) => callback(clip);
    ipcRenderer.on("companion:clip", listener);
    return () => ipcRenderer.removeListener("companion:clip", listener);
  },
});
