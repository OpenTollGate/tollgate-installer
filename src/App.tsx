import React, { useState, useEffect } from 'react';
import styled, { ThemeProvider } from 'styled-components';
import { theme } from './styles/theme';
import { GlobalStyle } from './styles/GlobalStyle';
import Welcome from './components/Welcome';
import RouterScanner from './components/RouterScanner';
import PasswordEntry from './components/PasswordEntry';
import Installer from './components/Installer';
import Complete from './components/Complete';
import NostrReleaseProvider from './components/NostrReleaseProvider';
import Background from './components/Background';
import { ScanResult } from '../shared/types';

// App stages
enum Stage {
  WELCOME,
  SCANNING,
  PASSWORD_ENTRY,
  INSTALLING,
  COMPLETE
}

// Router data structure
interface RouterInfo {
  ip: string;
  version?: string;
  boardName?: string;
  architecture?: string;
  compatible?: boolean;
}

// TypeScript declaration for Electron's IPC interface
declare global {
  interface Window {
    electron: {
      scanNetwork: () => Promise<ScanResult[]>;
      connectSsh: (ip: string, password?: string) => Promise<{ success: boolean; error?: string }>;
      getRouterInfo: (ip: string) => Promise<{ boardName: string; architecture: string; compatible: boolean }>;
      installTollgate: (ip: string) => Promise<{ success: boolean; step: string; progress: number; error?: string }>;
    };
  }
}

const AppContainer = styled.div`
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  color: ${props => props.theme.colors.text};
  overflow: hidden;
`;

const App: React.FC = () => {
  // State
  const [stage, setStage] = useState<Stage>(Stage.WELCOME);
  const [routers, setRouters] = useState<ScanResult[]>([]);
  const [selectedRouter, setSelectedRouter] = useState<RouterInfo | null>(null);
  const [password, setPassword] = useState<string>('');
  const [installProgress, setInstallProgress] = useState<number>(0);
  const [error, setError] = useState<string | null>(null);

  // Scan for routers
  const scanForRouters = async () => {
    try {
      setStage(Stage.SCANNING);
      setError(null);
      
      const results = await window.electron.scanNetwork();
      console.log('Scan results:', JSON.stringify(results, null, 2));
      setRouters(results);
      
      if (results.length === 0) {
        setError('No routers found. Make sure your router is powered on and connected.');
      }
    } catch (err) {
      setError('Error scanning network: ' + (err instanceof Error ? err.message : String(err)));
    }
  };

  // Select a router and attempt connection
  const selectRouter = async (ip: string, version?: string) => {
    try {
      // Find the selected router from our scan results
      const selectedScanResult = routers.find(router => router.ip === ip);
      
      if (!selectedScanResult) {
        throw new Error(`Router with IP ${ip} not found in scan results`);
      }
      
      // Extract router info from scan result
      const isOpenwrt = selectedScanResult.meta?.isOpenwrt || false;
      const boardName = selectedScanResult.meta?.boardInfo?.board_name || '';
      const architecture = selectedScanResult.meta?.boardInfo?.release?.architecture || '';
      
      // Set the selected router with information we already have
      setSelectedRouter({
        ip,
        version,
        boardName,
        architecture,
        compatible: isOpenwrt // Consider OpenWrt routers as compatible
      });
      
      // First try connecting with no password if SSH is open
      if (selectedScanResult.sshOpen) {
        const connection = await window.electron.connectSsh(ip, '');
        
        if (connection.success) {
          // Connection successful, proceed to installation
          if (isOpenwrt) {
            setStage(Stage.INSTALLING);
            await installTollgate(ip);
          } else {
            setError(`Router does not appear to be running OpenWrt, which is required for TollGateOS.`);
            setStage(Stage.SCANNING);
          }
        } else {
          // Connection failed, go to password entry
          setStage(Stage.PASSWORD_ENTRY);
        }
      } else {
        // SSH not open
        setError('SSH is not available on this router. Please enable SSH to install TollGateOS.');
        setStage(Stage.SCANNING);
      }
    } catch (err) {
      setError('Error connecting to router: ' + (err instanceof Error ? err.message : String(err)));
    }
  };

  // Submit password and retry connection
  const submitPassword = async () => {
    try {
      if (!selectedRouter) return;
      
      setError(null);
      const connection = await window.electron.connectSsh(selectedRouter.ip, password);
      
      if (connection.success) {
        // Connection successful with password, proceed to installation if it's OpenWrt
        if (selectedRouter.compatible) {
          setStage(Stage.INSTALLING);
          await installTollgate(selectedRouter.ip);
        } else {
          setError(`Router does not appear to be running OpenWrt, which is required for TollGateOS.`);
          setStage(Stage.SCANNING);
        }
      } else {
        setError('Failed to connect: ' + (connection.error || 'Invalid password. Please try again.'));
      }
    } catch (err) {
      if (err instanceof Error && err.message.includes('Connection to') && err.message.includes('timed out')) {
        setError('Connection timed out. Please check that your router is powered on and accessible.');
      } else if (err instanceof Error && err.message.includes('Authentication failed')) {
        setError('Authentication failed. Please check your password and try again.');
      } else {
        setError('Error connecting to router: ' + (err instanceof Error ? err.message : String(err)));
      }
    }
  };

  // Install TollGate OS
  const installTollgate = async (ip: string) => {
    try {
      setError(null);
      
      const result = await window.electron.installTollgate(ip);
      
      if (result.success) {
        setStage(Stage.COMPLETE);
      } else {
        setError('Installation failed: ' + (result.error || 'Unknown error'));
        setStage(Stage.SCANNING);
      }
    } catch (err) {
      setError('Error during installation: ' + (err instanceof Error ? err.message : String(err)));
      setStage(Stage.SCANNING);
    }
  };


  // Reset to start a new installation
  const startNewInstall = () => {
    setSelectedRouter(null);
    setPassword('');
    setInstallProgress(0);
    setError(null);
    setStage(Stage.WELCOME);
    scanForRouters();
  };

  return (
    <ThemeProvider theme={theme}>
      <GlobalStyle />
      <NostrReleaseProvider>
        <AppContainer>
          <Background />
          {stage === Stage.WELCOME && (
            <Welcome onStart={scanForRouters} />
          )}
        
        {stage === Stage.SCANNING && (
          <RouterScanner
            routers={routers}
            onSelectRouter={(ip, version) => selectRouter(ip, version)}
            error={error}
            onRescan={scanForRouters}
            setRouters={setRouters}
          />
        )}
        
        {stage === Stage.PASSWORD_ENTRY && (
          <PasswordEntry
            router={selectedRouter}
            routerIp={selectedRouter?.ip || ''}
            password={password}
            setPassword={setPassword}
            onSubmit={submitPassword}
            error={error}
            onBack={() => setStage(Stage.SCANNING)}
          />
        )}
        
        {stage === Stage.INSTALLING && (
          <Installer 
            router={selectedRouter} 
            progress={installProgress} 
            error={error} 
          />
        )}
        
        {stage === Stage.COMPLETE && (
          <Complete
            router={selectedRouter}
            onFlashNext={startNewInstall}
          />
        )}
        </AppContainer>
      </NostrReleaseProvider>
    </ThemeProvider>
  );
};

export default App;