import React, { useState, useEffect } from 'react';
import styled from 'styled-components';
import Button from './common/Button';
import Input from './common/Input';
import ProgressBar from './common/ProgressBar';
import PageContainer from './common/PageContainer';
import { useNostrReleases } from './NostrReleaseProvider';
import RouterItem from './RouterItem';
import { NDKEvent } from '@nostr-dev-kit/ndk';
import { ScanResult } from '../../shared/types';

interface RouterScannerProps {
  routers: ScanResult[];
  onSelectRouter: (ip: string, releaseId?: string, manualEntry?: boolean, releaseEvent?: NDKEvent) => void;
  error: string | null;
  onRescan: () => void;
  setRouters?: (routers: ScanResult[]) => void;
}

const RouterList = styled.div`
  margin: 1.5rem 0;
`;

const NoRoutersMessage = styled.div`
  text-align: center;
  padding: 2rem;
  color: ${props => props.theme.colors.textSecondary};
  background-color: #FFFFFF;
  border-radius: ${props => props.theme.radii.md};
`;

const ErrorMessage = styled.div`
  color: ${props => props.theme.colors.error};
  background-color: ${props => props.theme.colors.primaryLight};
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
`;

const LoadingState = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 2rem;
  background-color: #FFFFFF;
  border-radius: ${props => props.theme.radii.md};
`;

const LoadingText = styled.div`
  margin-top: 1rem;
  font-size: ${props => props.theme.fontSizes.md};
  color: ${props => props.theme.colors.textSecondary};
`;

const SuccessMessage = styled.div`
  color: ${props => props.theme.colors.success};
  background-color: rgba(16, 185, 129, 0.1); /* Lighter version of success color */
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
  display: flex;
  align-items: center;
  
  &:before {
    content: "✓";
    margin-right: 0.5rem;
    font-weight: bold;
  }
`;

const ManualIpSection = styled.div`
  background-color: #FFFFFF;
  border-radius: ${props => props.theme.radii.md};
  padding: 1.5rem;
  margin-top: 1.5rem;
`;

const ManualIpTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  margin-bottom: 1rem;
  color: ${props => props.theme.colors.text};
`;

const ManualIpForm = styled.div`
  display: flex;
  gap: 1rem;
  align-items: flex-end;

  @media (max-width: 600px) {
    flex-direction: column;
    align-items: stretch;
  }
`;

const FooterButtons = styled.div`
  display: flex;
  justify-content: center;
  gap: 1rem;
  margin-top: 2rem;
`;

