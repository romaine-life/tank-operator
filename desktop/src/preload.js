const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("tankOperatorDesktop", {
  microsoftLogin: () => ipcRenderer.invoke("desktop-auth:microsoft-login"),
});
