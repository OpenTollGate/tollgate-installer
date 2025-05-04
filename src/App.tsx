import React, { useState, useEffect } from 'react';
import styled, { ThemeProvider } from 'styled-components';
import { theme } from './styles/theme';
import { GlobalStyle } from './styles/GlobalStyle';
import Welcome from './components/Welcome';
import RouterScanner from './components/RouterScanner';
import PasswordEntry from './components/PasswordEntry';
import Installer from './components/Installer';
import Complete from './components/Complete';
import NostrVersionProvider from './components/NostrVersionProvider';

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
  boardName?: string;
  architecture?: string;
  compatible?: boolean;
}

// TypeScript declaration for Electron's IPC interface
declare global {
  interface Window {
    electron: {
      scanNetwork: () => Promise<Array<{ ip: string; sshOpen: boolean; meta?: any }>>;
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
  background-color: ${props => props.theme.colors.background};
  color: ${props => props.theme.colors.text};
`;

const App: React.FC = () => {
  // State
  const [stage, setStage] = useState<Stage>(Stage.WELCOME);
  const [routers, setRouters] = useState<Array<{ ip: string; sshOpen: boolean; meta?: any }>>([]);
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
      setRouters(results);
      
      if (results.length === 0) {
        setError('No routers found. Make sure your router is powered on and connected.');
      }
    } catch (err) {
      setError('Error scanning network: ' + (err instanceof Error ? err.message : String(err)));
    }
  };

  // Select a router and attempt connection
  const selectRouter = async (ip: string) => {
    try {
      // First try connecting with no password
      const connection = await window.electron.connectSsh(ip, '');
      
      if (connection.success) {
        // Connection successful, get router info
        await getRouterInfo(ip);
      } else {
        // Connection failed, go to password entry
        setSelectedRouter({ ip });
        setStage(Stage.PASSWORD_ENTRY);
      }
    } catch (err) {
      setError('Error connecting to router: ' + (err instanceof Error ? err.message : String(err)));
    }
  };

  // Get router information
  const getRouterInfo = async (ip: string) => {
    try {
      const info = await window.electron.getRouterInfo(ip);
      setSelectedRouter({ ip, ...info });
      
      if (info.compatible) {
        setStage(Stage.INSTALLING);
        await installTollgate(ip);
      } else {
        setError(`Router model ${info.boardName} with architecture ${info.architecture} is not compatible with TollGateOS.`);
        setStage(Stage.SCANNING);
      }
    } catch (err) {
      if (err instanceof Error && err.message.includes('No SSH connection')) {
        setError('Lost connection to router. Please try connecting again or check that the router is powered on and accessible.');
      } else {
        setError('Error getting router info: ' + (err instanceof Error ? err.message : String(err)));
      }
      // Return to scanning stage on error
      setStage(Stage.SCANNING);
    }
  };

  // Submit password and retry connection
  const submitPassword = async () => {
    try {
      if (!selectedRouter) return;
      
      setError(null);
      const connection = await window.electron.connectSsh(selectedRouter.ip, password);
      
      if (connection.success) {
        await getRouterInfo(selectedRouter.ip);
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
      <NostrVersionProvider>
        <AppContainer>
          {stage === Stage.WELCOME && (
            <Welcome onStart={scanForRouters} />
          )}
        
        {stage === Stage.SCANNING && (
          <RouterScanner 
            routers={routers} 
            onSelectRouter={selectRouter} 
            error={error} 
            onRescan={scanForRouters}
          />
        )}
        
        {stage === Stage.PASSWORD_ENTRY && (
          <PasswordEntry 
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
      </NostrVersionProvider>
    </ThemeProvider>
  );
};

export default App;