const RouterScanner: React.FC<RouterScannerProps> = ({
  routers,
  onSelectRouter,
  error,
  onRescan,
  setRouters
}) => {
  // State
  const [isScanning, setIsScanning] = useState(false);
  const [scanProgress, setScanProgress] = useState(0);
  const [selectedReleaseIds, setSelectedReleaseIds] = useState<Record<string, string>>({});
  // Store the full NDKEvent objects for selected releases
  const [selectedReleases, setSelectedReleases] = useState<Record<string, NDKEvent>>({});
  const [manualIp, setManualIp] = useState('');
  const [manualIpError, setManualIpError] = useState<string | undefined>(undefined);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);
  const { releases, loading } = useNostrReleases();

  // Scanning progress effect
  useEffect(() => {
    if (routers.length === 0 && !error) {
      setIsScanning(true);
      
      const interval = setInterval(() => {
        setScanProgress(prev => {
          const newProgress = prev + 5;
          if (newProgress >= 100) {
            clearInterval(interval);
            setIsScanning(false);
            return 100;
          }
          return newProgress;
        });
      }, 200);
      
      return () => clearInterval(interval);
    } else {
      setIsScanning(false);
      setScanProgress(100);
    }
  }, [routers, error]);

  // Event handlers
  const handleRescan = () => {
    setScanProgress(0);
    setIsScanning(true);
    onRescan();
  };

  const handleReleaseSelect = (routerIp: string, release: NDKEvent) => {
    // Store the release ID
    setSelectedReleaseIds(prev => ({
      ...prev,
      [routerIp]: release.id
    }));
    
    // Store the full release object
    setSelectedReleases(prev => ({
      ...prev,
      [routerIp]: release
    }));
  };

  const handleConnect = (routerIp: string, releaseId?: string) => {
    // Find the full release object if we have a releaseId
    const releaseObject = releaseId ? selectedReleases[routerIp] : undefined;
    
    // Pass the router IP, releaseId, and the full release object
    onSelectRouter(routerIp, releaseId, false, releaseObject);
  };

  // Validate IP address format
  const validateIpAddress = (ip: string): boolean => {
    // Regular expression for IPv4 validation
    const ipRegex = /^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/;
    return ipRegex.test(ip);
  };

  // Handle manual IP input
  const handleManualIpChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const value = e.target.value;
    setManualIp(value);
    setManualIpError(undefined); // Clear any existing errors when the input changes
    setSuccessMessage(null); // Clear success message when user starts typing
  };

  // Handle connect to manual IP
  const handleManualConnect = async () => {
    if (!manualIp) {
      setManualIpError('Please enter an IP address');
      setSuccessMessage(null);
      return;
    }

    if (!validateIpAddress(manualIp)) {
      setManualIpError('Please enter a valid IP address');
      setSuccessMessage(null);
      return;
    }

    // Check if this IP already exists in the routers list
    const existingRouter = routers.find(router => router.ip === manualIp);
    
    if (existingRouter) {
      // If it exists, just show a message
      setSuccessMessage(`Router ${manualIp} is already in the list. Please select a release version before connecting.`);
      setManualIpError(undefined);
      return;
    }
    
    // Show connecting state
    setIsScanning(true);
    setSuccessMessage(null);
    
    try {
      // Use the checkDevice function to get a properly enriched ScanResult
      const deviceResult = await window.electron.checkDevice(manualIp);
      
      if (!deviceResult) {
        setManualIpError('Could not connect to router: SSH port not accessible');
        setSuccessMessage(null);
        setIsScanning(false);
        return;
      }
      
      if (setRouters) {
        // Add the properly populated device to the scan results
        const updatedRouters = [...routers, deviceResult];
        setRouters(updatedRouters);
        
        // Clear the input field for better UX
        setManualIp('');
        
        // Show success message and clear error
        setManualIpError(undefined);
        setSuccessMessage(`Router ${deviceResult.ip} added successfully! Please select a release version before connecting.`);
        
        // Don't automatically connect - user needs to select a release first
        // The router will appear in the list, and they can select a release and
        // then click Connect from there
      } else {
        // If setRouters is not available (unlikely), just try to connect
        // but we shouldn't get here in normal operation
        onSelectRouter(manualIp, undefined, true, undefined);
      }
      
    } catch (error) {
      // Show error if we couldn't get router info
      setManualIpError(`Could not connect to router: ${error instanceof Error ? error.message : String(error)}`);
      setSuccessMessage(null);
    } finally {
      setIsScanning(false);
    }
  };

  const renderContent = () => {
    if (isScanning) {
      return (
        <LoadingState>
          <ProgressBar progress={scanProgress} color="primary" />
          <LoadingText>Scanning network for routers...</LoadingText>
        </LoadingState>
      );
    }
    
    if (routers.length === 0) {
      return (
        <NoRoutersMessage>
          No routers found on your network.
          <br />
          Make sure your router is powered on and connected.
        </NoRoutersMessage>
      );
    }
    
    return (
      <RouterList>
        {routers.map((router) => (
          <RouterItem
            key={router.ip}
            router={router}
            releases={releases}
            selectedReleaseId={selectedReleaseIds[router.ip]}
            onReleaseSelect={handleReleaseSelect}
            onConnect={handleConnect}
          />
        ))}
      </RouterList>
    );
  };

  return (
    <PageContainer 
      title="Detected Routers" 
      subtitle="Select a router to install TollGate OS"
    >
      {error && <ErrorMessage>{error}</ErrorMessage>}
      {successMessage && <SuccessMessage>{successMessage}</SuccessMessage>}
      {renderContent()}

      <ManualIpSection>
        <ManualIpTitle>Add router by IP address</ManualIpTitle>
        <ManualIpForm>
          <Input
            label="Router IP Address"
            placeholder="192.168.1.1"
            value={manualIp}
            onChange={handleManualIpChange}
            error={manualIpError}
            fullWidth
          />
          <Button
            variant="primary"
            onClick={handleManualConnect}
            disabled={isScanning}
          >
            Add Router
          </Button>
        </ManualIpForm>
      </ManualIpSection>

      <FooterButtons>
        <Button
          variant="outline"
          onClick={handleRescan}
          disabled={isScanning}
        >
          Scan Again
        </Button>
      </FooterButtons>
    </PageContainer>
  );
};

export default RouterScanner;
