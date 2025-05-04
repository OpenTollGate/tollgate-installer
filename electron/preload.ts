import { contextBridge, ipcRenderer } from 'electron';

// Expose protected IPC methods to the renderer process
contextBridge.exposeInMainWorld('electron', {
  // Network scanning
  scanNetwork: async (): Promise<Array<{ ip: string; sshOpen: boolean; meta?: any }>> => {
    return await ipcRenderer.invoke('scan-network');
  },

  // SSH connection
  connectSsh: async (ip: string, password?: string): Promise<{ success: boolean; error?: string }> => {
    return await ipcRenderer.invoke('connect-ssh', ip, password);
  },

  // Get router information
  getRouterInfo: async (ip: string): Promise<{
    boardName: string;
    architecture: string;
    compatible: boolean;
  }> => {
    return await ipcRenderer.invoke('get-router-info', ip);
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

});