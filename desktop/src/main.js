const { app, BrowserWindow, Menu, screen, shell } = require("electron");
const path = require("node:path");

const DEFAULT_TANK_URL = "https://tank.romaine.life";
const tankUrl = normalizeUrl(process.env.TANK_OPERATOR_URL || DEFAULT_TANK_URL);
const tankOrigin = new URL(tankUrl).origin;
const MIN_ZOOM_FACTOR = 0.5;
const MAX_ZOOM_FACTOR = 2.0;
const ZOOM_STEP = 0.1;
const WINDOW_TITLE = "Tank";

let mainWindow = null;

function normalizeUrl(value) {
  const parsed = new URL(value);
  parsed.hash = "";
  return parsed.toString();
}

function isTankUrl(url) {
  try {
    return new URL(url).origin === tankOrigin;
  } catch {
    return false;
  }
}

function isAuthNavigation(url) {
  try {
    const parsed = new URL(url);
    const host = parsed.hostname.toLowerCase();
    return (
      isHostOrSubdomain(host, "login.microsoftonline.com") ||
      isHostOrSubdomain(host, "login.microsoft.com") ||
      isHostOrSubdomain(host, "login.windows.net") ||
      isHostOrSubdomain(host, "login.live.com") ||
      isHostOrSubdomain(host, "account.live.com") ||
      isHostOrSubdomain(host, "ms-sso.copilot.microsoft.com") ||
      isHostOrSubdomain(host, "github.com")
    );
  } catch {
    return false;
  }
}

function isHostOrSubdomain(host, domain) {
  return host === domain || host.endsWith(`.${domain}`);
}

function createWindow(initialUrl = tankUrl) {
  const workArea = screen.getPrimaryDisplay().workArea;
  const width = Math.min(1320, workArea.width);
  const height = Math.min(900, workArea.height);

  const win = new BrowserWindow({
    x: workArea.x + Math.floor((workArea.width - width) / 2),
    y: workArea.y + Math.floor((workArea.height - height) / 2),
    width,
    height,
    minWidth: Math.min(960, workArea.width),
    minHeight: Math.min(640, workArea.height),
    show: false,
    title: WINDOW_TITLE,
    skipTaskbar: false,
    backgroundColor: "#171717",
    autoHideMenuBar: true,
    icon: path.join(__dirname, "..", "assets", "app-icon-512.png"),
    webPreferences: {
      nodeIntegration: false,
      contextIsolation: true,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
    },
  });

  win.once("ready-to-show", () => {
    win.maximize();
    win.show();
  });

  win.on("page-title-updated", (event) => {
    event.preventDefault();
    win.setTitle(WINDOW_TITLE);
  });

  win.webContents.setWindowOpenHandler(({ url }) => {
    if (isAuthNavigation(url)) {
      void win.loadURL(url);
    } else if (isTankUrl(url)) {
      createWindow(url);
    } else {
      void shell.openExternal(url);
    }
    return { action: "deny" };
  });

  win.webContents.on("will-navigate", (event, url) => {
    if (isTankUrl(url) || isAuthNavigation(url)) return;
    event.preventDefault();
    void shell.openExternal(url);
  });

  registerWindowShortcuts(win);
  void win.loadURL(initialUrl);
  return win;
}

app.setName("Tank Operator");
if (process.platform === "win32") {
  app.setAppUserModelId("life.romaine.tank-operator");
}
Menu.setApplicationMenu(null);

app.whenReady().then(() => {
  mainWindow = createWindow();

  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      mainWindow = createWindow();
    }
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

function registerWindowShortcuts(win) {
  win.webContents.on("before-input-event", (event, input) => {
    if (input.type !== "keyDown") return;

    const commandOrControl = process.platform === "darwin" ? input.meta : input.control;
    const key = input.key.toLowerCase();

    if (commandOrControl && input.shift && key === "r") {
      win.webContents.reloadIgnoringCache();
      event.preventDefault();
      return;
    }
    if ((commandOrControl && key === "r") || input.key === "F5") {
      win.webContents.reload();
      event.preventDefault();
      return;
    }
    if (commandOrControl && (input.key === "+" || input.key === "=")) {
      adjustZoom(win, ZOOM_STEP);
      event.preventDefault();
      return;
    }
    if (commandOrControl && input.key === "-") {
      adjustZoom(win, -ZOOM_STEP);
      event.preventDefault();
      return;
    }
    if (commandOrControl && input.key === "0") {
      win.webContents.setZoomFactor(1);
      event.preventDefault();
      return;
    }

    if (input.alt && input.key === "ArrowLeft") {
      const history = win.webContents.navigationHistory;
      if (history.canGoBack()) history.goBack();
      event.preventDefault();
      return;
    }
    if (input.alt && input.key === "ArrowRight") {
      const history = win.webContents.navigationHistory;
      if (history.canGoForward()) history.goForward();
      event.preventDefault();
    }
  });
}

function adjustZoom(win, delta) {
  const current = win.webContents.getZoomFactor();
  const next = Math.max(
    MIN_ZOOM_FACTOR,
    Math.min(MAX_ZOOM_FACTOR, Math.round((current + delta) * 10) / 10),
  );
  win.webContents.setZoomFactor(next);
}
