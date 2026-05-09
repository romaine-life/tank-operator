const { app, BrowserWindow, Menu, ipcMain, screen, shell } = require("electron");
const { CryptoProvider, PublicClientApplication } = require("@azure/msal-node");
const fs = require("node:fs");
const path = require("node:path");
const { randomUUID } = require("node:crypto");

const DEFAULT_TANK_URL = "https://tank.romaine.life";
const tankUrl = normalizeUrl(process.env.TANK_OPERATOR_URL || DEFAULT_TANK_URL);
const tankOrigin = new URL(tankUrl).origin;
const DESKTOP_AUTH_PROTOCOL = "tank-operator";
const DESKTOP_AUTH_REDIRECT_URI = `${DESKTOP_AUTH_PROTOCOL}://auth`;
const DESKTOP_AUTH_SCOPES = ["User.Read", "openid", "profile", "email"];
const DESKTOP_AUTH_TIMEOUT_MS = 5 * 60 * 1000;
const MIN_ZOOM_FACTOR = 0.5;
const MAX_ZOOM_FACTOR = 3.0;
const ZOOM_STEP = 0.1;
const DESKTOP_PREFS_FILE = "desktop-preferences.json";
const WINDOW_TITLE = "Tank";

let mainWindow = null;
let pendingDesktopAuth = null;
let desktopZoomFactor = 1;

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
      preload: path.join(__dirname, "preload.js"),
    },
  });

  win.webContents.setZoomFactor(desktopZoomFactor);

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
registerProtocolClient();
Menu.setApplicationMenu(null);

const gotSingleInstanceLock = app.requestSingleInstanceLock();
if (!gotSingleInstanceLock) {
  app.quit();
} else {
  app.on("second-instance", (_event, argv) => {
    handleProtocolCallback(argv.find((arg) => arg.startsWith(`${DESKTOP_AUTH_PROTOCOL}://`)));
    if (mainWindow) {
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.focus();
    }
  });
}

app.on("open-url", (event, url) => {
  event.preventDefault();
  handleProtocolCallback(url);
});

ipcMain.handle("desktop-auth:microsoft-login", async (event) => {
  assertTrustedSender(event);
  return startDesktopMicrosoftLogin();
});

app.whenReady().then(() => {
  desktopZoomFactor = loadDesktopZoomFactor();
  mainWindow = createWindow();
  handleProtocolCallback(
    process.argv.find((arg) => arg.startsWith(`${DESKTOP_AUTH_PROTOCOL}://`)),
  );

  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      mainWindow = createWindow();
    }
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});

function registerProtocolClient() {
  if (process.defaultApp && process.argv.length >= 2) {
    app.setAsDefaultProtocolClient(DESKTOP_AUTH_PROTOCOL, process.execPath, [
      path.resolve(process.argv[1]),
    ]);
    return;
  }
  app.setAsDefaultProtocolClient(DESKTOP_AUTH_PROTOCOL);
}

function assertTrustedSender(event) {
  const senderUrl = event.senderFrame?.url || "";
  if (!isTankUrl(senderUrl)) {
    throw new Error("desktop auth is only available to the Tank Operator app");
  }
}

async function fetchAuthConfig() {
  const res = await fetch(new URL("/api/config", tankOrigin));
  if (!res.ok) throw new Error(`config fetch failed: ${res.status}`);
  const config = await res.json();
  if (!config.entra_client_id) throw new Error("backend has no ENTRA_CLIENT_ID");
  return config;
}

async function startDesktopMicrosoftLogin() {
  if (pendingDesktopAuth) {
    pendingDesktopAuth.reject(new Error("replaced by a newer desktop auth request"));
    clearPendingDesktopAuth();
  }

  const config = await fetchAuthConfig();
  const client = new PublicClientApplication({
    auth: {
      clientId: config.entra_client_id,
      authority: config.entra_authority,
    },
  });
  const cryptoProvider = new CryptoProvider();
  const pkce = await cryptoProvider.generatePkceCodes();
  const state = randomUUID();

  const authUrl = await client.getAuthCodeUrl({
    scopes: DESKTOP_AUTH_SCOPES,
    redirectUri: DESKTOP_AUTH_REDIRECT_URI,
    codeChallenge: pkce.challenge,
    codeChallengeMethod: "S256",
    prompt: "select_account",
    state,
  });

  const result = new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      reject(new Error("Microsoft sign-in timed out"));
      clearPendingDesktopAuth();
    }, DESKTOP_AUTH_TIMEOUT_MS);
    pendingDesktopAuth = {
      client,
      codeVerifier: pkce.verifier,
      reject,
      resolve,
      state,
      timeout,
    };
  });

  await shell.openExternal(authUrl);
  return result;
}

