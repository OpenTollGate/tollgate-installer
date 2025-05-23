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
import { NDKEvent } from '@nostr-dev-kit/ndk';

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
      checkDevice: (ip: string) => Promise<ScanResult | null>;
      installTollgate: (ip: string, releaseEvent: string) => Promise<{ success: boolean; step: string; progress: number; error?: string }>;
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
  const [selectedRelease, setSelectedRelease] = useState<NDKEvent | null>(null);
  const [password, setPassword] = useState<string>('');
  const [installProgress, setInstallProgress] = useState<number>(0);
  const [error, setError] = useState<string | null>(null);
  const [currentInstallStep, setCurrentInstallStep] = useState<string>('');
  const [failedStep, setFailedStep] = useState<string | null>(null);

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

  // Select a router and prepare for installation or password entry
  const selectRouter = async (ip: string, version?: string, manualEntry?: boolean, releaseEvent?: NDKEvent) => {
    try {
      console.log(`Starting router selection for IP: ${ip}, with release:`, releaseEvent);
      
      // Important: First store any provided release event to ensure it's available
      if (releaseEvent) {
        console.log(`Storing release event for ${ip}:`, releaseEvent);
        setSelectedRelease(releaseEvent);
      }
      
      // Find the selected router from our scan results
      let selectedScanResult = routers.find(router => router.ip === ip);
      
      // If this is a manual entry and we don't have it in scan results yet,
      // try to check the device directly
      if (!selectedScanResult && manualEntry) {
        console.log(`Manual entry for ${ip}, checking device directly...`);
        const deviceResult = await window.electron.checkDevice(ip);
        
        if (deviceResult) {
          console.log(`Found device info for manual IP: ${ip}`, deviceResult);
          // Add to routers state for future reference
          setRouters(prevRouters => [...prevRouters, deviceResult]);
          selectedScanResult = deviceResult;
        } else {
          throw new Error(`Could not connect to router at ${ip}`);
        }
      } else if (!selectedScanResult) {
        throw new Error(`Router with IP ${ip} not found in scan results`);
      }
      
      // Extract router info from scan result
      const isOpenwrt = selectedScanResult.meta?.isOpenwrt || false;
      const boardName = selectedScanResult.meta?.boardInfo?.board_name || '';
      const architecture = selectedScanResult.meta?.boardInfo?.release?.architecture || '';
      
      // Create the router info object
      const routerInfo = {
        ip,
        version,
        boardName,
        architecture,
        compatible: isOpenwrt // Consider OpenWrt routers as compatible
      };
      
      // Set the selected router
      console.log('Setting selected router to:', routerInfo);
      setSelectedRouter(routerInfo);
      
      // Check if the release is selected before proceeding
      if (!releaseEvent) {
        console.log('No release provided. Router was selected but installation cannot proceed without a release');
        return;
      }
      
      // Only try to connect if SSH is open
      if (selectedScanResult.sshOpen) {
        console.log(`Attempting SSH connection to ${ip}`);
        const connection = await window.electron.connectSsh(ip, '');
        
        if (connection.success) {
          // Connection successful
          if (isOpenwrt) {
            // Use a function to proceed to installation that takes all required params directly
            // This avoids issues with state updates not being immediately available
            proceedToInstallation(ip, routerInfo, releaseEvent);
          } else {
            setError(`Router does not appear to be running OpenWrt, which is required for TollGateOS.`);
            setStage(Stage.SCANNING);
          }
        } else {
          // Connection failed, go to password entry
          console.log('SSH connection failed, proceeding to password entry');
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

  // New function to handle the installation process with all required parameters
  const proceedToInstallation = async (ip: string, router: RouterInfo, release: NDKEvent) => {
    console.log(`Proceeding to installation for IP: ${ip}`);
    console.log('Router info:', router);
    console.log('Release:', release);
    
    // Make sure states are set (even if we already have the values as params)
    setSelectedRouter(router);
    setSelectedRelease(release);
    
    // Change the stage to installing
    setStage(Stage.INSTALLING);
    
    // Wait a tiny bit to ensure React has updated the UI
    await new Promise(resolve => setTimeout(resolve, 50));
    
    // Start the installation using the parameters directly
    try {
      await installTollgateWithParams(ip, release);
    } catch (err) {
      console.error('Installation error:', err);
      setError('Error during installation: ' + (err instanceof Error ? err.message : String(err)));
    }
  };
  
  // Direct installation function that takes the release as a parameter
  const installTollgateWithParams = async (ip: string, release: NDKEvent) => {
    try {
      // Reset installation state
      setError(null);
      setInstallProgress(0);
      setCurrentInstallStep('');
      setFailedStep(null);
      
      console.log(`Installing TollGate OS directly on ${ip} with release:`, release);
      
      // Start the installation process using the provided release
      const result = await window.electron.installTollgate(ip, release.serialize());
      
      // Update based on final result
      if (result.success) {
        setStage(Stage.COMPLETE);
      } else {
        setError(`Installation failed at step "${result.step}": ${result.error || 'Unknown error'}`);
        setFailedStep(result.step);
        // Stay on the installer page
      }
    } catch (err) {
      console.error('Error in direct installation:', err);
      setError('Error during installation: ' + (err instanceof Error ? err.message : String(err)));
      setFailedStep('unknown');
    }
  };

  // Submit password and retry connection
  const submitPassword = async () => {
    try {
      if (!selectedRouter) {
        console.error('No router selected in password entry');
        return;
      }
      
      if (!selectedRelease) {
        console.error('No release selected in password entry');
        setError('No release selected. Please select a release before installing.');
        setStage(Stage.SCANNING);
        return;
      }
      
      setError(null);
      const connection = await window.electron.connectSsh(selectedRouter.ip, password);
      
      if (connection.success) {
        // Connection successful with password, proceed to installation if it's OpenWrt
        if (selectedRouter.compatible) {
          // Use the direct proceed function with the current state values
          proceedToInstallation(selectedRouter.ip, selectedRouter, selectedRelease);
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
      // Reset installation state
      setError(null);
      setInstallProgress(0);
      setCurrentInstallStep('');
      setFailedStep(null);
      
      if (!selectedRelease) {
        // Log what's happening for debugging
        console.log('No release selected for installation. Router IP:', ip);
        console.log('Selected router info:', selectedRouter);
        
        // Check if the router was manually added
        const routerInList = routers.find(router => router.ip === ip);
        if (routerInList) {
          // The router exists in our list, but no release was selected
          setError('No release selected. Please select a release before installing.');
          setStage(Stage.SCANNING);
          return;
        } else {
          // Something went wrong with the router data
          setError('Router information not found. Please try adding the router again.');
          setStage(Stage.SCANNING);
          return;
        }
      }
      
      console.log(`Installing TollGate OS on ${ip} with release:`, selectedRelease);
      
      // Set up event listener for installation progress updates
      const onInstallProgress = (result: { success: boolean; step: string; progress: number; error?: string }) => {
        console.log('Installation progress:', result);
        
        // Update the current step
        setCurrentInstallStep(result.step);
        
        // Update progress
        setInstallProgress(result.progress);
        
        // If there's an error, update the error state and record which step failed
        if (!result.success && result.error) {
          setError(`Error during ${result.step}: ${result.error}`);
          setFailedStep(result.step);
        }
      };
      
      // Start the installation process
      const result = await window.electron.installTollgate(ip, selectedRelease.serialize());
      
      // Update based on final result
      if (result.success) {
        setStage(Stage.COMPLETE);
      } else {
        setError(`Installation failed at step "${result.step}": ${result.error || 'Unknown error'}`);
        setFailedStep(result.step);
        // Stay on the installer page instead of returning to scanner
        // setStage(Stage.SCANNING);
      }
    } catch (err) {
      setError('Error during installation: ' + (err instanceof Error ? err.message : String(err)));
      setFailedStep('unknown');
      // Stay on the installer page instead of returning to scanner
      // setStage(Stage.SCANNING);
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
            onSelectRouter={(ip, version, manualEntry, releaseEvent) => selectRouter(ip, version, manualEntry, releaseEvent)}
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
            currentStep={currentInstallStep}
            failedStep={failedStep}
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