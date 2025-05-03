import { app, BrowserWindow, ipcMain } from 'electron';
import * as path from 'path';
import * as isDev from 'electron-is-dev';
import { autoUpdater } from 'electron-updater';
import { NetworkScanner } from './services/network-scanner';
import { SshConnector } from './services/ssh-connector';
import { InstallerEngine } from './services/installer-engine';
import { UpdateManager } from './services/update-manager';

let mainWindow: BrowserWindow | null = null;

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 900,
    height: 680,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
    },
  });

  const startUrl = isDev
    ? 'http://localhost:3000'
    : `file://${path.join(__dirname, '../renderer/index.html')}`;

  mainWindow.loadURL(startUrl);

  // Auto-open DevTools in development
  if (isDev) {
    mainWindow.webContents.openDevTools();
  }

  mainWindow.on('closed', () => {
    mainWindow = null;
  });

  // Initialize auto-updater
  if (!isDev) {
    autoUpdater.checkForUpdatesAndNotify();
  }
}

// Create window when Electron is ready
app.whenReady().then(() => {
  createWindow();

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createWindow();
    }
  });

  // Initialize services
  const networkScanner = new NetworkScanner();
  const sshConnector = new SshConnector();
  const installerEngine = new InstallerEngine();
  const updateManager = new UpdateManager();

  // Register IPC handlers
  setupIpcHandlers(networkScanner, sshConnector, installerEngine, updateManager);
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});

function setupIpcHandlers(
  networkScanner: NetworkScanner,
  sshConnector: SshConnector,
  installerEngine: InstallerEngine,
  updateManager: UpdateManager
) {
  // Network scanning
  ipcMain.handle('scan-network', async () => {
    return await networkScanner.scan();
  });

  // SSH connection
  ipcMain.handle('connect-ssh', async (_, ip: string, password?: string) => {
    return await sshConnector.connect(ip, password);
  });

  // Get router information
  ipcMain.handle('get-router-info', async (_, ip: string) => {
    return await sshConnector.getRouterInfo(ip);
  });

  // Installation
  ipcMain.handle('install-tollgate', async (_, ip: string) => {
    return await installerEngine.install(ip);
  });

  // Updates
  ipcMain.handle('check-for-updates', async () => {
    return await updateManager.checkForUpdates();
  });
}