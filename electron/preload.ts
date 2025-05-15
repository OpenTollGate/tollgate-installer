import { contextBridge, ipcRenderer } from 'electron';
import { ScanResult } from '../shared/types';

// Expose protected IPC methods to the renderer process
contextBridge.exposeInMainWorld('electron', {
  // Network scanning
  scanNetwork: async (): Promise<ScanResult[]> => {
    return await ipcRenderer.invoke('scan-network');
  },

  // SSH connection
  connectSsh: async (ip: string, password?: string): Promise<{ success: boolean; error?: string }> => {
    return await ipcRenderer.invoke('connect-ssh', ip, password);
  },

  // Installation
  installTollgate: async (ip: string): Promise<{
    success: boolean;
    step: string;
    progress: number;
    error?: string;
  }> => {
    return await ipcRenderer.invoke('install-tollgate', ip);
  },

  // Check device
  checkDevice: async (ip: string): Promise<ScanResult | null> => {
    return await ipcRenderer.invoke('check-device', ip);
  },
});