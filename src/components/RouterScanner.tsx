import React, { useState, useEffect } from 'react';
import styled from 'styled-components';
import Button from './common/Button';
import ProgressBar from './common/ProgressBar';
import PageContainer from './common/PageContainer';
import { useNostrVersions } from './NostrVersionProvider';
import { NDKEvent } from '@nostr-dev-kit/ndk';

interface RouterScannerProps {
  routers: Array<{ ip: string; sshOpen: boolean; meta?: any }>;
  onSelectRouter: (ip: string, version?: string) => void;
  error: string | null;
  onRescan: () => void;
}

const RouterList = styled.div`
  margin: 1.5rem 0;
`;

const RouterItem = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1rem;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 0.75rem;
  transition: all ${props => props.theme.transitions.fast};
  
  &:hover {
    border-color: ${props => props.theme.colors.primary};
    box-shadow: ${props => props.theme.shadows.sm};
  }
`;

const RouterInfo = styled.div`
  flex: 1;
`;

const RouterIP = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  font-size: ${props => props.theme.fontSizes.md};
`;

const RouterDetail = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  margin-top: 0.25rem;
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

const FooterButtons = styled.div`
  display: flex;
  justify-content: center;
  gap: 1rem;
  margin-top: 2rem;
`;

const VersionSelectorContainer = styled.div`
  margin-bottom: 1rem;
  position: relative;
`;

const VersionSelectorButton = styled(Button)`
  width: 100%;
  justify-content: space-between;
  padding: 0.75rem 1rem;
`;

const VersionList = styled.ul`
  position: absolute;
  top: 100%;
  left: 0;
  width: 100%;
  background: white;
  border: 1px solid ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  list-style: none;
  padding: 0;
  margin: 0;
  z-index: 2;
  max-height: 200px;
  overflow-y: auto;
`;

interface VersionItemProps {
  $isSelected: boolean;
  $isCompatible: boolean;
}

const VersionItem = styled.li<VersionItemProps>`
  padding: 0.5rem 1rem;
  cursor: pointer;
  background-color: ${props => props.$isSelected ? props.theme.colors.primaryLight : 'transparent'};
  color: ${props => props.$isCompatible ? props.theme.colors.text : props.theme.colors.textSecondary};
  opacity: ${props => props.$isCompatible ? 1 : 0.6};
  
  &:hover {
    background-color: ${props => props.theme.colors.primaryLight};
  }
`;

const VersionText = styled.span`
  font-size: ${props => props.theme.fontSizes.md};
`;

const RouterScanner: React.FC<RouterScannerProps> = ({
  routers,
  onSelectRouter,
  error,
  onRescan
}) => {
  const [isScanning, setIsScanning] = useState(false);
  const [scanProgress, setScanProgress] = useState(0);
  const [showVersionSelector, setShowVersionSelector] = useState(false);
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null);
  const { versions, loading, compatibleVersions } = useNostrVersions();

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

  const handleRescan = () => {
    setScanProgress(0);
    setIsScanning(true);
    onRescan();
  };

  const handleVersionSelect = (version: string) => {
    setSelectedVersion(version);
    setShowVersionSelector(false);
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
          <RouterItem key={router.ip}>
            <RouterInfo>
              <RouterIP>{router.ip}</RouterIP>
              <RouterDetail>
                {router.meta?.isGateway ? 'Default Gateway' : 'OpenWRT Router'}
                {router.meta?.status === 'no-ssh' && ' (SSH Not Active)'}
              </RouterDetail>
            </RouterInfo>
            <Button
              variant="primary"
              size="small"
              onClick={() => onSelectRouter(router.ip, selectedVersion || undefined)}
              disabled={router.meta?.status === 'no-ssh' || !selectedVersion}
            >
              Connect
            </Button>
          </RouterItem>
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
      {renderContent()}
      
      <VersionSelectorContainer>
        <VersionSelectorButton
          variant="outline"
          onClick={() => setShowVersionSelector(!showVersionSelector)}
        >
          <span>{selectedVersion || 'Select TollGateOS Version'}</span>
          <span>▼</span>
        </VersionSelectorButton>
        {showVersionSelector && (
          <VersionList>
            {compatibleVersions.map((version: NDKEvent) => (
              <VersionItem 
                key={version.id} 
                onClick={() => handleVersionSelect(version.id)}
                $isSelected={selectedVersion === version.id}
                $isCompatible={true}
              >
                <VersionText>
                  {version.id.substring(0, 8)} (Compatible)
                </VersionText>
              </VersionItem>
            ))}
            {versions
              .filter((version: NDKEvent) => !compatibleVersions.some(v => v.id === version.id))
              .map((version: NDKEvent) => (
                <VersionItem 
                  key={version.id} 
                  onClick={() => handleVersionSelect(version.id)}
                  $isSelected={selectedVersion === version.id}
                  $isCompatible={compatibleVersions.some(v => v.id === version.id)}
                >
                  <VersionText>
                    {version.id.substring(0, 8)} (Incompatible)
                  </VersionText>
                </VersionItem>
              ))}
          </VersionList>
        )}
      </VersionSelectorContainer>

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