async function handleProtocolCallback(url) {
  if (!url || !pendingDesktopAuth) return false;
  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  if (parsed.protocol !== `${DESKTOP_AUTH_PROTOCOL}:` || parsed.hostname !== "auth") {
    return false;
  }

  const pending = pendingDesktopAuth;
  if (parsed.searchParams.get("state") !== pending.state) {
    pending.reject(new Error("desktop auth state mismatch"));
    clearPendingDesktopAuth();
    return true;
  }

  const error = parsed.searchParams.get("error");
  if (error) {
    const description = parsed.searchParams.get("error_description") || error;
    pending.reject(new Error(description));
    clearPendingDesktopAuth();
    return true;
  }

  const code = parsed.searchParams.get("code");
  if (!code) {
    pending.reject(new Error("Microsoft did not return an authorization code"));
    clearPendingDesktopAuth();
    return true;
  }

  try {
    const token = await pending.client.acquireTokenByCode({
      code,
      codeVerifier: pending.codeVerifier,
      redirectUri: DESKTOP_AUTH_REDIRECT_URI,
      scopes: DESKTOP_AUTH_SCOPES,
    });
    if (!token?.idToken) throw new Error("Microsoft did not return an ID token");
    pending.resolve({ idToken: token.idToken });
  } catch (e) {
    pending.reject(e);
  } finally {
    clearPendingDesktopAuth();
    if (mainWindow) {
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.focus();
    }
  }
  return true;
}

function clearPendingDesktopAuth() {
  if (pendingDesktopAuth?.timeout) clearTimeout(pendingDesktopAuth.timeout);
  pendingDesktopAuth = null;
}

function desktopPrefsPath() {
  return path.join(app.getPath("userData"), DESKTOP_PREFS_FILE);
}

function clampZoomFactor(value) {
  if (!Number.isFinite(value)) return 1;
  return Math.max(MIN_ZOOM_FACTOR, Math.min(MAX_ZOOM_FACTOR, value));
}

function normalizeZoomFactor(value) {
  return Math.round(clampZoomFactor(value) * 10) / 10;
}

function loadDesktopZoomFactor() {
  try {
    const raw = fs.readFileSync(desktopPrefsPath(), "utf8");
    const prefs = JSON.parse(raw);
    return normalizeZoomFactor(Number(prefs.zoomFactor));
  } catch {
    return 1;
  }
}

function saveDesktopZoomFactor(value) {
  try {
    fs.writeFileSync(
      desktopPrefsPath(),
      `${JSON.stringify({ zoomFactor: value }, null, 2)}\n`,
      "utf8",
    );
  } catch {
    // Zoom should still apply to open windows even if preference persistence fails.
  }
}

function setDesktopZoomFactor(value) {
  desktopZoomFactor = normalizeZoomFactor(value);
  saveDesktopZoomFactor(desktopZoomFactor);
  for (const win of BrowserWindow.getAllWindows()) {
    win.webContents.setZoomFactor(desktopZoomFactor);
  }
}

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
      adjustZoom(ZOOM_STEP);
      event.preventDefault();
      return;
    }
    if (commandOrControl && input.key === "-") {
      adjustZoom(-ZOOM_STEP);
      event.preventDefault();
      return;
    }
    if (commandOrControl && input.key === "0") {
      setDesktopZoomFactor(1);
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

function adjustZoom(delta) {
  adjustDesktopZoom(delta);
}

function adjustDesktopZoom(delta) {
  setDesktopZoomFactor(desktopZoomFactor + delta);
}
